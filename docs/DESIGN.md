# Token Proxy Router: design and build contract

Token Proxy Router (TPR) is a friendlier fork of
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) and its
[Management Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center).
It turns Claude and ChatGPT/Codex subscriptions into one API that every machine
on a tailnet can use, with quota-aware routing, quota history and a one-command
setup. It is maintained for one person and published as open source.

This document is the contract the code is built against. If an implementation
has to differ, update this file in the same commit.

## Product rules

1. **Zero-config start.** `tpr` with no config generates safe defaults and random
   keys, binds to loopback, and opens a setup wizard.
2. **One data folder.** Everything lives in `~/.token-proxy-router` on every OS
   (`TPR_HOME` overrides it). See `internal/tpr/home`.
3. **One binary.** No runtime downloads of the panel, no dynamic plugins needed
   for any TPR feature, no cgo. The panel is compiled in. Linux and Windows are
   both first-class; macOS is built but best effort.
4. **Updates are one click and reversible.** Checksummed download, atomic swap,
   health check, automatic rollback.
5. **Upstream merges stay cheap.** New code goes in `internal/tpr/**`. Upstream
   files only get small hooks, each marked `// TPR:`. Keep the Go module path
   `github.com/router-for-me/CLIProxyAPI/v8`. Do not reformat or rename upstream
   code. Every touched upstream file is listed in `docs/upstream-sync.md`.
6. **Claude and Codex come first in the UI.** Other upstream providers keep
   working but sit behind a "More providers" expander.
7. **Never fail a request because of TPR.** A panic, bad data or missing state in
   router code falls back to the upstream selector. Rejections happen only for
   explicit user choices (strict pin, every account Off, a pause past its limit).
8. **Secrets stay secret.** Tokens, API keys and management keys never appear in
   logs, API responses, errors, fixtures, docs or commits. This repo is public:
   no real emails, auth IDs, hostnames, tailnet names or IPs. Use
   `alice@example.com` and `<hub-url>` placeholders.
9. **Respect provider limits.** Route only among credentials the host offers.
   Never retry around a usage limit or disguise which account is used.

## Repository layout

| Path | What it is |
| --- | --- |
| `cmd/server`, `internal/*`, `sdk/*` | Upstream server. Hooks only. |
| `panel/` | Upstream panel as a `git subtree`. Freely modified. |
| `internal/tpr/home` | Data folder paths. |
| `internal/tpr/routes.go` | `tpr.AddRoutes`: mounts feature routes at `/v8/management/tpr`. |
| `internal/tpr/router` | Native port of custom-quota-router, multi-provider. |
| `internal/tpr/backend` | Starts the router and the features below once the auth manager exists. |
| `internal/tpr/store` | bbolt database at `data/tpr.db`. Exposes `Backup(io.Writer)`. |
| `internal/tpr/quota` | Provider quota fetchers, history collector, forecast. |
| `internal/tpr/alerts` | Quota alerts and notification delivery. |
| `internal/tpr/health` | Account health. |
| `internal/tpr/keyusage` | Usage per client API key. |
| `internal/tpr/cli` | `tpr` subcommands. |
| `internal/tpr/setup` | First run and setup wizard state. |
| `internal/tpr/service` | systemd user service, Windows autostart, supervisor. |
| `internal/tpr/update` | Self-update and rollback. |
| `internal/tpr/tailscale` | Tailnet exposure and identity sign-in. |
| `internal/tpr/backup` | Encrypted backup and restore. |
| `internal/tpr/connect` | Client configuration: Codex, Claude Code, T3 Code. |
| `internal/tpr/migrate` | Import from an existing CLIProxyAPI install. |
| `internal/tpr/webui` | Embedded panel (`dist/`, built by `make panel`). |
| `scripts/` | `install.sh`, `install.ps1`, `sync-upstream.sh`. |

## Decisions

- **Router runs in-process.** `coreauth.Manager.SetPluginScheduler` already takes
  a Go interface (`PluginScheduler`), and `usage.RegisterPlugin` takes a Go
  `Plugin`. The port uses those directly. The plugin's `.so`, cgo bridge and
  version lock go away. `SetPluginScheduler` must chain safely with the upstream
  plugin host if one is active.
- **One engine per provider pool.** Codex and Claude each get an independent
  current account, ranking and history. A pick is handled only when the request's
  provider has an engine, so other providers use upstream selection untouched.
