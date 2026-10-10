package config

import (
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/fiberfx"
	"github.com/go-core-fx/fiberfx/openapi"
	"github.com/go-core-fx/telegofx"
	"go.uber.org/fx"
)

// Module provides the raw Config to the rest of the graph together with the
// config value each consumer takes. Library-owned settings are handed to the
// library config as-is; the bridge-domain settings are mapped to the module
// config their own module consumes.
//
//	fiberfx.Config                  <- http.*                        -> fiberfx
//	openapi.Config                  <- http.openapi.*                -> openapi
//	telegofx.Config                 <- telegram.token, telegram.proxy -> telegofx.New
//	bridge.Config                   <- bridge.chat_id                -> bridge.New
//	webhook.Config                  <- smsgate.webhook.signing_key   -> webhook.New
//
// No bare primitives (int64, string, ...) are provided: fx would then have no
// way to tell two values of the same type apart.
func Module() fx.Option {
	return fx.Module(
		"config",
		fx.Provide(New, fx.Private),
		fx.Provide(
			func(cfg Config) fiberfx.Config {
				return fiberfx.Config{
					Address:     cfg.HTTP.Address,
					ProxyHeader: cfg.HTTP.ProxyHeader,
					Proxies:     cfg.HTTP.Proxies,
				}
			},
			func(cfg Config) openapi.Config {
				return openapi.Config{
					Enabled:    cfg.HTTP.OpenAPI.Enabled,
					PublicHost: cfg.HTTP.OpenAPI.PublicHost,
					PublicPath: cfg.HTTP.OpenAPI.PublicPath,
				}
			},
			func(cfg Config) telegofx.Config {
				// The telegram section is telegofx infrastructure only: the token
				// authenticates the bot and the proxy is telegofx's own dialer.
				return telegofx.Config{
					Token: cfg.Telegram.Token,
					// telegofx owns the proxy dialer: a non-empty value must be a
					// socks5:// or socks5h:// URL, anything else fails bot creation.
					ProxyURL: cfg.Telegram.ProxyURL,
					// The bridge polls Telegram. ModeWebhook would additionally
					// require mounting telegofx.WebhookHandler with a
					// secret-token check, which is not wired yet.
					Mode: telegofx.ModePolling,
				}
			},
			func(cfg Config) bridge.Config {
				// The bridge section is owned by internal/bridge and holds only
				// the forward target.
				return bridge.Config{
					// Zero is "not configured": bridge.Service.Forward then fails
					// with bridge.ErrNoChatID instead of sending to an unknown chat.
					ChatID: cfg.Bridge.ChatID,
				}
			},
			func(cfg Config) webhook.Config {
				return webhook.Config{
					// The SMSGate webhook signing key must match the key configured on
					// the device (Settings -> Webhooks -> Signing Key). The webhook
					// service treats an empty key as fail-closed.
					SigningKey: cfg.SMSGate.Webhook.SigningKey,
				}
			},
		),
	)
}
