package store

import (
	"github.com/go-core-fx/fxutil"
	"github.com/go-core-fx/logger"
	"go.uber.org/fx"
)

// Module creates and returns an FX module for the store package.
//
// The database itself is not opened here: pkg/boltfx opens it from the raw
// storage.url setting and closes it when the application stops. This module owns
// the bucket schema, the mapping TTL and the cleanup loop on top of it.
//
// The repository is provided publicly, because the reply path resolves the
// sender of an answered message through it, while the cleanup service stays
// private to this module.
//
// Usage:
//
//	app := fx.New(
//	    boltfx.Module(),
//	    store.Module(true),
//	)
func Module(withRun bool) fx.Option {
	opts := []fx.Option{
		logger.WithNamedLogger("store"),

		fx.Provide(NewRepository),
		fx.Provide(New, fx.Private),
	}

	// Register the cleanup loop as a runnable only when background work is
	// needed: one-shot commands pass false to avoid starting the loop.
	if withRun {
		opts = append(opts, fx.Invoke(fxutil.RegisterRunnable[*Service]()))
	}

	return fx.Module("store", opts...)
}
