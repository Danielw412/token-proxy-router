# Token Proxy Router

Use your Claude and ChatGPT/Codex subscriptions from every machine you own, through one
self-hosted API that picks the right account, shows how much quota you have left, and
predicts when it runs out.

Token Proxy Router (TPR) is a friendlier fork of
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It keeps that project's
provider support and adds a new panel, built-in quota routing, recorded quota history, a
one-command setup and one-click updates. It runs on Linux and Windows.

## What it does

- **One command to set up.** Install it and it generates safe defaults and random keys, then
  opens a setup wizard. There is no config file to write.
- **Quota routing, built in.** Requests go to one current account until it runs low, then
  move on. A conversation and its subagents stay on one account while their prompt cache is
  warm. Claude and Codex each get their own pool, ranking, roles, pause and slow-down.
- **Quota history and forecasts.** See remaining quota over time, your burn rate, the next
  reset, and when each account and the whole pool will run out.
- **Alerts.** Get a notification through ntfy, Discord, Slack or a webhook before a pool
  runs dry.
- **Connect any machine.** One command configures Codex, Claude Code and
  [T3 Code](docs/setup.md#what-each-tool-gets) to use your hub, and one command undoes it.
- **Tailnet ready.** Publish it on your tailnet with a toggle. It only ever binds to
  loopback by itself.
- **Easy to keep current.** Update from the panel or with `tpr update`. Each update is
  checksummed, health-checked, and rolled back automatically if the new version does not
  come up.
- **Your data stays with you.** One data folder, encrypted backups, no telemetry added.

## Quick start

Pick one always-on machine to be the **hub**. It holds your logins.

```sh
# Linux
curl -fsSL https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.sh | sh
```

```powershell
# Windows, in PowerShell, without admin rights
irm https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.ps1 | iex
```

Open a new terminal, then:

```sh
tpr open     # the panel, which walks you through adding accounts
tpr expose   # publish on your tailnet
```

On each other machine, copy the command from the panel's **Connect** page. It looks like this:

```sh
curl -fsSL https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.sh | sh -s -- connect --hub <hub-url> --key <key>
```

Already running CLIProxyAPI? `tpr migrate` imports your config, keys, accounts and router
plugin state without changing the old install. See [Moving from CLIProxyAPI](docs/setup.md#moving-from-cliproxyapi).

> **Run one hub per set of accounts.** Providers rotate refresh tokens, so two servers holding
> the same login log each other out.

## Everyday commands

| Command | Does |
| --- | --- |
| `tpr open` | Open the panel |
| `tpr status` / `tpr doctor` | Show state / diagnose problems and suggest fixes |
| `tpr login claude` / `tpr login codex` | Add an account |
| `tpr creds` | Show your first-run keys |
| `tpr expose` / `tpr unexpose` | Publish on, or remove from, your tailnet |
| `tpr connect --hub URL --key KEY` | Point this machine's tools at a hub (`--undo` reverses it) |
| `tpr update` / `tpr update --rollback` | Update / go back to the previous version |
| `tpr backup` / `tpr restore FILE` | Encrypted backup and restore |
| `tpr logs -f` | Follow the logs |

## Documentation

| Guide | Covers |
| --- | --- |
| [Setup](docs/setup.md) | Hubs and clients on Linux and Windows, tailnet, T3 Code, troubleshooting |
| [Operations](docs/operations.md) | Updates, rollback, backups, service management, uninstalling, building |
| [Routing](docs/routing.md) | How accounts are chosen, and every routing setting |
| [Design](docs/DESIGN.md) | Architecture, decisions and the management API |
| [Upstream sync](docs/upstream-sync.md) | How upstream changes are merged |
| [AGENTS.md](AGENTS.md) | Rules and commands for contributors and coding agents |

## Provider terms

TPR uses your own accounts and sends your requests to the providers on your behalf. Check that
your use fits each provider's terms, because those are your responsibility. TPR never hides which
account a request uses and does not try to get around usage limits.

## Credits and license

MIT. See [LICENSE](LICENSE). Built on [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
and [Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)
by Router-For.ME and their contributors, whose MIT license and notices are kept. The versions in
use are recorded in [UPSTREAM.md](UPSTREAM.md).

TPR is an independent project, not affiliated with those projects, Anthropic or OpenAI.
