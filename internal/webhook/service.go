package webhook

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/android-sms-gateway/client-go/smsgateway"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/go-core-fx/cachefx/cache"
	"go.uber.org/zap"
)

const (
	// signatureMaxAge bounds how far the X-Timestamp value may be from the
	// current time. The timestamp is part of the signed material, so rejecting
	// old (or future) requests limits how long a captured request can be
	// replayed.
	signatureMaxAge = 5 * time.Minute
	// eventDedupTTL is how long an accepted event id suppresses redelivery.
	// SMSGate delivers webhooks at least once, so a duplicate can arrive long
	// after the first attempt; the window is intentionally wider than the
	// 5-minute signature freshness window.
	eventDedupTTL = 24 * time.Hour
)

// Service verifies webhook signatures, de-duplicates events, and forwards
// accepted payloads to Telegram.
type Service struct {
	signingKey string

	bridgeSvc *bridge.Service

	eventCache cache.Cache

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
// eventCache is the webhook-owned de-duplication cache created by the module
// through the application cache factory.
func New(
	cfg Config,
	bridgeSvc *bridge.Service,
	eventCache cache.Cache,
	metrics *Metrics,
	logger *zap.Logger,
) *Service {
	return &Service{
		signingKey: cfg.SigningKey,

		bridgeSvc: bridgeSvc,

		eventCache: eventCache,

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

// Forward de-duplicates the event and delivers the message payload to
// Telegram.
//
// eventKey identifies the delivery (see internal/server/handlers/webhook). A
// key already present in the cache means the event was accepted before, so it
// is skipped and reported as a duplicate. When the cache is unavailable the
// event is forwarded anyway: de-duplication is best-effort and must not stop
// message delivery.
//
// The marker is written before forwarding and removed when forwarding fails,
// so a retry of a failed delivery is not suppressed. A successful delivery
// keeps the marker for eventDedupTTL.
func (s *Service) Forward(ctx context.Context, eventKey, phoneNumber, message string) error {
	duplicate, marked := s.claimEvent(ctx, eventKey)
	if duplicate {
		s.metrics.IncTotal(resultDuplicate)

		s.logger.Info("duplicate webhook event skipped", zap.String("event_key", eventKey))

		return nil
	}

	if err := s.bridgeSvc.Forward(ctx, phoneNumber, message); err != nil {
		if marked {
			s.releaseEvent(ctx, eventKey)
		}

		s.metrics.IncForwardErrors()
		s.metrics.IncTotal(resultError)

		s.logger.Error("forward failed", zap.Error(err))

		return fmt.Errorf("webhook: forward: %w", err)
	}

	s.metrics.IncTotal(resultOK)

	s.logger.Info("message forwarded")

	return nil
}

// claimEvent reserves eventKey in the de-duplication cache. The first result
// is true when the event was already claimed; the second is true when this
// call wrote the marker (so a failed forward can release it).
func (s *Service) claimEvent(ctx context.Context, eventKey string) (bool, bool) {
	if eventKey == "" {
		return false, false
	}

	// The marker value is never read; only the key's presence matters.
	err := s.eventCache.SetOrFail(ctx, eventKey, []byte("1"), cache.WithTTL(eventDedupTTL))
	switch {
	case err == nil:
		return false, true
	case errors.Is(err, cache.ErrKeyExists):
		return true, false
	default:
		s.logger.Warn(
			"event de-duplication cache unavailable, forwarding anyway",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)

		return false, false
	}
}

// releaseEvent removes a claimed marker after a failed forward, ignoring the
// request context: the client may already be gone, but the retry must still be
// allowed to pass.
func (s *Service) releaseEvent(ctx context.Context, eventKey string) {
	if err := s.eventCache.Delete(context.WithoutCancel(ctx), eventKey); err != nil {
		s.logger.Warn(
			"failed to release event de-duplication marker",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)
	}
}

// verifySignature delegates to the SMSGate client SDK: it compares the expected
// HMAC with the provided signature in constant time, after bounding the
// timestamp to the freshness window.
func (s *Service) verifySignature(body []byte, signature, timestamp string) bool {
	return smsgateway.VerifyWebhookSignatureWithin(s.signingKey, body, timestamp, signature, signatureMaxAge)
}
