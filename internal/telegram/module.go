package telegram

import (
	"net/http"

	"github.com/go-core-fx/httpfx"
	"github.com/go-core-fx/logger"
	"github.com/go-core-fx/telegofx"
	"go.uber.org/fx"
)

// Module wires the Telegram infra layer: the bot's HTTP client and the
// telegofx options that carry it.
func Module() fx.Option {
	return fx.Module(
		"telegram",
		logger.WithNamedLogger("telegram"),
		// The bot owns its HTTP client: it consumes the DEFAULT
		// httpfx.Factory from the graph (httpfx.Module, fed by the app-wide
		// httpfx.Config) and overrides only the bot's transport settings
		// per client from the bot's OWN Config (mapped by config.Module).
		// The proxy/timeout overrides below are unconditional, so the bot's
		// own Config always governs bot API traffic; the app-wide base
		// still contributes the settings not overridden here (idle pools,
		// TLS). fx.Private scopes *http.Client to this module, so it
		// OVERRIDES the app-wide default client for everything declared
		// here, and telegofx never takes a client from the graph directly.
		fx.Provide(
			func(factory httpfx.Factory, cfg Config) (*http.Client, error) {
				if err := cfg.Validate(); err != nil {
					return nil, err
				}

				return factory.NewClient(
					httpfx.WithProxyURL(cfg.ProxyURL, ""),
					httpfx.WithTimeout(cfg.Timeout),
				)
			},
			fx.Private,
		),
		// telegofx.New takes its options from the graph: WithHTTPClient passes
		// the bot's private client resolved above, keeping the client seam a
		// deliberate wiring decision. The zap logger is appended by telegofx
		// itself. This provider stays exported: the sibling telegofx module
		// consumes it.
		fx.Provide(func(client *http.Client) []telegofx.Option {
			return []telegofx.Option{telegofx.WithHTTPClient(client)}
		}),
	)
}
