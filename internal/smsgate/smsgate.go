package smsgate

import (
	"context"
	"fmt"
	"time"

	"github.com/android-sms-gateway/client-go/rest"
	"github.com/android-sms-gateway/client-go/smsgateway"
	"github.com/cenkalti/backoff/v5"
	"go.uber.org/zap"
)

// retryMaxElapsed bounds the total time the retry loop spends on one send.
//
// A Telegram update is handled synchronously, so an unbounded retry would keep
// the update pipeline busy behind a failing SMSGate server. After this budget
// the reply is reported to the chat as failed and the operator retries.
const retryMaxElapsed = 30 * time.Second

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

// Send queues text as an SMS to phone and returns the message id the SMSGate
// server assigned to it.
//
// A 409 from the server means the message is already queued and is reported as
// success, so a retried send never queues a second SMS. A 4xx that is not a
// conflict is permanent and is never retried; anything else (network, timeout,
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

		ID:                 "",
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

			return state.ID, nil
		}

		if rest.IsClientError(err) {
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
