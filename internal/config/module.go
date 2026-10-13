package config

import (
	botconfig "github.com/android-sms-gateway/telegram-bridge/internal/bot/config"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/smsgate"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/android-sms-gateway/telegram-bridge/pkg/boltfx"
	"github.com/go-core-fx/fiberfx"
	"github.com/go-core-fx/fiberfx/openapi"
	"github.com/go-core-fx/telegofx"
	"go.uber.org/fx"
)

// Module provides the raw Config to the rest of the graph together with the
// config value each consumer takes. Library-owned settings are handed to the
// library config as-is; the module-owned settings are mapped to the config
// their own module consumes.
//
//	fiberfx.Config                  <- http.*                         -> fiberfx
//	openapi.Config                  <- http.openapi.*                 -> openapi
//	telegofx.Config                 <- telegram.token, telegram.proxy -> telegofx.New
//	botconfig.Config                <- bot.allowed_user_ids           -> bot.Module, reply.New
//	bridge.Config                   <- bridge.chat_id                 -> bridge.New (internal/bridge)
//	webhook.Config                  <- smsgate.webhook.signing_key    -> webhook.New
//	smsgate.Config                  <- smsgate.base_url/username/password -> smsgate.New
//	boltfx.Config                   <- storage.url                    -> boltfx.New
//	store.Config                    <- (store default)                -> store.NewRepository
//
// No bare primitives (int64, string, ...) are provided: fx would then have no
// way to tell two values of the same type apart. The bot config is aliased here
// because this package and internal/bot/config are both named config.
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
					ProxyURL: cfg.Telegram.Proxy,
					// The bridge polls Telegram. ModeWebhook would additionally
					// require mounting telegofx.WebhookHandler with a
					// secret-token check, which is not wired yet.
					Mode: telegofx.ModePolling,
				}
			},
			func(cfg Config) botconfig.Config {
				// The bot section is owned by internal/bot/config: the allowlist
				// decides which Telegram users the routes of that module accept,
				// and each handler enforces it as a predicate while it registers
				// its routes. Empty is fail-closed there, and the bot module
				// warns about it.
				return botconfig.Config{
					AllowedUserIDs: cfg.Bot.AllowedUserIDs,
				}
			},
			func(cfg Config) bridge.Config {
				// The bridge section is the bridge-domain surface: where a
				// forwarded message is delivered. The reply path resolves a
				// sender through internal/store and sends through
				// internal/smsgate, so the service needs no setting of its own
				// beyond that target.
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
			func(cfg Config) smsgate.Config {
				// The smsgate section addresses the SMSGate server API used by the
				// reply path: basic auth against it, never against the device.
				return smsgate.Config{
					BaseURL:  cfg.SMSGate.BaseURL,
					Username: cfg.SMSGate.Username,
					Password: cfg.SMSGate.Password,
					// The per-attempt timeout stays the module default: it is not
					// operator-tunable yet, and a raw key that mapped to it would
					// put a networking detail in the operator-facing surface.
					Timeout: smsgate.DefaultTimeout,
				}
			},
			func(cfg Config) boltfx.Config {
				// The storage section is the embedded database of the bridge, and
				// pkg/boltfx owns how it is opened. Its parent directory is not
				// created: the file has to be reachable before the service starts.
				return boltfx.Config{
					URL: cfg.Storage.URL,
				}
			},
			func(_ Config) store.Config {
				// The mapping TTL belongs to the store and stays on its own default:
				// it is not operator-tunable yet, and adding a raw storage.ttl key
				// would not change anything in this module.
				return store.Config{
					TTL: store.DefaultTTL,
				}
			},
		),
	)
}
