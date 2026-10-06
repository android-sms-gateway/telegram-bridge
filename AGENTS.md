# go-project-template — AGENTS.md

## Commands
- `make deps` — go mod download
- `make gen` — go generate ./... (swag init)
- `make fmt` — golangci-lint fmt (goimports, golines 120, swaggo formatter)
- `make lint` — golangci-lint run --timeout=5m (very strict config)
- `make test` — go test -race -shuffle=on -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
- `make coverage` — test + go tool cover text + HTML
- `make build` — binary to bin/$(BINARY_NAME)
- `make air` — live reload (requires air, sets TZ=UTC DEBUG=1)
- `make swagger` — standalone swag fmt + swag init

## Architecture
- **Entrypoint**: main.go — swag //go:generate directive; version injected via ldflags (appVersion, appBuildDate, appReleaseID)
- **CLI**: urfave/cli/v3 in internal/app.go — CLI shell with DefaultCommand "serve"; commands in internal/commands/{serve,example}/
- **DI**: Fx graphs live in each command (internal/commands/serve/serve.go, internal/commands/example/example.go)
- **Config**: go-core-fx/config — env vars + optional YAML via CONFIG_PATH env var
- **HTTP**: Fiber at 127.0.0.1:3000, routes under /api/v1, validation middleware at group level
- **Bot**: telego + telegofx (Telegram), proxy set via fasthttpproxy.FasthttpProxyHTTPDialer()
- **DB**: MySQL/MariaDB via Bun (mysqldialect), Goose migrations in internal/db/migrations/ (//go:embed *.sql)
- **Metrics**: Prometheus via fiberfx (auto), per-module counters via promauto

## Module Conventions
- Each package exposes a Module(...) fx.Option (withRun bool for modules with background work)
- Handlers registered via group tags: Provide(..., fx.ResultTags(`group:"handlers"`))
- Internal-only deps use fx.Private
- Services with Run(ctx) use `fxutil.RegisterRunnable[*T]()` — conditionally via `withRun` bool
- Per-module named logger: logger.WithNamedLogger("name")
- Config module maps raw Config struct to sub-configs for fiberfx, telegofx, sqlfx, openapi, example

## Code Generation
- Swagger docs: go generate ./... (swag init --parseDependency --outputTypes go -g ./main.go -o ./internal/server/docs)
- Output: internal/server/docs/docs.go — DO NOT EDIT
- Live reload config: .air.toml

## Linting & CI
- golangci-lint v2, ~70 linters (exhaustruct, cyclop, gochecknoglobals, etc.)
- golines max length: 120
- Format + lint + test + coverage in that order
- CI: lint + test on push/PR to master in .github/workflows/go.yml
- CI: goreleaser snapshot on PR; release on v* tags
- E2E CI job is disabled (if: false)
- Stale issues/PRs closed after 14d inactivity

## Testing
- No tests exist yet — add _test.go as needed
- Test flags: -race -shuffle=on -count=1 (disables cache, randomizes order)

## Build & Release
- GoReleaser v2 for linux/windows/darwin, CGO_ENABLED=0
- Go 1.25+
- Docker images pushed to ghcr.io on PR and release
