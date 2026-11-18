package webhook

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/android-sms-gateway/client-go/smsgateway"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/go-core-fx/cachefx/cache"
	"go.uber.org/zap"
)

const (
	// signatureMaxAge bounds how far the X-Timestamp value may be from the
	// current time. The timestamp is part of the signed material, so rejecting
	// old (or future) requests limits how long a captured request can be
	// replayed.
	signatureMaxAge = 5 * time.Minute
	// eventDedupTTL is how long a delivered event suppresses redelivery.
	// SMSGate delivers webhooks at least once, so a duplicate can arrive long
	// after the first attempt; the window is intentionally wider than the
	// 5-minute signature freshness window.
	eventDedupTTL = 24 * time.Hour
	// forwardWaitTimeout bounds how long a request waits for the forward slot.
	// The wait is not cancelled by a client disconnect (Fiber passes a
	// fasthttp.RequestCtx, whose Done only closes on server shutdown), so the
	// timeout is what keeps a slow forward from queueing requests indefinitely.
	forwardWaitTimeout = 30 * time.Second
)

// Service verifies webhook signatures, de-duplicates events, and forwards
// accepted payloads to Telegram.
type Service struct {
	signingKey string

	bridgeSvc *bridge.Service

	storeSvc *store.Service

	eventCache cache.Cache

	// forwardSem serializes check-forward-mark with a single slot. Holding it
	// across the Telegram call is what lets the marker be written only after a
	// successful forward: a concurrent redelivery of the same event waits here,
	// then sees the marker and is skipped. Webhook traffic is low-volume (one
	// event per SMS), so one process-wide slot is simpler than per-key locks,
	// whose map would grow with the unbounded event ids. Waiting is bounded by
	// forwardWaitTimeout so a slow forward cannot queue requests indefinitely.
	forwardSem chan struct{}

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
//
// storeSvc owns the reply mapping written after every successful forward: it is
// the same store the reply path resolves from, so a forwarded message and a
// reply to it always agree on the chat and the message id.
func New(
	cfg Config,
	bridgeSvc *bridge.Service,
	eventCache cache.Cache,
	storeSvc *store.Service,
	metrics *Metrics,
	logger *zap.Logger,
) *Service {
	return &Service{
		signingKey: cfg.SigningKey,

		bridgeSvc: bridgeSvc,

		storeSvc: storeSvc,

		eventCache: eventCache,

		forwardSem: make(chan struct{}, 1),

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
// eventKey identifies the delivery (see internal/server/handlers/webhook). The
// check, the forward and the marker write run under one lock, so concurrent
// deliveries serialize: the loser waits, sees the marker the winner wrote and
// is skipped. The marker exists only after Telegram accepted the message and
// only its presence matters; a failed forward writes nothing, so a retry is
// never suppressed. When the cache is unavailable the event is forwarded
// anyway: de-duplication is best-effort and must not stop message delivery.
//
// A delivered event keeps its marker for eventDedupTTL.
//
// After a successful delivery the reply mappings are written: the chat each
// part went to plus the id Telegram gave it now resolve back to the sender,
// which is what lets an answer to any part of the bubble leave as an SMS. The
// same holds when a later part fails: the parts Telegram already accepted are
// mapped before the error is reported, so a reply to them still resolves to
// the sender even though the device is told the delivery failed.
//
// A mapping that cannot be written is logged and does not fail the request.
// The SMS has already reached Telegram at that point, so answering with an
// error would make the device retry the delivery and forward the same SMS a
// second time. The only consequence is that replies to that one message stay
// unresolvable, which the reply path reports to the operator in chat.
func (s *Service) Forward(ctx context.Context, eventKey, phoneNumber, message string) error {
	waitCtx, cancel := context.WithTimeout(ctx, forwardWaitTimeout)
	defer cancel()

	select {
	case s.forwardSem <- struct{}{}:
		defer func() { <-s.forwardSem }()
	case <-waitCtx.Done():
		return fmt.Errorf("wait for forward slot: %w", waitCtx.Err())
	}

	if s.isDelivered(ctx, eventKey) {
		s.metrics.IncTotal(resultDuplicate)

		s.logger.Info("duplicate webhook event skipped", zap.String("event_key", eventKey))

		return nil
	}

	forwarded, err := s.bridgeSvc.Forward(ctx, phoneNumber, message)

	for _, part := range forwarded {
		s.storeMapping(ctx, part, phoneNumber)
	}

	if err != nil {
		s.metrics.IncForwardErrors()
		s.metrics.IncTotal(resultError)

		s.logger.Error("forward failed", zap.Error(err))

		return fmt.Errorf("forward: %w", err)
	}

	s.markDelivered(ctx, eventKey)

	s.metrics.IncTotal(resultOK)

	s.logger.Info("message forwarded",
		zap.Int64("chat_id", forwarded[0].ChatID),
		zap.Int("parts", len(forwarded)),
	)

	return nil
}

// isDelivered reports whether eventKey already delivered successfully. An
// empty key carries no identity and is never deduplicated; a cache read error
// lets the event through instead of stopping it, because de-duplication must
// not break message delivery.
func (s *Service) isDelivered(ctx context.Context, eventKey string) bool {
	if eventKey == "" {
		return false
	}

	_, err := s.eventCache.Get(ctx, eventKey)
	switch {
	case err == nil:
		return true
	case errors.Is(err, cache.ErrKeyNotFound):
		return false
	default:
		s.logger.Warn(
			"event de-duplication cache unavailable, forwarding anyway",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)

		return false
	}
}

// markDelivered records a successful forward so redeliveries are skipped. It
// runs on an uncancelled context: the delivery already happened, so the marker
// must not be lost with the client. A failed marker write only costs
// de-duplication for this one event, never the delivery itself.
func (s *Service) markDelivered(ctx context.Context, eventKey string) {
	if eventKey == "" {
		return
	}

	// The marker value is never read; only the key's presence matters.
	err := s.eventCache.Set(context.WithoutCancel(ctx), eventKey, []byte("1"), cache.WithTTL(eventDedupTTL))
	if err != nil {
		s.logger.Warn(
			"failed to record processed webhook event",
			zap.String("event_key", eventKey),
			zap.Error(err),
		)
	}
}

// storeMapping records the chat and Telegram message id of a delivered part
// together with the SMS sender it was forwarded from. Every part of a split
// message gets its own mapping, so an answer to any part resolves to a sender.
//
// The domain record carries no timestamps: the store stamps created_at and
// expires_at from its own TTL, so the retention of a mapping is the store's
// decision and not a constant duplicated in this package.
//
// The write runs on a context detached from the request: the caller's context
// is the fasthttp request context, which fasthttp cancels on server shutdown
// while the request is still in flight. Telegram has already accepted the part
// at this point, so the mapping must be written even then — a dropped write
// would leave a delivered bubble with no sender to reply to, and the device
// still gets its 200 either way. The write is a local bbolt put, so detaching
// the cancellation cannot strand it on a remote call.
func (s *Service) storeMapping(ctx context.Context, forwarded bridge.Forwarded, phoneNumber string) {
	mapping := store.Mapping{
		MessageRef: store.MessageRef{
			ChatID:    forwarded.ChatID,
			MessageID: forwarded.MessageID,
		},
		Phone: phoneNumber,
	}

	if err := s.storeSvc.Put(context.WithoutCancel(ctx), mapping); err != nil {
		s.logger.Error("reply mapping not stored, replies to this message cannot be routed",
			zap.Error(err),
			zap.Int64("chat_id", forwarded.ChatID),
			zap.Int("telegram_message_id", forwarded.MessageID),
		)
	}
}

// verifySignature delegates to the SMSGate client SDK: it compares the expected
// HMAC with the provided signature in constant time, after bounding the
// timestamp to the freshness window.
func (s *Service) verifySignature(body []byte, signature, timestamp string) bool {
	return smsgateway.VerifyWebhookSignatureWithin(s.signingKey, body, timestamp, signature, signatureMaxAge)
}