- **Storage is bbolt.** Pure Go, crash safe, single file, works on Windows.
  History retention: full detail for 30 days, hourly to 180 days, then dropped.
  A second process gets a clear error rather than a hang. Router state and settings
  are JSON files in `data/router/`, so routing runs even if the database cannot open.
- **Only this server's own calls have timeouts.** Upstream model traffic gets none
  (`AGENTS.md`). A window start bounds how long it waits, not the request. Read-only
  quota polls and alert deliveries carry no model traffic and are bounded, so one
  hung endpoint cannot stop a pool's maintenance.
- **Static builds.** Releases use `CGO_ENABLED=0`. Upstream's dynamic plugin host
  stays compiled where it works without cgo (Windows) but no feature relies on it.
- **Panel embedding.** `internal/tpr/webui/dist/` is git-ignored except `.gitkeep`.
  With no built panel the server serves a short page saying to run `make panel`.
  Upstream's runtime panel updater and its hosted fallback are disabled.
- **Hub and clients.** One always-on machine is the hub and holds the OAuth
  credentials. Every other machine runs `tpr connect` and uses the hub over the
  tailnet. Two hubs must not share credentials: rotating refresh tokens would log
  each other out. `docs/setup.md` says so plainly.
- **Default bind is loopback.** Remote access goes through `tailscale serve`.
- **Versioning.** TPR tags are `vMAJOR.MINOR.PATCH`, independent of upstream.
  `UPSTREAM.md` records the upstream server and panel versions in each release.
- **One supervisor everywhere.** Every service kind runs `tpr service-run`, which runs
  the server as a child. Any process asks it to restart or stop through a signal file
  in `data/`, since that works the same on Windows. It runs the health check after an
  update and rolls back. Release assets are named `tpr-<os>-<arch>[.exe]`.
- **Restore applies at the next start.** A running server holds its database open, and
  Windows will not replace an open file. So restore validates and stages the archive
  in `.restore-pending/`, and it is moved into place before the config is read.
- **First-run overrides.** `TPR_PORT` and `TPR_BIND_HOST` choose what a newly generated
  config says. A busy port moves to the next free one, and first run says so.

## CLI

```
tpr                         Run the server (first run: generate config, print the panel URL)
tpr install [--no-service]  Copy to bin/, add to PATH, install and start the service
tpr uninstall [--purge]     Remove the service; --purge also deletes the data folder
tpr start|stop|restart      Control the service
tpr status                  Service, version, port, tailnet URL, account summary
tpr logs [-f]               Show server logs
tpr open                    Open the panel in a browser
tpr creds [--forget]        Show first-run keys / delete the plaintext copy
tpr login <claude|codex>    Add an account (--no-browser for remote machines)
tpr expose [--port 8443]    Publish on the tailnet with tailscale serve
tpr unexpose
tpr connect --hub URL --key KEY [--codex] [--claude] [--t3] [--default] [--dry-run] [--undo]
tpr update [--check] [--rollback] [--version vX.Y.Z]
tpr backup [FILE] / tpr restore FILE
tpr migrate [--from DIR] [--dry-run] [--force]    Import an existing CLIProxyAPI install
tpr doctor                  Diagnose common problems
tpr version
```

Upstream flags keep working: with no recognized subcommand, argument handling
falls through to upstream `main`. `-config` still overrides the config path.

## Management API contract

All routes are under `/v8/management/tpr`, require the management key, and return
JSON. Errors are `{"error":"message"}`. Times are RFC 3339. `provider` is `codex`
or `claude`. Account objects never contain tokens. Emails are returned as-is; the
panel blurs them.

### Router (ported from custom-quota-router)

| Route | Body | Does |
| --- | --- | --- |
| `GET /router/state?provider=&history=N` | | `{provider, mode, active, active_reason, accounts:[...], sessions:[...], settings, history:[...], pause, slowdown, waiting, windows:[...], last_error}` |
| `POST /router/mode?provider=` | `{"mode":"auto"}` or `{"mode":"manual","account":"id"}` | Automatic, or pin |
| `POST /router/select?provider=` | `{"account":"id"}` | Use now |
| `POST /router/refresh?provider=` | | Re-check quota |
| `GET/PATCH /router/settings?provider=` | settings keys | Ranking, threshold, order, roles and the rest |
| `POST /router/windows/start?provider=` | `{"delay_minutes":0,"stagger_minutes":0,"account":""}` | Start 5-hour windows |
| `POST /router/windows/schedule?provider=` | `{"account":"id","at":"...","daily":true}` | Schedule a start |
| `POST /router/windows/cancel?provider=` | `{"id":""}` or `{"account":""}` or `{}` | Cancel starts |
| `POST /router/pause?provider=` | `{"minutes":0,"auto_resume_minutes":20}` | Pause new requests |
| `POST /router/resume?provider=` | | Resume |
| `POST /router/slowdown?provider=` | `{"seconds":30}` | Delay each request |

