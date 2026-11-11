package cache

import (
	"github.com/go-core-fx/cachefx"
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// Factory is the application cache factory: cachefx's factory scoped to the
// "telegram-bridge" prefix.
//
// Caches created through it are swept by the cachefx sweeper, because
// WithName shares the sweeper's tracker with the root factory.
type Factory interface {
	cachefx.Factory
}

// Module provides the application cache factory. Consumers create the caches
// they need with Factory.New, so every module owns its own cache namespace
// instead of sharing a single unnamed instance.
func Module() fx.Option {
	return fx.Module(
		"cache",
		logger.WithNamedLogger("cache"),
		fx.Provide(func(factory cachefx.Factory) Factory {
			return factory.WithName("telegram-bridge")
		}),
	)
}
