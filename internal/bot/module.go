package bot

import (
	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handler"
	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handlers/start"
	"github.com/go-core-fx/logger"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/fx"
)

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
	)
}
