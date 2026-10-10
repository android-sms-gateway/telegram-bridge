package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"go.uber.org/zap"
)

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
// X-Timestamp headers).
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
// constant time.
func (s *Service) verifySignature(body []byte, signature, timestamp string) bool {
	if s.signingKey == "" || signature == "" || timestamp == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(s.signingKey))
	mac.Write(body)
	mac.Write([]byte(timestamp))

	expected := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}
