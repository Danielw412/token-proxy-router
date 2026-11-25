# Upstream versions

Token Proxy Router is a fork. This file records the upstream versions the current
source is based on, and the release workflow stamps them into every binary.
`scripts/sync-upstream.sh` updates this table. See `docs/upstream-sync.md`.

| Component | Upstream repository | Version |
| --- | --- | --- |
| Server | router-for-me/CLIProxyAPI | v8.1.3 |
| Panel | router-for-me/Cli-Proxy-API-Management-Center | v1.28.3 |

## Picked ahead of a release

Upstream commits taken before the release that contains them. When the server moves
past these, drop them from this list. A commit from a pull request that upstream closes
without merging stays until upstream fixes the problem another way.

None. v8.0.76 contains every commit picked for v1.0.6, and upstream fixed the websocket
test flake from the closed pull request #7354 in its own way.
