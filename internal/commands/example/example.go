package example

import (
	"context"
	"fmt"
	"os"

	"github.com/capcom6/go-project-template/internal/config"
	"github.com/capcom6/go-project-template/internal/example"
	"github.com/go-core-fx/healthfx"
	"github.com/go-core-fx/logger"
	"github.com/urfave/cli/v3"
	"go.uber.org/fx"
)

// Command returns a one-shot command that demonstrates composing a minimal Fx graph.
//
// It wires only the modules needed for this command (logger, config, example),
// prints the configured example value, and exits. This pattern is useful for
// CLI tools, migrations, data sync, admin tasks, etc.
func Command(_ healthfx.Version) *cli.Command {
	return &cli.Command{
		Name:  "example",
		Usage: "Run an example one-shot task demonstrating Fx DI",
		Action: func(ctx context.Context, _ *cli.Command) error {
			return run(ctx)
		},
	}
}

func run(ctx context.Context) error {
	var svc *example.Service

	app := fx.New(
		// CORE MODULES
		logger.Module(),
		logger.WithFxDefaultLogger(),
		// badgerfx.Module(),
		// bunfx.Module(),
		// cachefx.Module(),
		// fiberfx.Module(),
		// gocqlfx.Module(),
		// gocqlxfx.Module(),
		// sqlfx.Module(),
		// goosefx.Module(),
		// gormfx.Module(),
		// healthfx.Module(),
		// openrouterfx.Module(),
		// redisfx.Module(),
		// sqlxfx.Module(),
		// telegofx.Module(true),
		// validatorfx.Module(),
		// watermillfx.Module(),
		//
		// APP MODULES
		config.Module(),
		//
		// BUSINESS MODULES
		example.Module(false),
		fx.Populate(&svc),
	)

	startCtx, cancelStart := context.WithTimeout(ctx, app.StartTimeout())
	defer cancelStart()

	if err := app.Start(startCtx); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	defer func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), app.StopTimeout())
		defer cancelStop()
		if err := app.Stop(stopCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to stop app: %v\n", err)
		}
	}()

	fmt.Fprintf(os.Stdout, "example value: %s\n", svc.Example())

	return nil
}