`provider` may be omitted on `state` to get `{"pools":[...]}` for every pool. Every
other route needs it, and answers 400 without a valid one. The router's mutating
routes answer with the pool's state.

`GET /router/state` also carries `enabled`, `now`, `pinned`, `active_manual`,
`active_since`, `routing` and `routing_note` (the account a new session uses right
now, and why), `session_count`, `warm_sessions`, `warnings`, `starter`
(`{model, effort, plans, log, last_cost}`), `picks`, `pick_stats` and `last_poll`. In
it:

- `accounts[]`: `{id, label, email, plan, role, order, state, detail, active, pinned, disabled, short, long, observed_at, source, stale, blocked_until, poll_error, sessions, warm_sessions, busy_sessions, idle_sessions, last_used, window, window_reset_at, plans, auto_start_at}`. `state` is `ok|low|exhausted|unavailable|unknown|off`. `short` and `long` are `{remaining_percent, estimated, window_minutes, reset_at}`.
- `sessions[]`: `{id, account, label, last, bound_at, last_seen, requests, state}`, newest first, at most 200. `state` is `busy|warm|cold`.
- `windows[]`: `{account, label, state, reset_at, auto_start_at, plans}`. `state` is `running|idle|unknown`.
- `pause`: `null`, or `{since, until, auto_resume_minutes, next_auto_resume}`.
- `slowdown`: `{seconds}`.
- `history[]`: `{at, kind, from, to, reason, count}`, newest first.
- A time that is not set is left out, not sent as a zero value.

`GET` and `PATCH /router/settings` return `{provider, settings, warnings}`. `PATCH`
takes any settings keys (`docs/routing.md` lists them); out-of-range values are clamped
and reported in `warnings`, and an unknown key answers 400.

### Quota history and forecast

`GET /quota/pools?from=&to=&burn=3h&window=weekly&future=same`

- `burn`: `1h`, `3h`, `6h`. `window`: `weekly`, `session`.
- `future`: `same` (current burn), `idle` (no usage), `double` (twice the burn).

```json
{
  "now": "2026-10-26T12:00:00Z",
  "from": "...", "to": "...",
  "pools": [{
    "provider": "claude",
    "accounts": [{
      "id": "...", "label": "...", "email": "alice@example.com", "plan": "max",
      "weight": 1.0, "remaining_percent": 91, "reset_at": "...", "status": "ok"
    }],
    "remaining_percent": 555,
    "capacity_percent": 600,
    "weight_note": "Pro Max = 2.5x",
    "pool_empty_at": null,
    "first_empty": {"account": "id", "at": "..."},
    "next_reset": {"at": "...", "delta_percent": 100},
    "burn_per_hour": 3.7,
    "recorded": [{"at": "...", "percent": 580}],
    "projected": [{"at": "...", "percent": 540}],
    "resets": [{"at": "...", "delta_percent": 100, "projected": true}],
    "empty_marks": [{"at": "...", "label": "Empty"}],
    "coverage": {"first_sample_at": "...", "samples": 1234}
  }]
}
```

Percent values are pool-weighted: each account contributes `remaining * weight`
and `capacity = sum(100 * weight)`, so the numbers match the reference design
(`555% of 600%`). `recorded` is real samples only; `projected` starts at `now`.

- `from` and `to` default to a week either side of `now`, and can span at most 366 days.
- Weight is the account's plan size against the pool's smallest plan, so a pool of one
  plan is `100%` per account. `weight_note` names the weights that are not 1.
- Accounts that are Off, disabled or have no data yet are listed but not counted in the
  totals. Their `remaining_percent` is `null` if unknown. `status` is the router's
  `state` above.
- Accounts drain one at a time in routing order, as the router uses them: the current
  one first, reserves last. A reset gives back quota at its known time and recurs each
  period. Claude's weekly window keeps a fixed schedule; a Codex window and a 5-hour
  window restart at the first use after a reset.
- `projected` holds both sides of a reset at the same `at`, so it draws as a step.
- `first_empty`, `next_reset` and `pool_empty_at` are `null` when none falls before `to`.
- `resets[].projected` is `false` for a reset found in history.
- `coverage.samples` counts samples inside `from` and `to`; `first_sample_at` is the
  oldest kept at all. `recorded` is thinned to at most 1500 real sample times.

