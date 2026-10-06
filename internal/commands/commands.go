// Package commands defines CLI commands for the application.
//
// Each command lives in its own sub-package and exposes a Command constructor
// that returns a [*cli.Command]. The commands package aggregates them for the
// root CLI shell in internal/app.go.
package commands

import (
	"github.com/capcom6/go-project-template/internal/commands/example"
	"github.com/capcom6/go-project-template/internal/commands/serve"
	"github.com/go-core-fx/healthfx"
	"github.com/urfave/cli/v3"
)

// Commands returns all available CLI commands.
func Commands(version healthfx.Version) []*cli.Command {
	return []*cli.Command{
		serve.Command(version),
		example.Command(version),
	}
}
