// Package config holds the settings the bot module owns.
//
// The type lives in this leaf package and not in package bot because the values
// are consumed by the handler packages: internal/bot/module.go imports those
// packages to collect their routes, so a handler that took the config in its
// constructor could not import package bot back without closing an import
// cycle. Nothing here is imported from anywhere else in the module, so every
// handler under internal/bot may take this config.
package config

// Config holds the settings the bot module owns.
//
// The values are mapped from the raw koanf config in internal/config/module.go
// and reach their consumer as this struct, never as a bare list: fx has no way
// to tell two slices of int64 apart.
//
// The section mirrors its single runtime owner: who may drive the routes the
// bot registers, which is a property of the bot itself and not of the Telegram
// client (telegofx.Config carries the token and the proxy) nor of the bridge
// (bridge.Config carries the forward target).
//
// The allowlist is applied at REGISTRATION, as a predicate on every route that
// acts on behalf of a Telegram user, so an unauthorized update never reaches a
// handler body or a service at all.
//
// Example:
//
//	cfg := config.Config{
//	    AllowedUserIDs: []int64{123456789},
//	}
//
//	allowed := predicates.NewAllowedUsers(cfg.AllowedUserIDs)
type Config struct {
	// AllowedUserIDs are the Telegram user ids whose updates the routes of this
	// bot accept. The check is on the Telegram user, not on the chat, so a bot
	// added to a group cannot be driven by every member of it.
	//
	// The value is read from the bot section (bot.allowed_user_ids,
	// BOT__ALLOWED_USER_IDS) because it authorizes a Telegram actor, and it is
	// enforced by the routes because those routes are what turn an update into
	// a call.
	//
	// An empty list is fail-closed: every filtered route matches nothing and a
	// warning is logged at startup by the bot module. There is no "allow
	// everyone" spelling, because a bridge that answers any chat it is a member
	// of would send SMS on request from strangers.
	AllowedUserIDs []int64
}
