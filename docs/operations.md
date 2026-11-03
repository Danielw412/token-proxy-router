# Operations

Everything TPR owns is in one folder, `~/.token-proxy-router` on every OS. Set `TPR_HOME` to move it.

| Path | What it is |
| --- | --- |
| `config.yaml` | Server configuration |
| `credentials.txt` | First-run plaintext keys, owner-only. Delete with `tpr creds --forget`. |
| `auth/` | OAuth logins |
| `data/` | Router state, quota history, setup and update state |
| `logs/service.log` | Server output when running as a service |
| `backups/` | Config snapshots and pre-restore archives |
| `bin/` | The installed binary and its rollback copy |

## Service

```sh
tpr status                 # service, version, port, tailnet address, accounts
tpr start | stop | restart
tpr logs [-f] [-n 500]
```

| OS | Kind | Starts | Manage with |
| --- | --- | --- | --- |
| Linux | `systemd` | At boot, with lingering on | `systemctl --user ... token-proxy-router` also works. |
| Windows | `windows-task` | At logon | Task Scheduler, "TokenProxyRouter" |
| Windows | `windows-run` | At logon | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` |
| Any | `none` | Only by `tpr start` | `tpr` commands |

Every kind runs the same supervisor, `tpr service-run`. It starts the server as a child process, restarts it after a crash with growing delays, writes its output to `logs/service.log`, and handles the update handoff. `journalctl --user -u token-proxy-router` shows only the supervisor's start and stop, so use `tpr logs` for the server's own output.

macOS builds run, but `tpr install` sets up no autostart there. Use `tpr start`.

## Updates

```sh
tpr update --check          # is there a newer version?
tpr update                  # install it
tpr update --version v1.2.3 # install this exact version, which also allows a downgrade
tpr update --rollback       # go back
```

The panel's update button does the same.

1. Download the build for this machine.
2. Verify its SHA-256 against the release's `checksums.txt`. A mismatch or a missing entry stops here and changes nothing.
3. Run its `version` command. One that cannot run on this machine stops here too.
4. Copy `config.yaml` to `backups/`.
5. Keep the current binary as `bin/tpr.previous`, then swap in the new one in one step. On Windows, the running file is renamed aside first, and removed on a later run.
6. If a supervisor is running, restart the server and wait for `/healthz`. If the new version exits, or does not answer within 90 seconds, the supervisor puts the old binary back and restarts it. `tpr update` then reports the failure and exits non-zero, and the panel shows the reason.

Without a service there is nothing to restart, so the update ends at step 5. If you run `tpr` in a terminal, restart it yourself. Client machines update the same way.

`tpr update --rollback` makes the current and previous versions trade places. It gets the same health check, so if the older version no longer starts, for instance because it cannot read the current config, the service returns to the newer one.

The supervisor stays on the version it started with until the service itself restarts, which `tpr restart` does on Linux. On Windows, log out and in again. This matters only for a release that says it changes the supervisor.

Checks use GitHub's API without a token and are cached for 6 hours. If GitHub rate limits a check, TPR keeps the last answer. Development builds (`make build` without a version tag) will not replace themselves unless you pass `--version`.

## Backup and restore

```sh
tpr backup                       # writes to backups/
tpr backup /path/to/file.tprbak
tpr restore /path/to/file.tprbak
```

Set `TPR_BACKUP_PASSPHRASE` for scripts. Otherwise it asks, and the passphrase must be at least 12 characters. **Nothing can open a backup without its passphrase.**

An archive holds the config, auth files, router state and quota history. It is encrypted with AES-256-GCM using a key derived with scrypt. It does not hold `credentials.txt` or the binary.

With the server running, these commands go through its API, because only the server can copy its database consistently. They need the management key: from `credentials.txt`, or from `TPR_MANAGEMENT_KEY` if you deleted that file. With the server stopped, they work on the files directly.

Restore validates the whole archive first: the passphrase, the integrity of every byte, and every path inside it. A file that fails changes nothing. It then saves the current state as `backups/pre-restore-<time>.tprbak`, encrypted with the same passphrase, and applies the backup on the next start. A backup taken on another machine works: its auth folder moves to this machine's data folder.

Restore replaces your accounts with the ones in the backup. Refresh tokens rotate, so an old backup's logins may no longer work and may need `tpr login` again.

## Rotating keys

- **Client keys:** add and remove them in the panel. Clients then need `tpr connect` with the new key.
- **Management key:** set a new one in the panel. Delete `credentials.txt` if it holds the old one.

## Uninstall

```sh
tpr uninstall          # remove the service and PATH entry, keep your data
tpr uninstall --purge  # also delete the data folder, after asking
```

On a client, run `tpr connect --undo` first. On Windows, a running `tpr.exe` cannot delete itself, so `--purge` may ask you to delete the folder by hand.

`tpr unexpose` removes the tailnet mapping if you published one.

## Building from source

```sh
make panel          # build the panel and copy it for embedding
make build          # ./tpr for this machine
make check          # gofmt, go vet and go test, plus the panel's type check and tests
make release-local  # every platform, plus checksums and install scripts, in dist/
```

## Docker

`docker compose up -d` runs a hub with its data in `./tpr-data`. It publishes port 8317 on the host's loopback only. Through a published port every browser counts as remote, so set `MANAGEMENT_PASSWORD` to use the panel. Self-update does not apply here: pull a newer image instead.

## Releasing

Push a `vX.Y.Z` tag. `release.yml` tests, builds the panel, cross-compiles every platform with `CGO_ENABLED=0`, and publishes a GitHub Release with the binaries, a gzip copy of each, `checksums.txt` and the install scripts. The file names (`tpr-<os>-<arch>[.exe]` and the same with `.gz`) are what the updater and the install scripts look for, so do not change them. The install scripts and the updater download the `.gz` when the release has one. Keep the plain files, because installs from before v1.0.1 update from them.
