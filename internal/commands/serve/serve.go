package serve

import (
	"context"
	"fmt"

	"github.com/android-sms-gateway/telegram-bridge/internal/bot"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/config"
	"github.com/android-sms-gateway/telegram-bridge/internal/smsgate"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"

	// "github.com/android-sms-gateway/telegram-bridge/internal/db".
	"github.com/android-sms-gateway/telegram-bridge/internal/server"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/android-sms-gateway/telegram-bridge/pkg/boltfx"
	"github.com/go-core-fx/fiberfx"
	"github.com/go-core-fx/healthfx"
	"github.com/go-core-fx/logger"
	"github.com/go-core-fx/telegofx"
	"github.com/go-core-fx/validatorfx"
	"github.com/urfave/cli/v3"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Command returns the serve command that starts the full application.
func Command(version healthfx.Version) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Start the HTTP server, Telegram bot, and all services",
		Action: func(ctx context.Context, _ *cli.Command) error {
			return run(ctx, version)
		},
	}
}

func run(ctx context.Context, version healthfx.Version) error {
	app := fx.New(
		// CORE MODULES
		logger.Module(),
		logger.WithFxDefaultLogger(),
		// badgerfx.Module(),
		// bunfx.Module(),
		// cachefx.Module(),
		boltfx.Module(),
		fiberfx.Module(),
		// gocqlfx.Module(),
		// gocqlxfx.Module(),
		// goosefx.Module(),
		// gormfx.Module(),
		healthfx.Module(),
		// httpfx.Module(),
		// openaifx.Module(),
		// openrouterfx.Module(),
		// redisfx.Module(),
		// sqlfx.Module(),
		// sqlxfx.Module(),
		telegofx.Module(telegofx.WithRouter()),
		validatorfx.Module(),
		// watermillfx.Module(),
		//
		// APP MODULES
		config.Module(),
		// db.Module(),
		server.Module(),
		bot.Module(),
		store.Module(true),
		//
		// BUSINESS MODULES
		fx.Supply(version),
		bridge.Module(),
		webhook.Module(),
		smsgate.Module(),

		fx.Invoke(func(lc fx.Lifecycle, logger *zap.Logger) {
			lc.Append(fx.Hook{
				OnStart: func(_ context.Context) error {
					logger.Info("app started")
					return nil
				},
				OnStop: func(_ context.Context) error {
					logger.Info("app stopped")
					return nil
				},
			})
		}),
	)

	startCtx, cancelStart := context.WithTimeout(ctx, app.StartTimeout())
	defer cancelStart()

	if err := app.Start(startCtx); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	select {
	case <-ctx.Done():
	case <-app.Done():
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), app.StopTimeout())
	defer cancelStop()

	if err := app.Stop(stopCtx); err != nil {
		return fmt.Errorf("failed to stop app: %w", err)
	}

	return nil
}
