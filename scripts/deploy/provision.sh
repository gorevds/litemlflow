#!/bin/bash
# scripts/deploy/provision.sh
#
# First-time provisioning of LiteMLflow on a fresh host. Idempotent:
# safe to re-run; existing user/dirs/units are not clobbered.
#
# Run as root on the server. Expects:
#   /tmp/litemlflow         — the binary, scp'd separately by the deploy step
#   /tmp/lmf.service        — the systemd unit
#   /tmp/lmf.gorev.space.nginx — the nginx server block
#
# After this script:
#   • user `lmf` exists, owns /opt/lmf and /var/lib/lmf
#   • binary installed at /opt/lmf/litemlflow
#   • systemd unit lmf.service enabled and started
#   • nginx site available; reload not yet performed (cert needed)

set -euo pipefail

BINARY=/tmp/litemlflow
UNIT=/tmp/lmf.service
NGINX_SITE=/tmp/lmf.gorev.space.nginx

[ -f "$BINARY" ] || { echo "missing $BINARY" >&2; exit 2; }
[ -f "$UNIT" ]   || { echo "missing $UNIT"   >&2; exit 2; }
[ -f "$NGINX_SITE" ] || { echo "missing $NGINX_SITE" >&2; exit 2; }

# 1. Service user.
if ! id -u lmf >/dev/null 2>&1; then
    useradd --system --no-create-home --home /var/lib/lmf --shell /usr/sbin/nologin lmf
    echo "[ok] created user lmf"
else
    echo "[skip] user lmf already exists"
fi

# 2. Directories.
install -d -o lmf -g lmf -m 0755 /opt/lmf
install -d -o lmf -g lmf -m 0750 /var/lib/lmf

# 3. Binary install (atomic via rename).
install -o lmf -g lmf -m 0755 "$BINARY" /opt/lmf/litemlflow.new
mv -f /opt/lmf/litemlflow.new /opt/lmf/litemlflow
echo "[ok] installed /opt/lmf/litemlflow ($( /opt/lmf/litemlflow version ))"

# 4. systemd unit.
install -m 0644 "$UNIT" /etc/systemd/system/lmf.service
systemctl daemon-reload
systemctl enable lmf >/dev/null
systemctl restart lmf
sleep 1
systemctl is-active --quiet lmf && echo "[ok] lmf.service active" || {
    echo "[fail] lmf.service did not start; logs:"
    journalctl -u lmf --no-pager -n 30
    exit 1
}

# 5. nginx site (don't reload yet — needs cert from certbot).
install -m 0644 "$NGINX_SITE" /etc/nginx/sites-available/lmf.gorev.space
ln -sf /etc/nginx/sites-available/lmf.gorev.space /etc/nginx/sites-enabled/lmf.gorev.space
mkdir -p /var/www/certbot
nginx -t
echo "[ok] nginx site staged; run certbot next"

# 6. Health check via loopback.
sleep 1
curl -fsS http://127.0.0.1:5050/healthz | grep -q '"ok":true' && echo "[ok] /healthz responsive on 127.0.0.1:5050" || {
    echo "[fail] healthz not responding"; exit 1;
}
