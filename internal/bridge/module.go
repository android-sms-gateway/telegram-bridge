package bridge

import (
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// Module creates and returns an FX module for the bridge package.
//
// The module owns both directions of the bridge on one service, so it is the
// only place where the forward target, the mapping repository and the SMSGate
// client meet. The service is provided publicly because two other modules
// consume it: internal/webhook forwards through it after an inbound SMS, and
// the bot reply handler answers a Telegram bubble through it.
//
// Authorization is not here: the routes of internal/bot filter updates by sender
// before a reply reaches this service, which is why the service needs no
// allowlist of its own.
//
// Nothing else is provided here, so no dependency is marked fx.Private: the bot,
// the repository and the SMS client are all provided by other modules
// (telegofx, store, smsgate) and are consumed by nothing outside this one.
func Module() fx.Option {
	return fx.Module(
		"bridge",
		logger.WithNamedLogger("bridge"),
		fx.Provide(New),
	)
}
