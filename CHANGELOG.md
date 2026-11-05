# Changelog

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
