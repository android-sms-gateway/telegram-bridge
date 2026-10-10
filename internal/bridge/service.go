package bridge

import (
	"context"
	"fmt"
	"html"

	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/zap"
)

// forwardMessageFormat is the rendering of a forwarded SMS, matching the
// example-telegram-forwarder-fn contract (HTML parse mode).
const forwardMessageFormat = "New message from <b>%s</b>:\n<code>%s</code>"

// Service forwards SMSGate messages to the configured Telegram chat.
type Service struct {
	bot    *telegofx.Bot
	chatID int64

	logger *zap.Logger
}

// New creates a new Service.
//
// cfg carries the forward target taken from bridge.chat_id by the config
// module; a zero ChatID means "not configured" and makes every Forward call fail
// with ErrNoChatID.
func New(cfg Config, bot *telegofx.Bot, logger *zap.Logger) *Service {
	return &Service{
		bot:    bot,
		chatID: cfg.ChatID,

		logger: logger,
	}
}

// FormatForwardMessage renders a forwarded message with HTML escaping.
//
// Both fields are escaped: the message text is placed inside a <code> block
// and the phone number inside a <b> block, so untrusted content can never
// break the HTML entities of the Telegram message.
func FormatForwardMessage(phoneNumber, message string) string {
	return fmt.Sprintf(forwardMessageFormat, escapeHTML(phoneNumber), escapeHTML(message))
}

// Forward sends the given message to the configured chat.
func (s *Service) Forward(ctx context.Context, phoneNumber, message string) error {
	if s.chatID == 0 {
		return fmt.Errorf("forward: %w", ErrNoChatID)
	}

	text := FormatForwardMessage(phoneNumber, message)

	if _, err := s.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: s.chatID, Username: ""},
		Text:      text,
		ParseMode: telego.ModeHTML,
	}); err != nil {
		return fmt.Errorf("forward: send telegram message: %w", err)
	}

	s.logger.Info("message forwarded to telegram")

	return nil
}

func escapeHTML(text string) string {
	return html.EscapeString(text)
}
