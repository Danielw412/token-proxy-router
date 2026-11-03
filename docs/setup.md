# Setup

Token Proxy Router (TPR) runs on one always-on machine, the **hub**. The hub holds your Claude and ChatGPT/Codex logins. Every other machine is a **client**: it runs `tpr connect` and uses the hub over your tailnet.

Have **one hub per set of accounts**. Providers rotate refresh tokens, so when two servers hold the same login, each refresh logs the other one out. This includes an old CLIProxyAPI install: run one or the other, never both.

## Set up the hub

### Linux

```sh
curl -fsSL https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.sh | sh
```

This puts `tpr` in `~/.token-proxy-router/bin`, adds it to your `PATH`, generates a config with random keys, and starts a systemd user service. It listens on `127.0.0.1:8317` only.

Open a new terminal, then:

```sh
tpr creds    # your client API key and management key
tpr open     # the panel, which walks you through the rest
```

A user service stops when you log out unless lingering is on. `tpr install` tries to turn it on. If it says it could not, run:

```sh
sudo loginctl enable-linger $USER
```

If a machine has no systemd user session, `tpr install` starts the server in the background and says so. It will not come back after a reboot until you fix that and run `tpr install` again.

### Windows

In PowerShell, without admin rights:

```powershell
irm https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.ps1 | iex
```

TPR tries to create a Scheduled Task that starts at logon. Windows often refuses that without admin rights, so it falls back to a logon entry in `HKCU\...\Run`. Both start when you log in. `tpr status` reports which one you got. Neither runs before someone logs in, so turn on automatic sign-in if the hub has to survive a reboot on its own.

Open a new terminal, then `tpr creds` and `tpr open` as above.

### Add accounts

Use the panel, or:

```sh
tpr login claude
tpr login codex
```

On a machine with no browser, add `--no-browser` and open the printed address elsewhere. Run the command again to add another account.

### Moving from CLIProxyAPI

`tpr migrate` copies your config, client keys, hashed management key, auth files and router plugin state into the data folder. The old install is not changed.

```sh
tpr migrate --dry-run                # see what would be imported
tpr migrate                          # or: tpr migrate --from /path/to/old/folder
systemctl --user disable --now cliproxyapi.service
tpr install
```

Your existing keys keep working. Run `tpr migrate` right before switching, because the old server keeps refreshing its logins until you stop it, and a copy taken earlier may stop working.

To go back, run `tpr stop` and start the old server again. Any login TPR has refreshed by then no longer works on the old server, so log in to those accounts again there. Do not leave both running.

## Publish on your tailnet

On the hub, with [Tailscale](https://tailscale.com/download) signed in:

```sh
tpr expose               # https://<host>:8443
tpr expose --port 9443   # or another port
```

This runs `tailscale serve` for one HTTPS port, and it refuses a port that is already used for something else. `tpr unexpose` removes that mapping and leaves all others alone.

If Tailscale says this user may not change serve settings, run this once and try again:

```sh
sudo tailscale set --operator=$USER
```

On Windows, run `tailscale set --operator=%USERNAME%` in a terminal opened with "Run as administrator".

### Sign in with your tailnet identity

This is optional, off by default, and only available on Linux hubs. Turn it on in the panel, with the logins you want to allow, to open the panel from the tailnet without typing the management key.

A request skips the key only if all of these hold: it came through `tailscale serve` on this machine, the connection is owned by root (as `tailscaled` runs) or by the user running TPR, the server is bound to loopback, its `Tailscale-User-Login` is allowed, Tailscale's own `whois` for the sender's address agrees, it is addressed to this machine's tailnet name, and it did not come from another website. If any check fails, the normal key check applies.

## Set up a client

Copy the command from the panel's Connect page, or run it yourself with your hub address and a client API key.

Linux:

```sh
curl -fsSL https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.sh | sh -s -- connect --hub <hub-url> --key <key>
```

Windows:

```powershell
$env:TPR_INSTALL_ARGS='connect --hub <hub-url> --key <key>'; irm https://github.com/Danielw412/token-proxy-router/releases/latest/download/install.ps1 | iex
```

This installs `tpr` without a service, checks that the hub answers and accepts the key, and configures whichever tools it finds. To choose, add `--codex`, `--claude` or `--t3`. To preview, add `--dry-run`.

To put everything back:

```sh
tpr connect --undo
```

A file you have not edited since returns byte for byte. A file you have edited keeps your edits and loses only what TPR added.

### What each tool gets

| Tool | What changes | How to use it |
| --- | --- | --- |
| Codex | `[model_providers.tpr]` and `[profiles.tpr]` in `~/.codex/config.toml`. `TPR_API_KEY` in a sourced `~/.token-proxy-router/env` on Linux, or the user environment on Windows. | `codex --profile tpr`. Add `--default` to `tpr connect` to make the hub Codex's default. |
| Claude Code | `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` in the `env` block of `~/.claude/settings.json`. | Just run `claude`. **All Claude Code use on that machine now goes through the hub.** |
| T3 Code | Two provider instances, "Router: Codex" and "Router: Claude", in `~/.t3/userdata/settings.json`, with their keys in `secrets/`. `T3CODE_HOME` moves this. | Restart T3 Code, then pick a Router provider. Other instances are untouched. |

## Troubleshooting

Start with `tpr doctor`. It checks the service, the port, your keys, accounts, Tailscale, the clock, updates, and whether an old CLIProxyAPI is still running, and it gives a next step for each problem.

| Symptom | Likely cause and fix |
| --- | --- |
| Accounts keep getting logged out | Two servers hold the same login. Stop one. `tpr doctor` finds an old CLIProxyAPI. |
| `port 8317 is already in use` on first run | Something else has that port, so TPR picked the next free one and printed it. To choose, edit `server.port` in `config.yaml`. |
| `tpr: command not found` | Open a new terminal. If that does not help, run `~/.token-proxy-router/bin/tpr install --no-service`. |
| Clients say they cannot reach the hub | Check `tpr status` on the hub for the tailnet address, and `tailscale status` on the client. |
| `the hub rejected this API key` | Use a client key from the hub, with `tpr creds` or the panel. The management key will not work here. |
| OAuth login or quota times look wrong | Run `tpr doctor`. A clock that is more than a couple of minutes off breaks both. |
| The panel says to run `make panel` | This binary came from source without a panel build. Run `make panel build`, or use a release. |

Logs are at `~/.token-proxy-router/logs/service.log`, or use `tpr logs -f`.
