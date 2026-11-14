package bot

import (
	botconfig "github.com/android-sms-gateway/telegram-bridge/internal/bot/config"
	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handler"
	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handlers/reply"
	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handlers/start"
	"github.com/go-core-fx/logger"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// warnEmptyAllowlist is the startup report of the bot module: an empty
// BOT__ALLOWED_USER_IDS fails closed and filters every reply out of the route,
// so an operator who never configured it would otherwise see a bot that accepts
// /start and silently drops every answer to a forwarded bubble. The warning
// names the environment key so the log line points at the fix.
func warnEmptyAllowlist(cfg botconfig.Config, log *zap.Logger) {
	if len(cfg.AllowedUserIDs) == 0 {
		log.Warn("BOT__ALLOWED_USER_IDS is empty: every reply is filtered out of the route, no SMS can be sent")
	}
}

// Module creates and returns an FX module for the bot package.
func Module() fx.Option {
	return fx.Module(
		"bot",
		logger.WithNamedLogger("bot"),
		fx.Provide(func() []telego.BotOption {
			return []telego.BotOption{}
		}),
		fx.Provide(
			fx.Annotate(start.New, fx.ResultTags(`group:"handlers"`)),
			fx.Annotate(reply.New, fx.ResultTags(`group:"handlers"`)),
		),
		fx.Invoke(
			fx.Annotate(
				func(handlers []handler.Handler, r *telegofx.Router) {
					for _, h := range handlers {
						h.Register(r)
					}
				},
				fx.ParamTags(`group:"handlers"`),
			),
		),
		fx.Invoke(warnEmptyAllowlist),
	)
}
