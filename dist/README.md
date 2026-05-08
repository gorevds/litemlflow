# LiteMLflow — Distribution Artifacts

This directory contains packaging recipes for distributing LiteMLflow via
system package managers and Kubernetes. No Go compilation happens here; every
recipe consumes the pre-built binaries produced by the GitHub Actions release
pipeline (`.github/workflows/release.yml`).

## Directory layout

```
dist/
  homebrew/Formula/litemlflow.rb   Homebrew tap formula
  debian/                          Debian source package tree
  rpm/litemlflow.spec              RPM spec + bundled service file
  rpm/litemlflow.service
  snap/snapcraft.yaml              Snap package definition
  helm/litemlflow/                 Helm chart (Kubernetes)
```

## Homebrew

**Who builds it:** A human release manager (or a CI job on the
`litemlflow/homebrew-tap` repo) after the GitHub release is published.

**Steps:**

1. Download the four release binaries and their SHA256 hashes from
   `https://github.com/gorevds/litemlflow/releases/tag/vVERSION`.
2. Replace the four `PLACEHOLDER_*` values in
   `dist/homebrew/Formula/litemlflow.rb` with the real hashes.
3. Update `version` to match.
4. Copy the formula into the tap repository and open a PR.

Alternatively, use `brew bump-formula-pr` if the tap is already published.

## Debian (.deb)

**Who builds it:** the `make dist-deb` target (wraps `dpkg-buildpackage`).

**Steps:**

1. Run the release pipeline to produce the binary:
   `litemlflow-vVERSION-linux-amd64` (or arm64 for an arm build host).
2. Rename or symlink the binary to `litemlflow` and place it at the root of a
   build directory that also contains the `debian/` tree from this folder.
3. Run:
   ```
   dpkg-buildpackage -b -us -uc
   ```
   The `.deb` is deposited one level above the build directory.
4. Sign with `debsign` and upload to your APT repository.

The `debian/rules` file installs the binary from the working directory, so the
binary must be named `litemlflow` (without version suffix) in that directory.

`make dist-deb` automates steps 2-3; set `BINARY` to override the binary path.

## RPM

**Who builds it:** the `make dist-rpm` target (wraps `rpmbuild`).

**Steps:**

1. Create the rpmbuild tree: `mkdir -p ~/rpmbuild/{SOURCES,SPECS}`.
2. Copy `dist/rpm/litemlflow.service` to `~/rpmbuild/SOURCES/`.
3. Download the release binary (linux-x86_64 or linux-aarch64) to
   `~/rpmbuild/SOURCES/litemlflow-vVERSION-rc1-linux-x86_64`.
4. Update the `Source0` URL in `dist/rpm/litemlflow.spec` for the version.
5. Run:
   ```
   rpmbuild -bb dist/rpm/litemlflow.spec
   ```
6. Sign with `rpm --addsign` and upload to your DNF/YUM repository.

`make dist-rpm` automates steps 1-5.

## Snap

**Who builds it:** Snapcraft Cloud (https://build.snapcraft.io) or local
`snapcraft` CLI.

**Steps:**

1. Update `version` in `dist/snap/snapcraft.yaml` to match the release.
2. Update the `source` URL under `parts.litemlflow` to point to the correct
   release binary.
3. Run `snapcraft` from the `dist/snap/` directory (requires `snapd` and
   `snapcraft` installed), or push to the Snap Store via:
   ```
   snapcraft remote-build
   ```

## Helm

**Who builds it:** the release manager via `helm package` and `helm push`.

**Steps:**

1. Update `appVersion` in `dist/helm/litemlflow/Chart.yaml` to match the
   release (e.g. `0.4.0-rc1`). Bump `version` for chart-only changes.
2. Update `image.tag` default in `values.yaml` if needed.
3. Lint: `make dist-helm-lint`
4. Dry-run render: `make dist-helm-template`
5. Package: `helm package dist/helm/litemlflow/ --destination dist/`
6. Push to OCI registry:
   ```
   helm push litemlflow-0.1.0.tgz oci://ghcr.io/litemlflow/charts
   ```

## Makefile targets

| Target | Description |
|--------|-------------|
| `make dist-helm-lint` | Run `helm lint` on the chart |
| `make dist-helm-template` | Dry-run render to `/tmp/render.yaml` |
| `make dist-deb` | Build a .deb from a pre-built binary |
| `make dist-rpm` | Build an .rpm from a pre-built binary |

## Secrets and credentials

The Helm chart renders auth credentials into a Kubernetes Secret from Helm
values. For production, use an external secret manager (e.g. External Secrets
Operator or Vault Agent) and avoid passing `auth.passHash` via `--set`.
