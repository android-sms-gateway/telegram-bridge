// Package reply answers Telegram replies in chat.
//
// The operator answers a forwarded bubble, and this handler is the telego
// adapter around internal/bridge: it turns an update into the answer the bridge
// works with, renders the outcome of that call into the short sentence the
// operator reads, and sends it back to the chat the reply came from.
//
// The handler is deliberately thin. It owns no repository and no SMS client: the
// mapping store and the send live in the bridge service, which also logs the
// cause behind every failure. The module config is the one setting surface it
// takes, and it uses it only to build the filter of its route. Whatever the
// operator sees in chat is a fixed sentence, never an internal detail. Two
// things are decided by the route, not by the handler body: the shape of the
// update, owned by the telegofx predicates package for every handler of the
// bot, and WHO may drive this route, the filter built from the allowlist this
// handler is constructed with.
package reply

import (
	"fmt"

	"github.com/android-sms-gateway/telegram-bridge/internal/bot/config"
	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handler"
	botpredicates "github.com/android-sms-gateway/telegram-bridge/internal/bot/predicates"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/go-core-fx/telegofx"
	"github.com/go-core-fx/telegofx/predicates"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

const (
	// notForwardedText covers both a message the bridge never forwarded and a
	// mapping that has expired: the service reports them the same way, and from
	// the operator they mean the same thing.
	notForwardedText = "That message is not a forwarded SMS anymore, so I cannot tell " +
		"who to reply to."

	// storeFailedText is sent when the mapping lookup failed for any other reason,
	// for example an unreadable database.
	storeFailedText = "I could not look up the sender of that message. Please check the bridge logs."

	// sendFailedText is sent when the SMSGate server refused or did not answer the
	// send. It carries no server message, URL or credential.
	sendFailedText = "The SMS could not be queued. Please check the bridge logs."

	// emptyTextText is sent for a reply that carries no text (a photo, for
	// example): there is nothing to send.
	emptyTextText = "Reply to a forwarded message with text and it will be sent as an SMS."

	// ackFormat acknowledges a queued send with the recipient and the id the
	// SMSGate server assigned to the message.
	ackFormat = "Sent to %s (SMSGate message id: %s)."
)

// Handler answers Telegram replies as SMS.
type Handler struct {
	bridgeSvc *bridge.Service

	// allowed is the authorization filter of the reply route. It is built from
	// the module config once, in the constructor, and read by every update the
	// route accepts.
	allowed botpredicates.AllowedUsers
}

// The bot module collects handlers through this interface.
var _ handler.Handler = (*Handler)(nil)

// New creates a new reply Handler.
//
// The bridge service owns the mapping store and the SMS client; this
// constructor only binds it to a route. The config is taken here, and not in
// Register, so the filter the route is built from is fixed before the first
// update arrives and cannot be swapped by whatever the module collects.
//
// The handler owns no logger of its own: the route filters the updates, so no
// update that reached here is worth a warning, and a failure to answer is
// returned as an error for telegohandler to report.
func New(service *bridge.Service, cfg config.Config) handler.Handler {
	return &Handler{
		bridgeSvc: service,

		allowed: botpredicates.NewAllowedUsers(cfg.AllowedUserIDs),
	}
}

// Register registers the reply route on the shared router.
//
// The route is narrowed twice, and both narrowings happen here, while the route
// is being registered, not in the handler body:
//
//   - the shape: only a message that answers another message reaches
//     handleReply, so ordinary chat traffic never enters the reply path and a
//     new route here cannot shadow the /start handler;
//   - the sender: only an update from an allowed Telegram user reaches it, so an
//     unauthorized one is never answered in chat and never reaches the bridge
//     service at all. telegohandler runs the first matching route only, and
//     /start is registered before this one, so a command sent as a reply is
//     still answered by the start handler.
func (h *Handler) Register(router *telegofx.Router) {
	router.Handle(
		h.handleReply,
		predicates.MessageWithReplyToMessage(),
		h.allowed.Match,
	)
}

// handleReply answers a message that replies to a forwarded bubble.
//
// The route predicate guarantees the update carries a message, a sender and a
// replied-to message, so the shape of the update is not re-checked here: an
// ordinary chat message is filtered out before it gets here, and there is no
// branch left that could reach the chat with a sentence about a malformed update.
//
// Every failure ends in a short message to the chat and a nil error: a Telegram
// update that could not be routed must not crash the bot or be retried by
// Telegram forever.
func (h *Handler) handleReply(ctx *th.Context, update telego.Update) error {
	message := update.Message

	outcome := h.bridgeSvc.Reply(ctx, bridge.Answer{
		UserID:           message.From.ID,
		ChatID:           message.Chat.ID,
		ReplyToMessageID: message.ReplyToMessage.MessageID,
		Text:             message.Text,
	})

	return h.reply(ctx, message.Chat.ID, outcomeText(outcome))
}

// outcomeText renders an outcome of the bridge reply path as the sentence the
// operator reads.
//
// The mapping is one to one: every outcome has its own fixed sentence, and only
// a queued send names the recipient and the SMSGate message id, both of which the
// operator already knows. An outcome this adapter does not know about falls back
// to the generic lookup sentence, which says nothing about the failure behind
// it.
func outcomeText(outcome bridge.Outcome) string {
	switch outcome.Kind {
	case bridge.OutcomeEmptyText:
		return emptyTextText
	case bridge.OutcomeNotForwarded:
		return notForwardedText
	case bridge.OutcomeLookupFailed:
		return storeFailedText
	case bridge.OutcomeSendFailed:
		return sendFailedText
	case bridge.OutcomeSent:
		return fmt.Sprintf(ackFormat, outcome.Phone, outcome.MessageID)
	default:
		return storeFailedText
	}
}

// reply sends one plain text message back to the chat the update came from.
//
// It is the last step of every branch: the operator always gets an answer in
// chat, and a failure to send that answer is the only error this handler
// propagates.
func (h *Handler) reply(ctx *th.Context, chatID int64, text string) error {
	if _, err := ctx.Bot().SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID, Username: ""},
		Text:   text,
	}); err != nil {
		return fmt.Errorf("reply: send telegram message: %w", err)
	}

	return nil
}
