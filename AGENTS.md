# telegram-bridge — AGENTS.md

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
- **CLI**: urfave/cli/v3 in internal/app.go — CLI shell with DefaultCommand "serve"; commands in internal/commands/serve/
- **DI**: Fx graphs live in each command (internal/commands/serve/serve.go)
- **Config**: go-core-fx/config — env vars + optional YAML via CONFIG_PATH env var
- **HTTP**: Fiber at 127.0.0.1:3000, routes under /api/v1, validation middleware at group level
- **Bot**: telego + telegofx (Telegram); proxy via `telegofx.Config.ProxyURL` (socks5:// / socks5h:// only)
- **Config mapping**: raw koanf sections in internal/config/config.go mirror the runtime owner of each setting and are mapped in internal/config/module.go to the config that consumer takes - `telegram.token`/`telegram.proxy` (`TELEGRAM__TOKEN`, `TELEGRAM__PROXY_URL`) -> `telegofx.Config` (library-owned), `bridge.chat_id` (`BRIDGE__CHAT_ID`) -> `bridge.Config` (internal/bridge), `smsgate.webhook.signing_key` -> `webhook.Config`. The forward target moved out of the telegram section in R18; the old `TELEGRAM__CHAT_ID` is inert. Never provide bare primitives (int64, string, ...) through DI; even a single-field setting gets a module config struct the module consumes directly.
- **DB**: none — the MySQL/Bun/Goose stack was dropped together with the internal/db package; `storage.url` is declared for a later wave and is not consumed yet
- **Metrics**: Prometheus via fiberfx (auto), per-module counters via promauto

## Module Conventions
- Each package exposes a Module(...) fx.Option (withRun bool for modules with background work)
- Handlers registered via group tags: Provide(..., fx.ResultTags(`group:"handlers"`))
- Internal-only deps use fx.Private
- Services with Run(ctx) use `fxutil.RegisterRunnable[*T]()` — conditionally via `withRun` bool
- Per-module named logger: logger.WithNamedLogger("name")
- Config module maps the raw Config struct to the value each consumer takes (fiberfx.Config, openapi.Config, telegofx.Config, bridge.Config, webhook.Config)

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
