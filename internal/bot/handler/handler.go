package handler

import (
	"github.com/go-core-fx/telegofx"
)

// Handler registers bot routes in the shared telegofx router.
type Handler interface {
	// Register binds the routes of this handler on the shared router.
	//
	// A handler owns the whole of its own route, including the predicates that
	// decide which update reaches it: the authorization filter is built from
	// the module config it takes in its constructor and handed to the router
	// here, so an update from an unauthorized user is filtered out of the route
	// instead of being refused inside a handler body.
	Register(router *telegofx.Router)
}
