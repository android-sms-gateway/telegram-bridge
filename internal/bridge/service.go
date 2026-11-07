package bridge

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode/utf16"

	"github.com/android-sms-gateway/telegram-bridge/internal/smsgate"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/zap"
)

const (
	// forwardMessagePrefix introduces every forwarded message.
	forwardMessagePrefix = "New message from "
	// forwardMessageSep separates the escaped sender from the message body.
	forwardMessageSep = ":\n"
	// telegramMessageLimit is Telegram's maximum sendMessage length in UTF-16
	// code units after entities parsing.
	telegramMessageLimit = 4096
)

// Service is the bidirectional bridge between SMSGate and Telegram: Forward
// delivers an SMSGate message to the configured chat, and Reply answers a
// forwarded bubble back to the SMS sender it came from.
//
// Both directions live on one service because both are keyed on the same
// identity, the chat plus the id Telegram gave the forwarded message: the
// forward reports it, internal/webhook writes it to internal/store, and the
// reply resolves it.
type Service struct {
	chatID int64

	bot *telegofx.Bot

	smsgateSvc *smsgate.Service

	repo *store.Repository

	logger *zap.Logger
}

// New creates a new Service.
//
// cfg carries the forward target taken from bridge.chat_id by the config
// module, where a zero ChatID means "not configured" and makes every Forward
// call fail with ErrNoChatID.
//
// repo resolves the sender of a forwarded message and sms queues the answer,
// both of them shared with the forward path.
//
// Whether an update is allowed to become a reply at all is not decided here: the
// routes of internal/bot filter by Telegram sender before they call this service,
// so everything that arrives has already been authorized.
func New(
	cfg Config,
	bot *telegofx.Bot,
	repo *store.Repository,
	smsgateSvc *smsgate.Service,
	logger *zap.Logger,
) *Service {
	return &Service{
		chatID: cfg.ChatID,

		bot: bot,

		smsgateSvc: smsgateSvc,

		repo: repo,

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

// Forward sends the given message to the configured chat and returns what
// Telegram made of every part it was split into.
//
// Messages too long for a single Telegram message are split into several
// formatted messages; a failure on a later part returns the earlier parts
// together with the error, so the caller can store their reply mappings before
// it reports the failure. A reply to a part Telegram already accepted must
// resolve to the sender even though the forward as a whole failed.
//
// The delivered chats and message ids are the identity of the forward: the
// reply path resolves them back to the SMS sender, so a reply to any part of a
// split message must stay routable.
func (s *Service) Forward(ctx context.Context, phoneNumber, message string) ([]Forwarded, error) {
	if s.chatID == 0 {
		return nil, fmt.Errorf("forward: %w", ErrNoChatID)
	}

	chunks, err := formatForwardMessageChunks(phoneNumber, message)
	if err != nil {
		return nil, fmt.Errorf("forward: %w", err)
	}

	forwarded := make([]Forwarded, 0, len(chunks))

	var sent *telego.Message

	for _, text := range chunks {
		sent, err = s.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: s.chatID, Username: ""},
			Text:      text,
			ParseMode: telego.ModeHTML,
		})
		if err != nil {
			return forwarded, fmt.Errorf("forward: send telegram message: %w", err)
		}

		forwarded = append(forwarded, Forwarded{ChatID: s.chatID, MessageID: sent.MessageID})
	}

	s.logger.Info("message forwarded to telegram", zap.Int("parts", len(chunks)))

	return forwarded, nil
}

// The reply direction of the bridge: an answer typed in Telegram leaves as an
// SMS to the sender the bridge forwarded it from.
//
// The operator answers a forwarded bubble in Telegram, and Service.Reply sends
// that answer back to the SMS sender: it resolves the chat and the replied-to
// Telegram message id through the durable store, which is exactly the pair
// Forward reported and internal/webhook recorded, and hands the text to the
// SMSGate server API.
//
// Who may write such an answer is not decided in this package: the route of
// internal/bot that calls Reply filters the updates by Telegram sender while it
// is being registered, so an unauthorized one never arrives here. What is left
// are two gates in front of a send, each fail-closed: a reply without text
// never reaches the store or the SMSGate API, and a reply whose replied-to
// message does not resolve to a sender is never queued. Reply reports what
// happened as a typed Outcome and logs the internal reason for a failure, so an
// error, a URL or a credential can never leave this package: the telego adapter
// in internal/bot/handlers/reply only ever learns which of the five outcomes
// happened, and on success the recipient and the id the server assigned.

// Reply runs the reply path for one answer typed in Telegram and reports what
// happened.
//
// The name says which direction it is: on a service that also forwards, a bare
// Send would be ambiguous with the SMSGate client it delegates to, and the
// caller would have to know which of the two a Send refers to.
//
// The gates are applied in a fixed order, each of them fail-closed: a reply
// without text never reaches the store or the SMSGate API, and a reply whose
// replied-to message does not resolve to a sender is never queued.
//
// Every branch returns an outcome and never an error: the cause of a failure is
// logged here, because the caller renders a fixed sentence and must not be able
// to write an internal reason into the chat.
func (s *Service) Reply(ctx context.Context, in Answer) Outcome {
	if in.Text == "" {
		return Outcome{Kind: OutcomeEmptyText, Phone: "", MessageID: ""}
	}

	mapping, err := s.repo.Resolve(ctx, in.ChatID, in.ReplyToMessageID)
	if err != nil {
		return s.lookupFailed(in, err)
	}

	return s.queue(ctx, mapping.Phone, in.Text)
}

// lookupFailed answers a reply whose mapping could not be resolved.
//
// An unknown or expired mapping is an expected outcome, not a failure of the
// service. Anything else (a closed database, an unreadable record) is logged with
// its cause and reported as its own outcome, so the internal reason never
// reaches the chat.
func (s *Service) lookupFailed(in Answer, err error) Outcome {
	if errors.Is(err, store.ErrMappingNotFound) {
		s.logger.Info("reply target is unknown or expired",
			zap.Int64("chat_id", in.ChatID),
			zap.Int("reply_to_message_id", in.ReplyToMessageID),
		)

		return Outcome{Kind: OutcomeNotForwarded, Phone: "", MessageID: ""}
	}

	s.logger.Error("reply mapping lookup failed", zap.Error(err))

	return Outcome{Kind: OutcomeLookupFailed, Phone: "", MessageID: ""}
}

// queue hands the answer to the SMSGate server and reports what it answered.
//
// A queued send carries the recipient and the id the server assigned, so the
// operator can trace the message. A refused one carries neither the sender
// number nor the server answer: the cause is logged and the caller writes a
// fixed sentence.
func (s *Service) queue(ctx context.Context, phone, text string) Outcome {
	messageID, err := s.smsgateSvc.Send(ctx, phone, text)
	if err != nil {
		s.logger.Error("reply send failed", zap.Error(err), zap.String("phone", phone))

		return Outcome{Kind: OutcomeSendFailed, Phone: "", MessageID: ""}
	}

	if messageID == "" {
		messageID = unknownMessageID
	}

	s.logger.Info("reply sent",
		zap.String("phone", phone),
		zap.String("smsgate_message_id", messageID),
	)

	return Outcome{Kind: OutcomeSent, Phone: phone, MessageID: messageID}
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
