package bridge

import (
	"context"
	"fmt"
	"html"
	"strings"
	"unicode/utf16"

	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/zap"
)

const (
	// forwardMessagePrefix introduces every forwarded message.
	forwardMessagePrefix = "New message from "
	// forwardMessageSep separates the escaped sender from the message body.
	forwardMessageSep = ":\n"
	// forwardMessageFormat is the rendering of a forwarded SMS, matching the
	// example-telegram-forwarder-fn contract (HTML parse mode).
	forwardMessageFormat = forwardMessagePrefix + "<b>%s</b>" + forwardMessageSep + "<code>%s</code>"
	// telegramMessageLimit is Telegram's maximum sendMessage length in UTF-16
	// code units after entities parsing.
	telegramMessageLimit = 4096
)

// Service forwards SMSGate messages to the configured Telegram chat.
type Service struct {
	chatID int64

	bot *telegofx.Bot

	logger *zap.Logger
}

// New creates a new Service.
//
// cfg carries the forward target taken from bridge.chat_id by the config
// module; a zero ChatID means "not configured" and makes every Forward call fail
// with ErrNoChatID.
func New(cfg Config, bot *telegofx.Bot, logger *zap.Logger) *Service {
	return &Service{
		chatID: cfg.ChatID,

		bot: bot,

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
//
// Messages too long for a single Telegram message are split into several
// formatted messages; a failure on a later part returns an error after the
// earlier parts have already been delivered.
func (s *Service) Forward(ctx context.Context, phoneNumber, message string) error {
	if s.chatID == 0 {
		return fmt.Errorf("forward: %w", ErrNoChatID)
	}

	chunks, err := formatForwardMessageChunks(phoneNumber, message)
	if err != nil {
		return fmt.Errorf("forward: %w", err)
	}

	for _, text := range chunks {
		_, err = s.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: s.chatID, Username: ""},
			Text:      text,
			ParseMode: telego.ModeHTML,
		})
		if err != nil {
			return fmt.Errorf("forward: send telegram message: %w", err)
		}
	}

	s.logger.Info("message forwarded to telegram", zap.Int("parts", len(chunks)))

	return nil
}

// formatForwardMessageChunks renders a forwarded message, splitting the SMS
// text into as many formatted messages as needed so each stays within
// Telegram's per-message limit after entities parsing.
//
// The split happens on the raw message before formatting, so an HTML entity is
// never cut in half: every chunk is escaped on its own.
func formatForwardMessageChunks(phoneNumber, message string) ([]string, error) {
	budget := messageBudget(phoneNumber)
	if budget <= 0 {
		return nil, ErrMessageTooLong
	}

	chunks := splitText(message, budget)

	formatted := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		formatted = append(formatted, FormatForwardMessage(phoneNumber, chunk))
	}

	return formatted, nil
}

// messageBudget is the number of message characters that fit in one Telegram
// message for the given sender, after the fixed prefix and separator.
func messageBudget(phoneNumber string) int {
	return telegramMessageLimit - textLength(forwardMessagePrefix) -
		textLength(phoneNumber) - textLength(forwardMessageSep)
}

// splitText splits s into chunks of at most limit UTF-16 code units, breaking
// only on rune boundaries so a multi-byte rune is never split.
func splitText(s string, limit int) []string {
	if textLength(s) <= limit {
		return []string{s}
	}

	chunks := make([]string, 0, textLength(s)/limit+1)

	var b strings.Builder

	used := 0
	for _, r := range s {
		size := utf16.RuneLen(r)
		if used+size > limit {
			chunks = append(chunks, b.String())
			b.Reset()

			used = 0
		}

		b.WriteRune(r)

		used += size
	}

	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}

	return chunks
}

// textLength reports the length Telegram counts for s: the number of UTF-16
// code units after HTML entities are parsed.
func textLength(s string) int {
	n := 0

	for _, r := range s {
		n += utf16.RuneLen(r)
	}

	return n
}

func escapeHTML(text string) string {
	return html.EscapeString(text)
}
