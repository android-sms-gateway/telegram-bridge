package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"go.uber.org/zap"
)

// signatureMaxAge bounds how far the X-Timestamp value may be from the current
// time. The timestamp is part of the signed material, so rejecting old (or
// future) requests limits how long a captured request can be replayed.
const signatureMaxAge = 5 * time.Minute

// Service verifies webhook signatures and forwards accepted payloads to
// Telegram.
type Service struct {
	signingKey string

	bridge *bridge.Service

	metrics *Metrics

	logger *zap.Logger
}

// New creates a new webhook Service.
//
// cfg carries the SMSGate payload signing key taken from
// smsgate.webhook.signing_key by the config module. It must match the signing
// key configured on the device (Settings -> Webhooks -> Signing Key); an empty
// key is fail-closed and rejects every request.
func New(cfg Config, bridge *bridge.Service, metrics *Metrics, logger *zap.Logger) *Service {
	return &Service{
		signingKey: cfg.SigningKey,

		bridge: bridge,

		metrics: metrics,

		logger: logger,
	}
}

// Verify checks the request signature and records the rejected metric on
// failure. The signature is the hexadecimal HMAC-SHA256 digest of the raw
// request body followed by the Unix timestamp string, keyed with the
// configured signing key (SMSGate payload signing: X-Signature and
// X-Timestamp headers). The timestamp must be a Unix-seconds value within
// signatureMaxAge of the current time, so a captured request cannot be
// replayed indefinitely.
//
// Verification is fail-closed: an empty configured signing key rejects every
// request.
func (s *Service) Verify(body []byte, signature, timestamp string) error {
	if !s.verifySignature(body, signature, timestamp) {
		s.metrics.IncTotal(resultRejected)

		return ErrInvalidSignature
	}

	return nil
}

// Forward delivers the message payload to Telegram and records metrics.
func (s *Service) Forward(ctx context.Context, phoneNumber, message string) error {
	if err := s.bridge.Forward(ctx, phoneNumber, message); err != nil {
		s.metrics.IncForwardErrors()
		s.metrics.IncTotal(resultError)

		s.logger.Error("forward failed", zap.Error(err))

		return fmt.Errorf("webhook: forward: %w", err)
	}

	s.metrics.IncTotal(resultOK)

	s.logger.Info("message forwarded")

	return nil
}

// verifySignature compares the expected HMAC with the provided signature in
// constant time, after bounding the timestamp to the freshness window.
func (s *Service) verifySignature(body []byte, signature, timestamp string) bool {
	if s.signingKey == "" || signature == "" || timestamp == "" {
		return false
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	if age := time.Since(time.Unix(ts, 0)); age > signatureMaxAge || age < -signatureMaxAge {
		return false
	}

	mac := hmac.New(sha256.New, []byte(s.signingKey))
	mac.Write(body)
	mac.Write([]byte(timestamp))

	expected := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}
