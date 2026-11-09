# Upstream versions

Token Proxy Router is a fork. This file records the upstream versions the current
source is based on, and the release workflow stamps them into every binary.
`scripts/sync-upstream.sh` updates this table. See `docs/upstream-sync.md`.

| Component | Upstream repository | Version |
| --- | --- | --- |
| Server | router-for-me/CLIProxyAPI | v8.0.61 |
| Panel | router-for-me/Cli-Proxy-API-Management-Center | v1.27.3 |

## Picked ahead of a release

Upstream commits taken before the release that contains them. When the server moves
past these, drop them from this list. A commit from a pull request that upstream closes
without merging stays until upstream fixes the problem another way.

| Commit | Why |
| --- | --- |
| `2c5b01f4` fix(claude): disable default upstream connection reuse | v8.0.60 and v8.0.61 can hang the first Claude stream after a few idle minutes ([#7357](https://github.com/router-for-me/CLIProxyAPI/issues/7357)). |
| `4d421d66` test(api): wait for the websocket handler before TempDir cleanup | From open pull request [#7354](https://github.com/router-for-me/CLIProxyAPI/pull/7354). In v8.0.61, `TestServerResponsesWebsocketReauthorizesEachTurn` often fails while removing its temp folder, which fails CI. Test only. |
