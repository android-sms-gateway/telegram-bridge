package boltfx

import (
	"context"
	"fmt"

	"github.com/go-core-fx/logger"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Module provides an open *bbolt.DB to the rest of the graph.
//
// The database is opened from a [Config] supplied elsewhere in the graph and
// closed by a lifecycle hook when the application stops, so consumers must not
// hold the handle past shutdown.
func Module() fx.Option {
	return fx.Module(
		"boltfx",
		logger.WithNamedLogger("boltfx"),
		fx.Provide(New),
		fx.Invoke(registerLifecycle),
	)
}

// registerLifecycle appends the hook that closes the database on stop.
func registerLifecycle(lc fx.Lifecycle, db *bolt.DB, logger *zap.Logger) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			logger.Info("database started", zap.String("path", db.Path()))

			return nil
		},
		OnStop: func(_ context.Context) error {
			logger.Info("shutting down database", zap.String("path", db.Path()))

			if err := db.Close(); err != nil {
				return fmt.Errorf("close database: %w", err)
			}

			logger.Info("database shutdown completed")

			return nil
		},
	})
}
