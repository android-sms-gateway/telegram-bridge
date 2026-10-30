package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"go.uber.org/zap"
)

// Service verifies webhook signatures and forwards accepted payloads to
// Telegram.
type Service struct {
	signingKey string

	bridge *bridge.Service
	repo   *store.Repository

	metrics *Metrics

	logger *zap.Logger
}

// New creates a new webhook Service.
//
// cfg carries the SMSGate payload signing key taken from
// smsgate.webhook.signing_key by the config module. It must match the signing
// key configured on the device (Settings -> Webhooks -> Signing Key); an empty
// key is fail-closed and rejects every request.
//
// repo owns the reply mapping written after every successful forward: it is the
// same repository the reply path resolves from, so a forwarded message and a
// reply to it always agree on the chat and the message id.
func New(cfg Config, bridge *bridge.Service, repo *store.Repository, metrics *Metrics, logger *zap.Logger) *Service {
	return &Service{
		signingKey: cfg.SigningKey,

		bridge: bridge,
		repo:   repo,

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
//
// After a successful delivery the reply mapping is written: the chat the
// message went to plus the id Telegram gave it now resolve back to the sender,
// which is what lets an answer to the bubble leave as an SMS.
//
// A mapping that cannot be written is logged and does not fail the request. The
// SMS has already reached Telegram at that point, so answering with an error
// would make the device retry the delivery and forward the same SMS a second
// time. The only consequence is that replies to that one message stay
// unresolvable, which the reply path reports to the operator in chat.
func (s *Service) Forward(ctx context.Context, phoneNumber, message string) error {
	forwarded, err := s.bridge.Forward(ctx, phoneNumber, message)
	if err != nil {
		s.metrics.IncForwardErrors()
		s.metrics.IncTotal(resultError)

		s.logger.Error("forward failed", zap.Error(err))

		return fmt.Errorf("webhook: forward: %w", err)
	}

	s.storeMapping(ctx, forwarded, phoneNumber)

	s.metrics.IncTotal(resultOK)

	s.logger.Info("message forwarded",
		zap.Int64("chat_id", forwarded.ChatID),
		zap.Int("telegram_message_id", forwarded.MessageID),
	)

	return nil
}

// storeMapping records the chat and Telegram message id of a delivered message
// together with the SMS sender it was forwarded from.
//
// The mapping is written with a zero expiry: the store stamps expires_at from
// its own TTL, so the retention of a mapping is the store's decision and not a
// constant duplicated in this package.
func (s *Service) storeMapping(ctx context.Context, forwarded bridge.Forwarded, phoneNumber string) {
	mapping := store.Mapping{
		Phone:     phoneNumber,
		CreatedAt: time.Now(),
		ExpiresAt: time.Time{},
	}

	if err := s.repo.Put(ctx, forwarded.ChatID, forwarded.MessageID, mapping); err != nil {
		s.logger.Error("reply mapping not stored, replies to this message cannot be routed",
			zap.Error(err),
			zap.Int64("chat_id", forwarded.ChatID),
			zap.Int("telegram_message_id", forwarded.MessageID),
		)
	}
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
