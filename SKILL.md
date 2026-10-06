# Scaffolding a New Go Service from This Template

## Initial Setup

Two paths:

**GitHub template** — click "Use this template" on the repo page, clone your new repo, `cd` in.

**Manual** — clone and re-init:
```
git clone git@github.com:capcom6/go-project-template.git <your-project>
cd <your-project>
rm -rf .git
git init
git add .
git commit -m "init from go-project-template"
```

## Replace Placeholder Values

Search-and-replace table:

| Find | Replace with |
|---|---|
| `github.com/capcom6/go-project-template` | `your/module/path` (in `go.mod`, all `.go` imports) |
| binary name = dir name | used in `Makefile` `BINARY_NAME`, `.goreleaser.yaml` build |

**`main.go`** — update swagger annotations and contact info:
- `@title`, `@description`, `@contact.*`, `@host`, `@BasePath`
- version globals stay; they get real values via goreleaser ldflags

**`.goreleaser.yaml`** — update:
- `DOCKER_REGISTRY` env / labels `source`, `name`

**`internal/config/config.go`** — update defaults:
- `Default()` → `HTTP.Address`, `Database.URL`, `Example.Example`

**`internal/example/metrics.go`** — change `metricsNamespace` from `"template"` to your name.

## Remove (or Adapt) the Example Module

Delete the `internal/example/` directory, then remove its wiring:
1. `internal/commands/serve/serve.go` — remove `example.Module(true)`
2. `internal/commands/example/example.go` — delete entire file
3. `internal/commands/commands.go` — remove `example.Command(version)` from the list
4. `internal/config/module.go` — remove `example.Config` mapping
5. `internal/server/module.go` — remove example handler provide + import
6. `internal/server/docs/docs.go` — regenerate after deleting handler (`make gen`)

## Module Scaffold

When adding a new business domain, create:

```
internal/<name>/
├── config.go       # Config struct
├── domain.go       # Domain entities
├── errors.go       # Sentinel errors (Err*)
├── models.go       # Internal data models
├── metrics.go      # Prometheus counters via promauto
├── repository.go   # Data access layer
├── service.go      # Service with Run(ctx) error
└── module.go       # fx.Module()
```

**`module.go`** template:

```go
package <name>

import (
    "github.com/go-core-fx/fxutil"
    "github.com/go-core-fx/logger"
    "go.uber.org/fx"
)

func Module(withRun bool) fx.Option {
    opts := []fx.Option{
        logger.WithNamedLogger("<name>"),
        fx.Provide(NewMetrics, fx.Private),
        fx.Provide(NewRepository, fx.Private),
        fx.Provide(New),
    }
    if withRun {
        opts = append(opts, fx.Invoke(fxutil.RegisterRunnable[*Service]()))
    }
    return fx.Module("<name>", opts...)
}
```

**`service.go`** — constructor signature:
```go
func New(config Config, examples *Repository, metrics *Metrics, logger *zap.Logger) *Service
```
Implement `Run(ctx context.Context) error` for lifecycle management.

**`metrics.go`** — use `promauto.NewCounter` with `Namespace`/`Subsystem` fields.

### Adding HTTP Handlers

```
internal/server/handlers/<name>/
├── dto.go        # Request/Response with `validate:"required"` tags
└── handler.go    # Embeds handler.Base, tagged for group injection
```

Handler provide in `server.Module()`:
```go
fx.Provide(
    fx.Annotate(<name>.New, fx.ResultTags(`group:"handlers"`)),
    fx.Private,
)
```

Handler constructor must return `handler.Handler` — routes register via `h.Register(v1)` which is called automatically.

### Adding Telegram Bot Handlers

```
internal/bot/handlers/<name>/handler.go
```

Implement the `bot/handler.Handler` interface:
```go
func Register(router *telegofx.Router)
```

Add to `bot.Module()`:
```go
fx.Provide(
    fx.Annotate(<name>.New, fx.ResultTags(`group:"handlers"`)),
)
```

### Wiring Checklist

After creating files, do all three:

1. Add `<name>.Module(true)` to `internal/commands/serve/serve.go`
2. Map `Config` → `<name>.Config` in `internal/config/module.go`
3. Add handler provide (with `ResultTags`) to `server.Module()` or `bot.Module()`

## Local Development

```
make deps
make air       # live reload, requires `air` CLI: go install github.com/air-verse/air@latest
```

CLI usage:
```
make build && ./bin/go-project-template              # default = serve
./bin/go-project-template serve                      # explicit serve
./bin/go-project-template example                    # one-shot demo
./bin/go-project-template --help                     # list commands
```

MySQL/MariaDB required for DB features. Quick start:
```
docker run -d --name mariadb \
  -e MARIADB_USER=example -e MARIADB_PASSWORD=example \
  -e MARIADB_DATABASE=example -e MARIADB_ROOT_PASSWORD=root \
  -p 3306:3306 mariadb:11
```

Config via env vars or YAML:
```
export CONFIG_PATH=./config.local.yaml
make air
```

Telegram bot token required for bot features. Set via `telegram.token` in config.

## Common Gotchas

- **`exhaustruct`** — every struct literal must initialize all fields; use named field syntax, never zero-value shorthand.
- **`gochecknoglobals`** — bans package-level `var`/`const` except build metadata (annotate with `//nolint:gochecknoglobals`).
- **`sloglint` `no-global: all`** — `slog.SetDefault()`, `slog.Info()`, etc. are forbidden; use injected `*zap.Logger`.
- **Swagger regeneration** — `make fmt` calls `gen` which runs `swag init`. Output at `internal/server/docs/docs.go` is auto-generated and **DO NOT EDIT**.
- **Config mapping** — new feature configs must be added in `config/module.go` which maps the raw `Config` struct to per-module sub-configs.
- **`-count=1`** in test command disables Go test cache. Tests always run fresh with randomized order (`-shuffle=on`).

## Build & Deploy

```
make build              # binary to bin/<dirname>
make release            # goreleaser snapshot (local)
git tag v0.1.0 && git push origin v0.1.0   # triggers CI release + Docker push to ghcr.io
```

GoReleaser builds linux/windows/darwin with `CGO_ENABLED=0`. Docker images pushed to `ghcr.io` on PR (snapshot) and version tags (release).
