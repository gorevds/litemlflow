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
#   • user `lmf` exists and owns /var/lib/lmf; /opt/lmf and the binary are
#     root-owned (the service must not be able to replace its own binary)
#   • binary installed at /opt/lmf/litemlflow
#   • systemd unit lmf.service enabled and started
#   • nginx site enabled + validated if the TLS cert exists (reload left to
#     the operator); otherwise left disabled with instructions
#
# Subsequent upgrades: scripts/deploy/deploy.sh.

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
install -d -o root -g root -m 0755 /opt/lmf
install -d -o lmf -g lmf -m 0750 /var/lib/lmf

# 3. Binary install (atomic via rename).
install -o root -g root -m 0755 "$BINARY" /opt/lmf/litemlflow.new
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

# 5. nginx site. The server block references the Let's Encrypt cert
#    explicitly, and `nginx -t` fails on a missing cert, so only enable the
#    site once the cert exists — a broken site file would block every other
#    vhost on the next reload.
install -m 0644 "$NGINX_SITE" /etc/nginx/sites-available/lmf.gorev.space
if [ -f /etc/letsencrypt/live/lmf.gorev.space/fullchain.pem ]; then
    ln -sf /etc/nginx/sites-available/lmf.gorev.space /etc/nginx/sites-enabled/lmf.gorev.space
    nginx -t
    echo "[ok] nginx site enabled; run: systemctl reload nginx"
else
    echo "[todo] no cert yet: certbot certonly --nginx -d lmf.gorev.space"
    echo "       then re-run this script (or ln -s the site and reload nginx)"
fi

# 6. Health check via loopback (retry: startup runs migrations).
ok=
for _ in $(seq 1 20); do
    if curl -fsS --max-time 2 http://127.0.0.1:5050/healthz | grep -q '"ok":true'; then ok=1; break; fi
    sleep 0.5
done
[ -n "$ok" ] && echo "[ok] /healthz responsive on 127.0.0.1:5050" || {
    echo "[fail] healthz not responding"; journalctl -u lmf --no-pager -n 30; exit 1;
}
