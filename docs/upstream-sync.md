# Upstream sync

Token Proxy Router keeps upstream merges cheap by touching upstream files only with small hooks. This file lists each one, so a merge conflict is easy to recognise and resolve.

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
