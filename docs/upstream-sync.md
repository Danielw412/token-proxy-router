# Upstream sync

Token Proxy Router keeps upstream merges cheap by touching upstream files only with small hooks. This file lists each one, so a merge conflict is easy to recognise and resolve.

## How to merge

`UPSTREAM.md` records the versions in use. The `upstream-sync.yml` workflow runs weekly. It merges the latest upstream server and panel releases into a branch and runs the tests. A clean merge opens a pull request, failing tests open a draft pull request, and conflicts open an issue listing the files.

To do the same locally:

```sh
scripts/sync-upstream.sh --dry-run   # what would change
scripts/sync-upstream.sh             # merge into upstream-sync/server-<tag>-panel-<tag>
scripts/sync-upstream.sh --server v8.1.0 --panel v1.28.0   # exact versions
```

It needs a clean working tree. It exits 0 when merged, 2 on conflicts (the merge is aborted, so nothing is half done), and 3 when the merge is clean but tests fail. To resolve conflicts, run the merge it was attempting and fix the files by hand:

```sh
git fetch --no-tags https://github.com/router-for-me/CLIProxyAPI.git refs/tags/<tag>:refs/tags/upstream-<tag>
git merge upstream-<tag>
```

### Conflicts in `panel/`

`panel/` is a `git subtree` of the upstream panel, squashed, and we change it freely. Upstream's panel changes arrive with `git subtree merge --prefix=panel --squash panel-upstream-<tag>`.

Resolve in favour of what TPR does, then take what upstream added around it:

- Keep TPR's login page, theme, "Claude and Codex first" provider order, and the wizard, router, quota and connect pages.
- Take upstream's fixes to the shared API client and to pages TPR has not changed.
- When upstream reworks a page TPR changed heavily, keep TPR's version and port the fix by hand.

Then run `bun run type-check && bun run test` in `panel/`.

### Conflicts in Go code

Each hook below is one to a few lines marked `// TPR:`. Take upstream's version of the file, then put the hook back. If upstream moved the code a hook attaches to, attach it at the same point in the new code. Do not reformat or rename upstream code. Keep the Go module path.

To list everything that differs from upstream:

```sh
git diff --stat upstream-<tag> HEAD -- . ':!internal/tpr' ':!panel'
```

After a merge, run `make check`, and add any new upstream file you touched to the tables here.

## Setup, panel, update and tailnet

| Upstream file | Change |
| --- | --- |
| `cmd/server/main.go` | One import. At the top of `main`, `tprcli.Dispatch` runs `tpr` subcommands and `DefaultConfigPath` moves the default config to the data folder. `tprcli.BeforeConfigLoad` handles first run and a staged restore, in the local-config branch only, so the Postgres, Git, object store and Home modes are untouched. Three version lines say "Token Proxy Router". |
| `internal/managementasset/updater.go` | The `tprPanelEmbedded` constant. `StartAutoUpdater` returns early, and `EnsureLatestManagementHTML` only reports whether an override file exists, so there is no runtime download and no hosted fallback. Upstream's code stays below these checks, so merges stay clean. |
| `internal/api/server_management.go` | One import. `serveManagementControlPanel` calls `webui.Serve` first. `MANAGEMENT_STATIC_PATH` still selects a local file, which never gets overwritten. |
| `internal/api/server_routes.go` | `GET /` redirects to the panel instead of answering JSON. `tpr doctor` recognises an old CLIProxyAPI by upstream's JSON, so keep the two different. |
| `internal/api/server_management_v8.go` | Two imports. `tpr.Mount` attaches feature routes, and `GET /v8/management/tpr-public/session` is unauthenticated by design. |
| `internal/api/handlers/management/handler.go` | In `Middleware`, `h.tprIdentityAllowed` skips the key check only when every identity check passes. |
| `internal/api/handlers/management/tpr_identity.go` | New file with that one method. |
| `internal/api/handlers/management/config_basic.go` | `latest-version` looks at this repository's releases. |
| `internal/tui/i18n.go` | The TUI status bar says "Token Proxy Router". |
| `go.mod` | `github.com/pelletier/go-toml/v2` is now a direct dependency, for merging Codex's config. |

Not hooks, but replaced outright, so take ours in a conflict: `Dockerfile`, `docker-compose.yml`, `docker-compose.cluster.yml`, and `.github/workflows/`. `.github/scripts/refresh-model-catalogs.sh` stays upstream's, and the release workflow runs it. Model catalogs still come from upstream's sources.

After a merge, check that these still hold:

- `GET /healthz` still answers 200 with no key. The supervisor's health check and rollback depend on it.
- `-config` still parses as a flag, and upstream has no flag or subcommand named like a `tpr` command. `cmd/server` passes unknown arguments through, and `internal/tpr/cli` checks its list first.
- `config.SaveConfigPreserveCommentsUpdateNestedScalar` and `config.PrepareConfigFileForUpload` still exist. Migrate and restore use them.
- The v8 config layout still has `server.host`, `server.port`, `access.api-keys`, `management.secret-key`, `oauth.auth-dir` and `observability.usage.usage-statistics-enabled`. `setup.RenderConfig` writes those, and its test loads the result through upstream's loader.
- `Tailscale-User-Login` is still only trusted in `internal/tpr/tailscale`.

## Router, store, quota, alerts, health, key usage

Every hook is marked `// TPR:`, and `internal/tpr/router/README.md` explains how it attaches.

| Upstream file | Change |
| --- | --- |
| `internal/cmd/run.go` | Starts the native router and its features once a service is built: one import and one `defer` in each of `StartServiceWithPluginHost` and `StartServiceBackgroundWithPluginHost`. |
| `sdk/cliproxy/tpr_hooks.go` | New file. `Service.TPRAttachment` hands that hook the auth manager and a config reader. |
| `sdk/cliproxy/auth/tpr_hooks.go`, `tpr_hooks_test.go` | New files. `Manager.SetSchedulerFront` keeps the router in front of the plugin scheduler. |
| `sdk/cliproxy/auth/conductor.go` | One `schedulerFront` field on `Manager`. |
| `sdk/cliproxy/auth/conductor_selection.go` | One line in `SetPluginScheduler` chains through the front. |
| `AGENTS.md` | Lists the router's quota polls and alert deliveries among the allowed timeouts. |
| `go.mod`, `go.sum` | Adds `go.etcd.io/bbolt`. |

After a merge, check that these still hold:

- `SetPluginScheduler` is still the only place that writes `Manager.pluginScheduler` outside
  `tpr_hooks.go`. A new write would bypass the front.
- `pickViaPluginScheduler` still passes the session metadata that `session.Enrich` computes,
  `canonical_session_id` and `parent_session_id`. Claude's `metadata.user_id` reaches the router
  that way, because the scheduler never sees the request body.
- `usage.Record` still carries `ResponseHeaders` and `Fail`. The router and the quota history read
  rate limits from them.
- The Codex and Claude usage endpoints in `internal/tpr/router/host.go` still match the panel's,
  in `panel/src/utils/quota/constants.ts`.
