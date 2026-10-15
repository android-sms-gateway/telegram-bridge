package webhook

import (
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// Module creates and returns an FX module for the webhook package.
//
// The service is provided publicly so other modules (the server module's
// webhook handler) can consume it; metrics stay private to this module. The
// HTTP handler itself lives in internal/server/handlers/webhook and is
// registered by the server module.
func Module() fx.Option {
	return fx.Module(
		"webhook",
		logger.WithNamedLogger("webhook"),
		fx.Provide(NewMetrics, fx.Private),
		fx.Provide(New),
	)
}
