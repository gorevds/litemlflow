# Sunset distribution channels

These channels are no longer maintained as of v1.2.

| Path | Status |
|---|---|
| `snap/` | unpinned at v0.4.0-rc1; nobody using it |
| `homebrew/` | formula required hand-edits per release; broken since v0.4 |
| `rpm/` | no signed YUM repo; build target only |
| `debian/` | no signed APT repo; build target only |
| `terraform/` | provider scaffolded but never published to Terraform Registry |

The decision to sunset followed the v1.2 deep-review findings (see
`docs/reports/2026-05-08-deep-review.md`): two of six independent agents
flagged distribution sprawl, and CI was only exercising 2 of the 8 channels.

**Supported channels going forward:**

1. **Docker** — `docker pull ghcr.io/gorevds/litemlflow:<tag>`
2. **Helm** — see `dist/helm/`
3. **K8s operator** — see `operator/`
4. **Raw binary** — download from GitHub Releases:
   ```bash
   curl -fsSL -o /usr/local/bin/litemlflow \
     https://github.com/gorevds/litemlflow/releases/latest/download/litemlflow-linux-amd64
   chmod +x /usr/local/bin/litemlflow
   ```

Files in this directory are kept for reference; reviving any of them in v2.x
requires owning the publishing pipeline (signed APT repo, Snap store account,
Terraform Registry namespace, etc.).
