package bridge

import (
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// Module creates and returns an FX module for the bridge package.
//
// The service is provided publicly so other modules (webhook) can consume
// it; the bot instance itself comes from telegofx.
func Module() fx.Option {
	return fx.Module(
		"bridge",
		logger.WithNamedLogger("bridge"),
		fx.Provide(New),
	)
}