`GET /quota/accounts/:id/history?from=&to=&window=` returns one account's samples:
`{id, provider, window, samples:[{at, percent, reset_at, source}]}`. `percent` is
remaining. `window` defaults to `weekly`, and `to` to `now`. An auth ID may contain a slash.

`POST /quota/refresh` asks every account to poll now and answers 202
`{"requested":true}`. Past once a minute it answers 429 with `Retry-After`. Results arrive
through `GET /quota/pools` as the polls finish, at most 3 per pool every 15 seconds.

### Alerts

- `GET /alerts` / `PUT /alerts`: `{"enabled":true,"pool_empty_within_hours":6,"account_below_percent":10,"notify_on_reset":false,"channels":[{"type":"ntfy|discord|slack|webhook","url":"...","name":"..."}]}`. Channel URLs are secrets: `GET` masks them to `https://host/…`. A `PUT` that sends a masked URL back keeps the stored one. A threshold of 0 turns that alert off. Both also return `active`, the same list as below.
- `POST /alerts/test`: sends a test to each channel, `{"results":[{"name":"","ok":true,"error":""}]}`.
- `GET /alerts/events?limit=50`: `{"events":[{"at","kind","provider","account","message"}],"active":[{"key","kind","provider","account","message","since"}]}`. `kind` is `pool_empty_soon|account_low|quota_reset|cleared|delivery_failed`. The panel shows `active` as a banner.

One condition notifies once until it clears. `account_below_percent` watches the weekly
window. Delivery is not retried; a failure goes to the events log. Email addresses are
shortened in what is sent to a channel, and shown in full in the panel.

### Account health

`GET /health/accounts` returns:

```json
{"accounts":[{"id":"","provider":"claude","label":"","email":"","status":"ok|expiring|expired|failing|cooling|disabled","reason":"","last_success_at":"","last_error_at":"","last_error":"","token_expires_at":"","reauth":{"provider":"claude"}}]}
```

`last_error` is sanitized. The panel's Re-login button opens the existing OAuth
flow for `reauth.provider`. `reauth` is `null` for an API-key credential. Times are
`null` when unknown. Only Codex and Claude accounts are listed.

### Usage per client key

`GET /usage/keys?from=&to=&bucket=hour|day` returns:

```json
{"keys":[{"key_id":"","name":"","masked":"sk-...abcd","requests":0,"failures":0,"input_tokens":0,"output_tokens":0,"cached_tokens":0,"last_used_at":"","models":[{"model":"","requests":0,"total_tokens":0}],"series":[{"at":"","requests":0,"total_tokens":0}]}]}
```

`key_id` is a hash prefix, never the key, and the same `key_id` as `GET /connect`.
`from` defaults to a week ago and `to` to now. `bucket` defaults to `hour`, and `day` is a
calendar day in the server's time zone. Every configured key is listed, used or not, named
`Key N` by position; a key removed from the config keeps its history as `Removed key`, with
an empty `masked`. Names the panel keeps in the browser apply on top of these.

### System, update, setup

- `GET /system`: `{"version","commit","upstream_version","panel_version","data_dir","os","arch","uptime_seconds","service":{"kind":"systemd|windows-task|windows-run|none","installed":true,"running":true,"supervised":true},"setup":{"completed":false}}`
- `GET /update` (cached): `{"current","latest","available":true,"notes_url","notes","checked_at","can_apply":true,"reason","rollback_available":true,"state":"idle|downloading|verifying|applying|restarting|failed","error":""}`
- `POST /update/check`, `POST /update/apply` (optional `{"version":""}`), `POST /update/rollback`. Apply answers 202; the panel polls `GET /update` and reloads when `/system.version` changes.
- `GET /setup`, `POST /setup/complete`. The wizard shows when `setup.completed` is false.

### Tailscale

- `GET /tailscale`: `{"installed":true,"logged_in":true,"dns_name":"","exposed":true,"url":"https://<host>:8443","port":8443,"can_expose":true,"hint":"","identity":{"enabled":false,"allowed_logins":[],"available":true}}`
- `POST /tailscale/expose` `{"port":8443}`, `DELETE /tailscale/expose`.
- `PUT /tailscale/identity` `{"enabled":true,"allowed_logins":["alice@example.com"]}`.

