# AGENTS.md

Guidance for AI agents and humans working on Token Proxy Router (TPR).

TPR is a fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and its
[Management Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center).
It is a Go 1.26+ server that turns Claude and ChatGPT/Codex subscriptions into one
API, with quota-aware routing, quota history, an embedded panel, and a one-command
setup on Linux and Windows.

Read first: [README.md](README.md), [docs/DESIGN.md](docs/DESIGN.md) (the binding
contract), then [docs/routing.md](docs/routing.md) or
[docs/upstream-sync.md](docs/upstream-sync.md) as your task needs.

## Hard rules

1. **Never expose credentials.** OAuth tokens, API keys and management keys must never
   appear in logs, API responses, errors, fixtures, docs or commits.
2. **This repository is public.** Never commit real account emails, auth IDs, hostnames
   (including tailnet names), IP addresses, screenshots of a live panel, `config.yaml`,
   `tpr.db`, backups or logs. Use `alice@example.com` and placeholders such as
   `<hub-url>`. Panel screenshots in docs must come from the dev fixtures.
3. **Production is live.** A hub carries real work. Do not stop, restart, migrate or
   reconfigure a running install, or touch its data folder, `~/.codex`, `~/.claude` or
   `~/.t3`, unless the user asked for that specific change. Test with `TPR_HOME` and
   `HOME` pointed at temp directories, and also set `T3CODE_HOME` to a temp directory:
   T3 Code exports it to shells it starts, and it overrides `HOME` for `tpr connect`.
4. **Only one server per set of logins.** Providers rotate refresh tokens, so two servers
   holding the same login log each other out. Never run a second instance on real auth
   files, including for a test.
5. **Never fail a request because of TPR.** A panic, bad data or missing state in router
   code falls back to upstream selection. Rejections happen only for explicit user
   choices: a strict pin, every account Off, or a pause past its limit.
6. **Respect provider limits.** Route only among credentials the host offers. Never
   disguise which account is used or add retries around a usage limit.
7. **Keep upstream merges cheap.** New code goes in `internal/tpr/**`. Upstream files get
   only small hooks marked `// TPR:`, and every touched file is listed in
   `docs/upstream-sync.md`. Do not rename, move or reformat upstream code, and keep the
   module path `github.com/router-for-me/CLIProxyAPI/v8`.
8. **One static binary.** No cgo, no runtime panel downloads, and no TPR feature may rely
   on dynamic plugins. It must keep working on Windows.

## Commands

```bash
export GOTOOLCHAIN=go1.26.10
make panel                               # build the panel into internal/tpr/webui/dist
make build                               # ./tpr for this machine
make check                               # gofmt, vet and tests for TPR and hooked packages, plus the panel
go test -race ./internal/tpr/<pkg>       # one package
go test -race ./internal/tpr/e2e/        # boots a real server against a mock upstream (-short skips it)
GOOS=windows go vet ./internal/tpr/<pkg> # check Windows code from Linux
TPR_HOME=$(mktemp -d) go run ./internal/tpr/quota/cmd/seed   # synthetic history for fake accounts
```

Panel, from `panel/` (`bun.lock` is authoritative; do not add a `package-lock.json`):

```bash
bun install --frozen-lockfile
bun run dev        # includes a dev-only mock of the TPR API with realistic fixtures
bun run verify     # tests, lint, build
```

On small machines use `GOFLAGS=-p=2`, test specific packages, and leave the whole upstream
suite to CI. `go test ./...` is expensive and mostly tests code TPR does not change.

## Code map

