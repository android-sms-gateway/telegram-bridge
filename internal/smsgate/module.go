package smsgate

import (
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// Module creates and returns an FX module for the smsgate package.
//
// The service is provided publicly so the reply handler can consume it. The
// client is built once at startup from the smsgate config section; it holds no
// background work of its own.
func Module() fx.Option {
	return fx.Module(
		"smsgate",
		logger.WithNamedLogger("smsgate"),
		fx.Provide(New),
	)
}