**Identity sign-in** is off by default. When enabled, a management request is
accepted without a key only if all hold: the TCP peer is loopback, the bind address
is loopback, the `Tailscale-User-Login` header is in `allowed_logins`, and the local
Tailscale API's `whois` for the forwarded client address returns the same login.
Any failed check falls through to the normal key check. It never grants anything
on a non-management route.

Tailscale identity is ambient: a browser on the tailnet carries it on every request,
and upstream answers with `Access-Control-Allow-Origin: *`. So two more conditions
must hold, or any website the user visits could read the management API: the request's
`Host` is this machine's tailnet name, and a browser request must be same-origin
(`Origin` matches `Host`, `Sec-Fetch-Site` is not cross-site). Funnel requests are
refused, and so are a repeated or list-valued `Tailscale-User-Login` or
`X-Forwarded-For`, and a forwarded address outside Tailscale's ranges.

`GET /session` (unauthenticated, under `/v8/management/tpr-public/session`) tells
the login page whether identity sign-in applies to this request:
`{"identity_login":false,"login":""}`.

### Backup

- `POST /backup` `{"passphrase":"..."}` streams an encrypted archive (config, auth,
  router state, history). scrypt plus AES-256-GCM. A passphrase of 12+ characters
  is required.
- `POST /restore` multipart `file` + `passphrase`. Validates, snapshots the current
  state to `backups/`, applies, then restarts.

### Connect

`GET /connect` returns what the panel needs to show copy-ready setup:

```json
{
  "hub_urls": [{"label": "Tailnet", "url": "https://<host>:8443"}, {"label": "This machine", "url": "http://127.0.0.1:8317"}],
  "keys": [{"key_id": "", "name": "", "masked": ""}],
  "models": {"codex": ["..."], "claude": ["..."]},
  "install": {
    "linux": "curl -fsSL https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.sh | sh",
    "windows": "irm https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.ps1 | iex"
  }
}
```

`POST /connect/snippets` `{"key_id":"","hub_url":""}` returns the commands and config
for that key, including the real key, because the owner asked for them:
`{"linux":"...","windows":"...","codex_toml":"...","claude_env":"...","claude_env_windows":"...","t3":"..."}`.
Management-key auth is required and the response is `Cache-Control: no-store`.

## T3 Code integration

T3 Code runs provider instances from `settings.json` in `$T3CODE_HOME/userdata`
(default `~/.t3/userdata`):

```json
"providerInstances": {
  "tpr-codex": {
    "driver": "codex",
    "displayName": "Router: Codex",
    "enabled": true,
    "environment": [{"name": "TPR_API_KEY", "value": "", "sensitive": true, "valueRedacted": true}],
    "config": {"launchArgs": "-c model_provider=tpr -c model_providers.tpr.name=TokenProxyRouter -c model_providers.tpr.base_url=<hub>/v1 -c model_providers.tpr.env_key=TPR_API_KEY -c model_providers.tpr.wire_api=responses"}
  },
  "tpr-claude": {
    "driver": "claudeAgent",
    "displayName": "Router: Claude",
    "enabled": true,
    "environment": [
      {"name": "ANTHROPIC_BASE_URL", "value": "<hub>", "sensitive": false},
      {"name": "ANTHROPIC_AUTH_TOKEN", "value": "", "sensitive": true, "valueRedacted": true}
    ]
  }
}
```

Sensitive values live beside it in
`secrets/provider-env-<base64url(instanceId)>-<base64url(varName)>.bin` as raw bytes,
mode 0600. `tpr connect --t3` writes both. It backs up `settings.json` first, merges
only its own instances, writes atomically, and never removes or edits other instances.

Conversations must stay on one account while their prompt cache is warm, including
T3 subagents. The router's session tracking must recognise Codex session headers and
Claude Code's session identifiers (`X-Claude-Code-Session-Id`, and `metadata.user_id`
in the request body when the header is absent).

## Testing

- `make check`: `go vet` and `go test` on `./internal/tpr/...` plus the touched
  upstream packages, then the panel's type check and tests.
- End-to-end tests run an isolated server with `TPR_HOME` in a temp dir and a mock
  upstream. **Nothing may touch the live install, `~/cliproxyapi`, `~/.cli-proxy-api`,
  `~/.t3`, `~/.codex` or `~/.claude`.**
- This machine has 4 modest cores. Use `GOFLAGS=-p=2`, test specific packages rather
  than `./...`, and never run the whole upstream test suite locally. CI runs more.
- CI runs on Ubuntu and Windows, so Windows behaviour gets real coverage there.
