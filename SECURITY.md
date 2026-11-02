# Security

Token Proxy Router holds OAuth logins for your AI accounts, so treat a hub like a password store.

## Reporting a problem

Please report vulnerabilities privately through
[GitHub security advisories](https://github.com/Danielw412/token-proxy-router/security/advisories/new),
not in a public issue. Do not include real tokens, keys or logs containing them.

## How a hub protects you

- It binds to loopback by default. Tailnet access goes through `tailscale serve`, which keeps it
  off the public internet.
- Client API keys only reach the model API. The management key is stored hashed, and it is needed
  for every panel and management route.
- The data folder, config and credentials are readable by the owner only.
- Backups are encrypted with a passphrase using scrypt and AES-256-GCM.
- Updates are verified against the release's SHA-256 checksums. This protects against a corrupted
  or swapped download, not against a compromise of the GitHub account that publishes releases,
  because the checksums come from the same place.

## What you should do

- Do not publish the server port to a network you do not trust, or use `tailscale funnel` on it.
- Treat `~/.token-proxy-router/credentials.txt` as a secret, and run `tpr creds --forget` once you
  have saved your keys somewhere safe.
- Use a separate client key per machine, so you can revoke one without touching the others.
- Identity sign-in is off by default. Before turning it on, read
  [what it checks](docs/setup.md#sign-in-with-your-tailnet-identity).