| Path | What it is |
| --- | --- |
| `internal/tpr/router` | Native quota router: one engine per pool (`codex`, `claude`). `router/engine` is pure policy with no I/O. |
| `internal/tpr/store` | bbolt database at `data/tpr.db`. |
| `internal/tpr/quota` | History collector, forecast math and quota routes. The math is pure and heavily tested. |
| `internal/tpr/alerts`, `health`, `keyusage` | Alerts, account health, usage per client key. |
| `internal/tpr/backend` | Starts the features above when the auth manager is ready. |
| `internal/tpr/cli` | The `tpr` commands. Dispatched from one line at the top of `main()`. |
| `internal/tpr/setup`, `service`, `update` | First run, autostart and supervisor, self-update and rollback. |
| `internal/tpr/tailscale`, `backup`, `connect`, `migrate` | Tailnet exposure and identity sign-in, encrypted backups, client configuration, import from CLIProxyAPI. |
| `internal/tpr/webui` | Embedded panel. `dist/` is built, not committed. |
| `internal/tpr/home` | Paths in the single data folder. Use it rather than building paths. |
| `internal/tpr/e2e` | End-to-end tests. |
| `panel/` | The web panel, a `git subtree` of upstream's panel that TPR changes freely. |
| `scripts/` | `install.sh`, `install.ps1`, `sync-upstream.sh`. |
| `cmd/server`, `internal/*`, `sdk/*` | Upstream server. Hooks only. |

Upstream's map, for when a task reaches into its code:

- `internal/api/` Gin HTTP API. `internal/runtime/executor/` per-provider executors.
  `internal/translator/` protocol translators. `internal/registry/` model registry.
  `internal/watcher/` config hot reload. `sdk/cliproxy/` service and auth manager.
- `internal/thinking/` is the reasoning pipeline. `ApplyThinking()` normalizes to a canonical
  `ThinkingConfig` and applies per-provider output. Keep that architecture.

## Adding a feature

1. Put it in `internal/tpr/<name>`, and register routes with `tpr.AddRoutes` from `init()`.
   Handlers answer 503 until the feature has started.
2. Add routes under `/v8/management/tpr` and document the exact shape in `docs/DESIGN.md`.
3. If the feature holds state, keep it in the store or `home.DataDir()`, not `config.yaml`, and
   contribute it to backups with `tpr.AddSnapshot`.
4. Add a fixture to `panel/dev/` so the panel works against the mock, and check the page
   against the real server too.

## Releasing

Update `CHANGELOG.md`, then push a `vX.Y.Z` tag. `release.yml` tests, builds and publishes.
Installed hubs see the update in the panel and in `tpr update`. Upstream changes arrive as
pull requests from `upstream-sync.yml`; see `docs/upstream-sync.md`.

## Code conventions

These are inherited from upstream. Follow them in upstream code and in `internal/tpr`.

- Keep changes small and simple. Comments in English only. New Markdown in English.
- Follow `gofmt`, keep imports goimports-style, and wrap errors with context where helpful.
- Do not use `log.Fatal` or `log.Fatalf`. Return errors and log with logrus.
- Shadowed variables take a method suffix (`errStart := server.Start()`).
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`.
- Avoid panics in HTTP handlers. Prefer logged errors and meaningful status codes.
- Timeouts are allowed only during credential acquisition. After an upstream connection is
  established, do not set timeouts on any later network behavior. Allowed exceptions, none of
  which carry model traffic except the first two, which have their own liveness rules:
  - Codex websocket deadlines in `internal/runtime/executor/codex_websockets_executor.go`
  - wsrelay session deadlines in `internal/wsrelay/session.go`
  - the management APICall timeout in `internal/api/handlers/management/api_tools.go`
  - read-only quota polls in `internal/tpr/router/host.go`
  - alert deliveries in `internal/tpr/alerts/deliver.go`
  - release checks and downloads in `internal/tpr/update`
  - hub checks, health probes and diagnostics in `internal/tpr/connect`, `internal/tpr/cli`
    and `internal/tpr/service`
  - utility timeouts in `cmd/fetch_antigravity_models`
- Avoid wall-clock `time.Sleep` in TTL, expiry or ordering tests. Use a controllable clock or
  deterministic synchronization, because Windows timers and CI load make sleeps flaky.
- Do not make standalone changes to `internal/translator/`.
- In upstream code, use `github.com/MoeForge/moego/ojson` and `github.com/MoeForge/moego/base64`,
  not `gjson`, `sjson` or `encoding/base64`. Make JSON edits on one `*ojson.Doc` and serialize
  it once at the end.
- `internal/runtime/executor/` holds executors and their tests only. Helpers go in
  `internal/runtime/executor/helps/`.
- Payload configuration must be the final semantic barrier before sending a request in every
  executor, including streaming, WebSocket, retry and token-count paths. Add regression tests
  when changing request-building paths.
- `/v0/management` is deprecated. New routes go under `/v8/management`.
