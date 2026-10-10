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
//
// Telegram connectivity, including proxying, is owned by telegofx: the raw
// TELEGRAM__TOKEN and TELEGRAM__PROXY_URL values are mapped into telegofx.Config
// by the config module. telegofx then builds the matching SOCKS5 dialer itself.
func Module() fx.Option {
	return fx.Module(
		"bot",
		logger.WithNamedLogger("bot"),
		fx.Provide(newBotOptions),
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

// newBotOptions supplies the extension point for extra telego bot options.
// telegofx appends its own proxy option after these, so a configured
// TELEGRAM__PROXY_URL always wins.
func newBotOptions() []telego.BotOption {
	return []telego.BotOption{}
}
