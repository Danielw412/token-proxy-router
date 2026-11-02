# internal/tpr/router

The native, in-process port of the custom-quota-router plugin, with one engine per
provider pool (`codex`, `claude`). User-facing behaviour is in
[docs/routing.md](../../../docs/routing.md); the API is in
[docs/DESIGN.md](../../../docs/DESIGN.md).

| Path | What it is |
| --- | --- |
| `engine/` | All routing policy. Pure: no I/O, no goroutines, time injected. Its tests are the plugin's, ported. |
| `router.go` | `PickAuth` (the scheduler) and `HandleUsage` (the usage plugin). Recovers every panic to not-handled. |
| `pool.go` | One pool's background work: roster, polling, persistence. |
| `hold.go` | Waits for a pause or slow down, outside the engine lock. |
| `starter.go` | Sends 5-hour window starts. |
| `host.go` | `Host`: everything the router needs from the server, on the auth manager. Replaces the plugin's `host.*` callbacks. |
| `api.go` | Management routes. |
| `import.go` | `ImportPluginState`, for migrating from the old plugin. |

## Rules

- **Never fail a request because of the router.** A panic, bad data or missing state
  returns not-handled, so upstream selection decides. Rejections are only for explicit
  user choices: a strict pin, every account Off, a pause past its limit.
- **`Engine.Pick` runs on every request under one mutex.** In-memory work only: no I/O,
  logging or calls out. The only deliberate wait is `Pool.Hold`, outside the lock.
- **Tokens stay in the executors.** `managerHost` calls the manager's `HttpRequest`,
  which adds the credential, so nothing in this package reads a token.
- **Only explicit choices reject.** Add a rejection only if the user asked for it.

## How it attaches

`internal/cmd/run.go` calls `backend.Start`, which calls `router.Start`, which calls
`Manager.SetSchedulerFront` (`sdk/cliproxy/auth/tpr_hooks.go`). Upstream calls
`SetPluginScheduler` on every config reload; the hook keeps the router in front and
makes whatever upstream installed the scheduler behind it, so requests the router does
not handle reach an upstream scheduler plugin exactly as before.

## State

Everything is under `data/router/`, not `config.yaml`: `<provider>.state.json` and
`<provider>.settings.json`. The state format is the plugin's, with fields only added,
so `ImportPluginState` needs no format conversion. Add fields only, zero-value safe.

## Tests

```bash
go test -race ./internal/tpr/router/...   # the engine and the service
go test -race ./internal/tpr/e2e/         # a real server against a mock upstream, about 80 s
```
