#!/usr/bin/env bash
# scripts/deploy/deploy.sh — build and roll out a new litemlflow binary on
# this host, rolling back automatically if /healthz does not come up.
#
#   sudo scripts/deploy/deploy.sh
#
# The build runs as the invoking (sudo) user so bin/ and the Go cache are not
# root-owned. Set GO=/path/to/go if `go` is not on root's PATH.
#
# Caveat: if the new binary applied a schema migration on startup, the old
# binary may not accept the newer DB after rollback; see `litemlflow rollback`.
set -euo pipefail

DEST=/opt/lmf/litemlflow
UNIT=lmf.service
HEALTH=http://127.0.0.1:5050/healthz
KEEP_BACKUPS=5

[ "$(id -u)" -eq 0 ] || { echo "run with sudo" >&2; exit 1; }
cd "$(dirname "$0")/../.."

BUILD_USER=${SUDO_USER:-root}
BUILD_HOME=$(getent passwd "$BUILD_USER" | cut -d: -f6)
GO_BIN=${GO:-$(command -v go || echo "$BUILD_HOME/sdk/go/bin/go")}
sudo -u "$BUILD_USER" -H make build GO="$GO_BIN"

healthy() {
    for _ in $(seq 1 30); do
        curl -fsS --max-time 2 "$HEALTH" 2>/dev/null | grep -q '"ok":true' && return 0
        sleep 1
    done
    return 1
}

# Stage next to the target (same filesystem) so the final mv is atomic.
install -o root -g root -m 0755 bin/litemlflow "$DEST.new"
"$DEST.new" version

BACKUP=
if [ -f "$DEST" ]; then
    BACKUP="$DEST.bak-$(date +%Y%m%d-%H%M%S)"
    cp -p "$DEST" "$BACKUP"
fi
mv -f "$DEST.new" "$DEST"
systemctl restart "$UNIT" || true   # failure is handled by the health check

if healthy; then
    echo "[ok] deployed; $HEALTH healthy"
    ls -1t "$DEST".bak-* 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -f
    exit 0
fi

echo "[fail] health check failed after deploy:" >&2
journalctl -u "$UNIT" --no-pager -n 30 >&2 || true
[ -n "$BACKUP" ] || { echo "no backup to roll back to" >&2; exit 1; }
cp -p "$BACKUP" "$DEST.new" && mv -f "$DEST.new" "$DEST"
systemctl restart "$UNIT" || true
if healthy; then
    echo "[rolled back] restored $BACKUP" >&2
else
    echo "[fail] rollback to $BACKUP is ALSO unhealthy" >&2
fi
exit 1
