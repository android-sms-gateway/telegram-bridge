package webhook

import (
	"fmt"

	appcache "github.com/android-sms-gateway/telegram-bridge/internal/cache"
	"github.com/go-core-fx/cachefx/cache"
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// eventCacheName namespaces the webhook event de-duplication cache inside the
// application cache factory, so webhook keys never collide with another
// module's cache.
const eventCacheName = "webhook-events"

// Module creates and returns an FX module for the webhook package.
//
// The service is provided publicly so other modules (the server module's
// webhook handler) can consume it; metrics and the event cache stay private to
// this module. The HTTP handler itself lives in
// internal/server/handlers/webhook and is registered by the server module.
func Module() fx.Option {
	return fx.Module(
		"webhook",
		logger.WithNamedLogger("webhook"),
		fx.Provide(NewMetrics, fx.Private),
		fx.Provide(
			func(factory appcache.Factory) (cache.Cache, error) {
				eventCache, err := factory.New(eventCacheName)
				if err != nil {
					return nil, fmt.Errorf("create event cache: %w", err)
				}

				return eventCache, nil
			},
			fx.Private,
		),
		fx.Provide(New),
	)
}
