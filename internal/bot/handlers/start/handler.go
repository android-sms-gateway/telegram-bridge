package start

import (
	"fmt"

	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handler"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	"go.uber.org/zap"
)

// startText describes the bridge when a user interacts with the bot.
const startText = "Telegram bridge for SMSGate is connected. " +
	"Messages received from your SMSGate webhooks will be forwarded to this chat. " +
	"Reply to a forwarded message and your answer is sent back as an SMS to the original sender."

// Handler answers the /start command.
type Handler struct {
	logger *zap.Logger
}

// The bot module collects handlers through this interface.
var _ handler.Handler = (*Handler)(nil)

func New(logger *zap.Logger) handler.Handler {
	return &Handler{
		logger: logger,
	}
}

// Register registers the /start route on the shared router.
//
// The route takes no allowed-users filter on purpose: /start only describes the
// bot and drives nothing, so it stays open to every user. A stranger who types
// /start learns that the bridge exists and nothing else, while the routes that
// send SMS or deliver messages are still closed to them.
func (h *Handler) Register(router *telegofx.Router) {
	router.Handle(
		h.handleStart,
		th.CommandEqual("start"),
		th.AnyMessageWithFrom(),
	)
}

func (h *Handler) handleStart(ctx *th.Context, update telego.Update) error {
	if update.Message == nil || update.Message.From == nil {
		h.logger.Warn("received /start update without message sender")
		return nil
	}

	return h.reply(ctx, update.Message.Chat.ID, startText)
}

func (h *Handler) reply(ctx *th.Context, chatID int64, text string) error {
	_, err := ctx.Bot().SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID, Username: ""},
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}

	return nil
}
