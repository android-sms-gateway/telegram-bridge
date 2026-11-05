package webhook

import (
	"bytes"
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
	// eventClaimTTL bounds how long a claim suppresses redelivery before the
	// delivery is confirmed. It must exceed one forward attempt: if the
	// confirmation never lands (crash, failed marker release) the pending
	// marker expires on its own, so a retry is delayed by minutes instead of
	// being suppressed for eventDedupTTL.
	eventClaimTTL = 2 * time.Minute
	// markerPending marks an event whose delivery is currently running. It is
	// written with eventClaimTTL and replaced by markerDone on success.
	markerPending = "pending"
	// markerDone marks an event delivered at least once. It is written with
	// eventDedupTTL and makes redeliveries duplicates.
	markerDone = "done"
)

// claimResult is the outcome of a de-duplication claim attempt.
type claimResult int

const (
	// claimNone: no marker constrains the event; forward it.
	claimNone claimResult = iota
	// claimAcquired: this call wrote the pending marker.
	claimAcquired
	// claimDuplicate: a confirmed marker exists; skip the event.
	claimDuplicate
	// claimInFlight: another delivery of the same event is still running.
	claimInFlight
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
// eventKey identifies the delivery (see internal/server/handlers/webhook) and
// moves through two marker states: a pending claim written before forwarding
// and a confirmed marker written after a successful forward. A confirmed
// marker means the event was accepted before, so it is skipped and reported
// as a duplicate. A pending marker means the first delivery is still running,
// so ErrEventInFlight is returned instead of acknowledging a delivery that
// has not completed yet. When the cache is unavailable the event is forwarded
// anyway: de-duplication is best-effort and must not stop message delivery.
//
// The pending claim is released when forwarding fails, so a retry of a failed
// delivery is not suppressed; a successful delivery keeps the confirmed
// marker for eventDedupTTL.
func (s *Service) Forward(ctx context.Context, eventKey, phoneNumber, message string) error {
	claim := s.claimEvent(ctx, eventKey)

	if claim == claimDuplicate {
		s.metrics.IncTotal(resultDuplicate)

		s.logger.Info("duplicate webhook event skipped", zap.String("event_key", eventKey))

		return nil
	}

	if claim == claimInFlight {
		s.metrics.IncTotal(resultDuplicate)

		s.logger.Info("webhook event delivery already in progress", zap.String("event_key", eventKey))

		return ErrEventInFlight
	}

	if err := s.bridgeSvc.Forward(ctx, phoneNumber, message); err != nil {
		if claim == claimAcquired {
			s.releaseEvent(ctx, eventKey)
		}

		s.metrics.IncForwardErrors()
		s.metrics.IncTotal(resultError)

		s.logger.Error("forward failed", zap.Error(err))

		return fmt.Errorf("webhook: forward: %w", err)
	}

	s.confirmEvent(ctx, eventKey)
	s.metrics.IncTotal(resultOK)

	s.logger.Info("message forwarded")

	return nil
}

// claimEvent reserves eventKey before forwarding. It reports which claim (if
// any) constrains this delivery: the marker this call wrote, a completed
// delivery to skip, a delivery still in flight, or none at all.
func (s *Service) claimEvent(ctx context.Context, eventKey string) claimResult {
	if eventKey == "" {
		return claimNone
	}

	err := s.eventCache.SetOrFail(ctx, eventKey, []byte(markerPending), cache.WithTTL(eventClaimTTL))
	switch {
	case err == nil:
		return claimAcquired
	case errors.Is(err, cache.ErrKeyExists):
		return s.existingClaim(ctx, eventKey)
	default:
		// The claim may have landed without its TTL (the Redis backend sets
		// the field and its expiry in two commands): roll back the marker we
		// may own so it cannot suppress the event forever, then forward
		// anyway.
		s.rollbackClaim(ctx, eventKey)
		s.logger.Warn(
			"event de-duplication cache unavailable, forwarding anyway",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)

		return claimNone
	}
}

// existingClaim reports what an already-present marker means for eventKey: a
// confirmed delivery is a duplicate, a pending marker means another forward is
// still running, and a marker that expired or cannot be read lets the event
// through instead of stopping it.
func (s *Service) existingClaim(ctx context.Context, eventKey string) claimResult {
	value, err := s.eventCache.Get(ctx, eventKey)
	switch {
	case errors.Is(err, cache.ErrKeyNotFound):
		// The marker expired or was released between SetOrFail and Get: treat
		// the event as unclaimed and forward it.
		return claimNone
	case err != nil:
		s.logger.Warn(
			"event de-duplication cache unavailable, forwarding anyway",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)

		return claimNone
	case bytes.Equal(value, []byte(markerDone)):
		return claimDuplicate
	default:
		return claimInFlight
	}
}

// confirmEvent promotes a pending claim to a confirmed marker after a
// successful forward, ignoring the request context: the delivery already
// happened, so its confirmation must not be lost with the client. A failed
// confirmation is not fatal either way: the pending claim expires after
// eventClaimTTL and a later redelivery is forwarded again, because
// de-duplication is best-effort and at-least-once delivery wins over a lost
// confirmation.
func (s *Service) confirmEvent(ctx context.Context, eventKey string) {
	if eventKey == "" {
		return
	}

	err := s.eventCache.Set(
		context.WithoutCancel(ctx),
		eventKey,
		[]byte(markerDone),
		cache.WithTTL(eventDedupTTL),
	)
	if err != nil {
		s.logger.Warn(
			"failed to confirm event de-duplication marker",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)
	}
}

// releaseEvent removes a pending claim after a failed forward, ignoring the
// request context: the client may already be gone, but the retry must still be
// allowed to pass. A failed removal only costs eventClaimTTL: the pending
// marker expires on its own instead of suppressing the event for
// eventDedupTTL.
func (s *Service) releaseEvent(ctx context.Context, eventKey string) {
	if err := s.eventCache.Delete(context.WithoutCancel(ctx), eventKey); err != nil {
		s.logger.Warn(
			"failed to release event de-duplication marker",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)
	}
}

// rollbackClaim removes a marker claimEvent may have written without a TTL, so
// a partially applied claim cannot suppress the event forever. It is
// best-effort: when the cache cannot delete either, the failure is logged and
// the event is forwarded with the marker still possibly present.
func (s *Service) rollbackClaim(ctx context.Context, eventKey string) {
	if err := s.eventCache.Delete(context.WithoutCancel(ctx), eventKey); err != nil {
		s.logger.Warn(
			"failed to roll back event de-duplication marker",
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
