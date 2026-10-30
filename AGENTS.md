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
- **CLI**: urfave/cli/v3 in internal/app.go — CLI shell with DefaultCommand "serve"; commands in internal/commands/{serve,example}/
- **DI**: Fx graphs live in each command (internal/commands/serve/serve.go, internal/commands/example/example.go)
- **Config**: go-core-fx/config — env vars + optional YAML via CONFIG_PATH env var
- **HTTP**: Fiber at 127.0.0.1:3000, routes under /api/v1, validation middleware at group level
- **Bot**: telego + telegofx (Telegram); proxy via `telegofx.Config.ProxyURL` (socks5:// / socks5h:// only); routes register telegohandler predicates and the first matching route wins. UPDATE SHAPE comes from `github.com/go-core-fx/telegofx/predicates` (`MessageWith*` family); the reply route is `predicates.MessageWithReplyToMessage()`, so the handler no longer re-checks the update shape. WHO MAY DRIVE THE BOT comes from the local `internal/bot/predicates` package (recreated in R24): `predicates.AllowedUsers`, a struct built from the allowlist whose `Match` method is itself a `th.Predicate`. It cannot live in package `bot` - package `bot` imports the handler packages, so a handler importing `bot` for the filter would close a cycle - and the update-shape predicates were already contributed upstream and must not be duplicated. For the same reason the settings that build the filter live in the leaf package `internal/bot/config` (`config.Config{AllowedUserIDs []int64}`, aliased `botconfig` in `internal/config`), not in package `bot`. The filter is built where it is enforced: `reply.New(service, cfg config.Config)` stores `predicates.NewAllowedUsers(cfg.AllowedUserIDs)` and `Register(router *telegofx.Router)` registers `th.And(predicates.MessageWithReplyToMessage(), h.allowed.Match)`. `internal/bot/module.go` keeps only the startup report (`warnEmptyAllowlist`), which warns once when the list is empty; /start takes no filter (it drives nothing) and answers every user.
- **Go workspaces (local only, never committed)**: `go.work` in the repo root uses `.` and `../../go-core-fx/telegofx`; a second one at `/Users/capcom/go-core-fx/go.work` lists every sibling library clone there that owns a `go.mod` (that directory is not a git repo). Both are gitignored (`go.work`, `go.work.sum`, `go.work*`), because CI checks out telegram-bridge alone and a committed `go.work` pointing outside the repo breaks every build. Verify with `go env GOWORK` and `go list -m -f '{{.Dir}}' github.com/go-core-fx/telegofx`.
- **CI stays red until the upstream tag lands**: `go.mod` still pins `github.com/go-core-fx/telegofx v0.4.0` on purpose — `go mod tidy` cannot see a workspace and would churn go.mod for nothing. v0.4.0 predates the predicate, so `GOWORK=off go build ./...` fails with `undefined: predicates.MessageWithReplyToMessage`; that failure is the expected tradeoff of developing the predicate upstream. Go green upstream: predicate + tests in the telegofx clone, telegofx v0.5.0 tagged, then the one `go.mod` line bumped. See `docs/plan/20261006-telegram-bridge/logs/wave2-go-work-telegofx.md`.
- **Config mapping**: raw koanf sections in internal/config/config.go mirror the runtime owner of each setting and are mapped in internal/config/module.go to the config that consumer takes - `telegram.token`/`telegram.proxy` (`TELEGRAM__TOKEN`, `TELEGRAM__PROXY`) -> `telegofx.Config` (library-owned), `bot.allowed_user_ids` (`BOT__ALLOWED_USER_IDS`) -> `internal/bot/config.Config.AllowedUserIDs` (taken by `reply.New`, enforced as a registration-time predicate; the allowlist moved out of the telegram section in R24 and the old `TELEGRAM__ALLOWED_USER_IDS` is inert, exactly like `TELEGRAM__CHAT_ID`), `bridge.chat_id` (`BRIDGE__CHAT_ID`) -> `bridge.Config.ChatID` (internal/bridge), `smsgate.webhook.signing_key` -> `webhook.Config`, `storage.url` (`STORAGE__URL`) -> `boltfx.Config` (pkg/boltfx, opens the file for internal/store; the parent directory must exist before startup). The forward target moved out of the telegram section in R18. Never provide bare primitives (int64, string, ...) through DI; even a single-field setting gets a module config struct the module consumes directly, and the allowlist crosses the graph as the `internal/bot/config.Config` struct, never as a bare slice or a func value.
- **Store**: internal/store (bbolt, `store.Module(true)`) maps `chatID:telegramMessageID` -> `{phone, created_at, expires_at}` in the `mappings` bucket plus a single `offsets` key; TTL 7 days (`store.DefaultTTL`), expiry enforced on read and by a 15m cleanup runnable.
- **Bridge**: internal/bridge (`bridge.Module()`) is the bidirectional bridge and owns both directions on ONE service: `Forward(ctx, phone, message) (Forwarded, error)` renders the SMS as HTML and sends it through the telegofx bot, and `Reply(ctx, Answer) Outcome` takes a Telegram answer back to the SMS sender (empty text -> `store.Resolve` -> `smsgate.Send`, each fail-closed) and returns a typed `Outcome`, never text. The reply half came from the deleted internal/reply module in R21; the method is `Reply`, not `Send`, because the service also holds the SMSGate client it delegates to. It owns ONE config, `bridge.Config`, with a single setting on it: `ChatID` (forward target, raw `bridge.chat_id`). Since R24 the service holds NO allowlist and no `allowed` set: authorization is not a bridge concern, the bot routes filter unauthorized updates out before they get here, so `OutcomeDenied` and the "you are not allowed" sentence are gone (5 outcomes, 5 sentences) and `Answer.UserID` is now only the identity of the sender, never an authorization input.
- **Reply handler**: internal/bot/handlers/reply is a thin telego adapter around `*bridge.Service`: it turns an update into a `bridge.Answer`, calls `Reply`, renders the outcome into the fixed sentence and sends it to the chat. It owns no config, no repository and no client (R19/R20 still hold).
- **DB**: MySQL/MariaDB via Bun (mysqldialect), Goose migrations in internal/db/migrations/ (//go:embed *.sql)
- **Metrics**: Prometheus via fiberfx (auto), per-module counters via promauto

## Module Conventions
- Each package exposes a Module(...) fx.Option (withRun bool for modules with background work)
- Handlers registered via group tags: Provide(..., fx.ResultTags(`group:"handlers"`))
- **Layering (R19/R20)**: handlers are thin adapters. A handler owns no config type, no repository and no client: configuration and durable state live in the service that enforces them, repositories are reached from services only, and a handler receives a service. User-facing wording stays in the handler that renders it.
- Internal-only deps use fx.Private
- Services with Run(ctx) use `fxutil.RegisterRunnable[*T]()` — conditionally via `withRun` bool
- Per-module named logger: logger.WithNamedLogger("name")
- Config module maps the raw Config struct to the value each consumer takes (fiberfx.Config, openapi.Config, telegofx.Config, bridge.Config, webhook.Config, smsgate.Config, boltfx.Config, store.Config)

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
