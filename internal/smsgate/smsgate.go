package smsgate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/android-sms-gateway/client-go/rest"
	"github.com/android-sms-gateway/client-go/smsgateway"
	"github.com/cenkalti/backoff/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// retryMaxElapsed bounds the total time the retry loop spends on one send.
//
// A Telegram update is handled synchronously, so an unbounded retry would keep
// the update pipeline busy behind a failing SMSGate server. After this budget
// the reply is reported to the chat as failed and the operator retries.
const retryMaxElapsed = 30 * time.Second

// statusCodeMarker is what client-go (rest.formatError) puts in front of the
// HTTP status code of every error that is neither a 400, a 409 nor a 5xx.
const statusCodeMarker = "unexpected status code "

// Service enqueues SMS through the SMSGate server REST API.
//
// It holds no state of its own: the queued message, its id and its delivery
// state live in the SMSGate server.
type Service struct {
	timeout time.Duration

	client *smsgateway.Client

	// configured records whether the API credentials were supplied, so the
	// password is never kept in a second field of this struct.
	configured bool

	logger *zap.Logger
}

// New creates a new Service.
//
// cfg carries the SMSGate server address and the basic-auth credentials taken
// from the raw smsgate section by the config module. Missing credentials are a
// warning rather than a startup failure: forwarding keeps working without them,
// and only the reply path, which fails closed on its first send, is affected.
func New(cfg Config, logger *zap.Logger) *Service {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	configured := cfg.Username != "" && cfg.Password != ""

	if !configured {
		logger.Warn("smsgate api credentials are not configured, replies cannot be sent")
	}

	return &Service{
		timeout: timeout,

		client: smsgateway.NewClient(smsgateway.Config{
			BaseURL:  cfg.BaseURL,
			User:     cfg.Username,
			Password: cfg.Password,

			Client: nil,
			Token:  "",
		}),

		configured: configured,

		logger: logger,
	}
}

// Send queues text as an SMS to phone and returns the message id of the queued
// message.
//
// One id is generated per Send, before the first attempt, and every retry
// carries it: the server enforces uniqueness of (id, device), so a retry after
// a timeout collides with the insert the first attempt already made and is
// answered with a 409 instead of queueing a second SMS. Without a stable id the
// server would generate a fresh id per attempt and both would be queued. That
// 409 means "already queued" and is reported as success with the generated id,
// because the error response carries no id of its own.
//
// A 4xx is permanent and is never retried, except 408 Request Timeout and 429
// Too Many Requests, which a retry may clear. Anything else (network, timeout,
// 5xx) is retried with an exponential backoff until it succeeds or
// retryMaxElapsed is spent.
func (s *Service) Send(ctx context.Context, phone, text string) (string, error) {
	if !s.configured {
		return "", fmt.Errorf("smsgate: send: %w", ErrNoCredentials)
	}

	sms := smsgateway.Message{
		TextMessage: &smsgateway.TextMessage{
			Text: text,
		},
		PhoneNumbers: []string{phone},
		Priority:     smsgateway.PriorityDefault,

		ID:                 uuid.NewString(),
		Message:            "",
		DataMessage:        nil,
		MmsMessage:         nil,
		DeviceID:           "",
		IsEncrypted:        false,
		SimNumber:          nil,
		WithDeliveryReport: nil,
		TTL:                nil,
		ValidUntil:         nil,
		ScheduleAt:         nil,
	}

	operation := func() (string, error) {
		opCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()

		state, err := s.client.Send(opCtx, sms)
		if rest.IsConflict(err) {
			s.logger.Info("message already enqueued", zap.String("phone", phone))

			return sms.ID, nil
		}

		if rest.IsClientError(err) {
			if transientClientStatus(err) {
				return "", fmt.Errorf("smsgate: send: %w", err)
			}

			return "", backoff.Permanent(fmt.Errorf("smsgate: send: %w", err))
		}

		if err != nil {
			return "", fmt.Errorf("smsgate: send: %w", err)
		}

		s.logger.Info("message enqueued",
			zap.String("id", state.ID),
			zap.String("phone", phone),
			zap.String("state", string(state.State)),
		)

		return state.ID, nil
	}

	id, err := backoff.Retry(ctx, operation,
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxElapsedTime(retryMaxElapsed),
	)
	if err != nil {
		return "", fmt.Errorf("smsgate: send: %w", err)
	}

	return id, nil
}

// transientClientStatus reports whether err carries one of the two 4xx statuses
// a retry may clear: 408 Request Timeout and 429 Too Many Requests.
//
// client-go classifies every 4xx that is not a 400 or a 409 as rest.ErrClient
// and exposes the status only inside the error text ("unexpected status code
// %d ..."), so that text is the single place the number is readable from. An
// error without such a status keeps the permanent treatment it had before the
// two transient statuses were told apart.
func transientClientStatus(err error) bool {
	msg := err.Error()
	idx := strings.Index(msg, statusCodeMarker)
	if idx < 0 {
		return false
	}

	var code int
	if _, serr := fmt.Sscanf(msg[idx:], statusCodeMarker+"%d", &code); serr != nil {
		return false
	}

	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests
}
