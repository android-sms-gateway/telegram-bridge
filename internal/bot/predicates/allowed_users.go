// Package predicates holds the update filters this bot tree owns.
//
// The telegofx predicates package describes the SHAPE of an update (is it a
// message, is it a reply to another message); the filters here answer a
// different question, WHO is allowed to talk to this bot at all. Keeping the
// two apart is what lets a route say both in one call: it hands one shape
// predicate and one authorization filter to the router, and telegohandler
// requires every one of them to match.
//
// The filters live here and not in package bot because package bot imports the
// handler packages to register their routes: a handler importing bot back for
// the filter would close a cycle. This package imports neither, so every
// handler may take the filter from it.
package predicates

import (
	"context"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// AllowedUsers matches the updates written by the Telegram users this bot
// answers.
//
// The type itself is the predicate: a route hands it to the router next to its
// own shape predicates and telegohandler applies them all with AND, so an
// unauthorized update is filtered out of the route instead of being refused
// inside a handler body. Only the type crosses the fx graph, never the id list
// and never a bare func value.
type AllowedUsers struct {
	// ids is the allowlist as a set: an update is matched once per delivery, and
	// a map makes that independent of the order and of any duplicate in the
	// configured list. The zero value is an empty set, so it matches nothing.
	ids map[int64]struct{}
}

// NewAllowedUsers builds the matcher from the configured Telegram user ids.
//
// An empty or nil list matches nothing: the routes that take it stay closed
// until an operator names the users allowed to drive them. There is no "allow
// everyone" spelling on purpose, because a bot that answers any chat it is a
// member of would send SMS on request from strangers.
func NewAllowedUsers(userIDs []int64) AllowedUsers {
	ids := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		ids[userID] = struct{}{}
	}

	return AllowedUsers{ids: ids}
}

// Match reports whether the update is a message written by an allowed user.
//
// The check is on the sender, not on the chat, so adding the bot to a group
// does not hand the bridge to every member of it. An update that carries no
// message or no sender matches nothing: there is no actor to authorize, and it
// is the shape predicates of the route, not this filter, that decide whether
// such an update is addressed to this bot at all.
//
// The update is read, never modified: a filter that rewrote it would silently
// change what the next route in the chain sees.
func (a AllowedUsers) Match(_ context.Context, update telego.Update) bool {
	if update.Message == nil || update.Message.From == nil {
		return false
	}

	_, ok := a.ids[update.Message.From.ID]

	return ok
}

// The router takes predicates by value of the func type, so what the
// constructor returns has to satisfy th.Predicate rather than being called by
// hand at every route.
var _ th.Predicate = NewAllowedUsers(nil).Match
