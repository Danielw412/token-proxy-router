# Changelog

## Unreleased

### Changed

- Based on CLIProxyAPI v8.1.0 (was v8.0.76) and Management Center v1.28.3 (was v1.28.1).
  Upstream's changes include a persistent server ID in `config.yaml`, a GitHub Copilot
  provider, many Antigravity fixes, steadier stream failover and cancellation in the auth
  manager, and payload replace rules for more fields in the panel.
- New installs write a `server.server-id` on first run. Existing installs get one from
  upstream on their next start.
- GPT-6.2 Sol and Astra now come from upstream's model catalogs, so TPR no longer adds them.

## v1.0.8

### Fixed

- Codex's model picker lists the hub's models, including ones newer than the Codex release, such
  as GPT-6.2 Sol and Astra. `tpr connect` now sets `model_catalog_url`, so Codex loads its picker
  from the hub instead of the list built into Codex. Run `tpr connect` again to pick this up.

## v1.0.7

### Added

- GPT-6.2 Sol and GPT-6.2 Astra for Codex Plus, Pro and Business accounts, from OpenAI's
  Codex model catalog. Codex clients need version 0.166.0 or later to list them.
- TPR can now list a model before upstream's model catalogs do. The hub keeps adding it to
  every catalog it loads or refreshes until upstream lists the model itself.

### Changed

- Based on CLIProxyAPI v8.0.76 (was v8.0.61) and Management Center v1.28.1 (was v1.27.3).
  Upstream's changes include payload rules applied as the last step before every request,
  user text replacement rules, Retry-After headers kept on Codex overload errors, Claude
  expired refresh tokens treated as final, more reliable Codex websocket failover, Gemini 4
  models, and a login proxy for OAuth sign-ins.
- The panel takes upstream's plugin page, quota card layouts, Codex credit overage controls
  and model catalog refresh on the accounts page. The plugin store, the dashboard page and
  sponsor providers stay out.

## v1.0.6

### Changed

- Based on CLIProxyAPI v8.0.61 (was v8.0.52) and Management Center v1.27.3 (was v1.27.2).
  Upstream's changes include more robust Codex token refresh, a fix for disabled credentials
  that kept trying to refresh, prompt-cache fixes for Claude and Codex, and Claude errors that
  show when a cooldown ends.
- Includes upstream's fix for a Claude stream that could hang after a few idle minutes, a
  regression in v8.0.60 that v8.0.61 still has.
- The panel's quota pages name limits the same way for every provider, and plugins can offer
  more than one sign-in. Upstream's new AI Code Mirror sponsor provider is left out.

### Fixed

- Claude Fable 5.2 works from Claude Code clients that TPR does not recognize as the plain CLI,
  such as T3 Code. TPR sent those requests to Anthropic as Claude Code 2.1.280, and Anthropic
  refused Fable 5.2 from versions older than 2.1.290. TPR now presents Claude Code 2.1.321, with
  upstream's matching SDK version, Node version and beta flags.
- A backup taken while the hub runs no longer copies upstream's new key-value database,
  `auth/kv.db`, which is open at the time. A restore keeps this machine's copy.

## v1.0.5

### Added

- **Stagger** for auto-start (`stagger_windows`, off by default) spaces 5-hour window starts
  evenly across a pool's accounts: one every 5 hours divided by the number of accounts, so
  with 3 accounts a window starts every 100 minutes and one resets every 100 minutes. Turn it
  on next to *Auto-start* on the Routing page. `GET /router/state` reports it as
  `starter.stagger_accounts` and `starter.stagger_minutes`.

## v1.0.4

### Fixed

- Streamed responses (`text/event-stream`) are sent uncompressed, as the Anthropic and OpenAI
  APIs send them. Claude Code on Windows, connecting through Tailscale Serve, sometimes stopped
  partway through a reply that was writing a large tool call and waited until stopped, while the
  hub logged the request as complete. Other responses are still compressed.

## v1.0.3

### Added

- The Dashboard shows each pool's 5-hour quota next to its weekly quota, and a 5-hour and a
  weekly bar for every account.
- Quota Management shows the pool's 5-hour quota and a 5-hour bar on every account card.
- `GET /quota/pools` returns `five_hour_remaining_percent` and `five_hour_reset_at` for each
  account, and `five_hour_remaining_percent` and `five_hour_capacity_percent` for each pool,
  whichever `window` is asked for.

### Fixed

- The Routing page counted every conversation the router remembers, including ones idle for up to
  a day, so it showed more sessions than were running. It now shows conversations running now and
  idle ones with a warm cache, and counts older ones separately.

## v1.0.2

### Added

- The Routing page has a *Route all traffic through* picker: choose one account to send every
  request in the pool to, or *Automatic*. While an account is chosen, you can also choose whether
  requests route automatically or are rejected if it becomes unavailable.

### Changed

- The Routing page opens on the Codex pool instead of Claude.

## v1.0.1

### Changed

- Installs and updates download a compressed build, about a third of the size. The plain files are
  still published so that v1.0.0 can update.
- The install scripts show speed and time left, fail on a stalled connection, and no longer report
  Ctrl-C as a failed download.

### Fixed

- `tpr install` on Linux wrote a systemd unit that systemd refused to start, because
  `WorkingDirectory=` was quoted. The service never came up. Tests now check the unit with
  `systemd-analyze verify`.
- The switch-over commands in the docs and in `tpr migrate` stop at the first failure, so a failed
  step cannot stop the old server with nothing to replace it.
- `install.sh` no longer leaves its temp folder behind after a successful install.

## v1.0.0

First release of Token Proxy Router, based on CLIProxyAPI v8.0.52 and Management Center v1.27.2.

### Added

- New panel: a redesigned interface, a setup wizard, Quick Start, Connect, Usage, Alerts and System
  pages, and blurred emails by default.
- Built-in quota routing with separate Claude and Codex pools, ported from the custom-quota-router
  plugin. No plugin build or version matching is needed any more.
- Recorded quota history, burn rate and forecasts, stored in `data/tpr.db`.
- Quota alerts through ntfy, Discord, Slack and webhooks.
- Account health with one-click re-login, and usage per client key.
- `tpr` command: first-run setup, install, connect, expose, update, rollback, backup, restore,
  migrate and doctor.
- Self-update with checksum verification, a health check and automatic rollback.
- One-command client setup for Codex, Claude Code and T3 Code, with undo.
- Tailnet exposure, and optional sign-in with your tailnet identity.
- Encrypted backup and restore.
- Windows support for installation and autostart.
- Weekly automatic pull requests that merge upstream releases.

### Changed

- The panel is compiled into the binary, and upstream's runtime panel download is off.
- Everything lives in one data folder, `~/.token-proxy-router`.

### Removed

- Sponsor and promotional content.
