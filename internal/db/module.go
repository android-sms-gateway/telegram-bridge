package db

import (
	"github.com/capcom6/go-project-template/internal/db/migrations"
	"github.com/go-core-fx/goosefx"
	"github.com/go-core-fx/logger"
	"github.com/pressly/goose/v3/database"
	"github.com/uptrace/bun/dialect/mysqldialect"
	"github.com/uptrace/bun/schema"
	"go.uber.org/fx"

	_ "github.com/go-sql-driver/mysql" // required
)

func Module() fx.Option {
	return fx.Module(
		"db",
		logger.WithNamedLogger("db"),
		fx.Provide(func() database.Dialect {
			return database.DialectMySQL
		}),
		fx.Provide(func() schema.Dialect {
			return mysqldialect.New()
		}),
		fx.Provide(func() goosefx.Storage {
			return goosefx.Storage(migrations.FS)
		}),
	)
}
