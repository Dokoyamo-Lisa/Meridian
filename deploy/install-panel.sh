#!/usr/bin/env bash
# Meridian panel installer. Run it as root from an unpacked release directory
# (meridian-<version>-linux-<arch>/):
#
#   sudo ./install-panel.sh --domain panel.example.com [--email you@example.com]
#   sudo ./install-panel.sh --listen 127.0.0.1:8080   # plain HTTP behind your own TLS proxy (or :8080)
#   sudo ./install-panel.sh --upgrade                 # replace the binaries, keep everything else
#   sudo ./install-panel.sh --uninstall               # remove the service (the data stays)
#   ... --database sqlite                             # a new panel on SQLite instead of PostgreSQL
#
# With --domain the panel gets a Let's Encrypt certificate by itself (ports 80 and 443 must be
# reachable; using it accepts Let's Encrypt's terms). The panel runs as the unprivileged user
# "meridian" in a hardened systemd unit; its data lives in /var/lib/meridian. A new panel keeps its
# data in PostgreSQL (installed from the distribution's packages, reached over its local socket
# without a password); an existing panel keeps the database it has - meridian db to-postgres moves
# a SQLite panel's data later.
set -euo pipefail

DOMAIN=""
EMAIL=""
LISTEN=""
ACTION="install"
DATABASE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --email) EMAIL="${2:-}"; shift 2 ;;
    --listen) LISTEN="${2:-}"; shift 2 ;;
    --database) DATABASE="${2:-}"; shift 2 ;;
    --upgrade) ACTION="upgrade"; shift ;;
    --uninstall) ACTION="uninstall"; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "run as root (sudo $0 ...)"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN=/usr/local/bin/meridian
LIB=/usr/local/lib/meridian
DATA=/var/lib/meridian
ETC=/etc/meridian
UNIT=/etc/systemd/system/meridian.service
UPDATER=/etc/systemd/system/meridian-update

if [ "$ACTION" = "uninstall" ]; then
  systemctl disable --now meridian meridian-update.path 2>/dev/null || true
  rm -f "$UNIT" "$UPDATER.path" "$UPDATER.service" "$BIN"
  rm -rf "$LIB"
  systemctl daemon-reload
  say "Meridian panel removed. Its data is still in $DATA and $ETC (and, if it used PostgreSQL, in the database meridian) - delete them yourself if you no longer need them."
  exit 0
fi

[ -x "$HERE/meridian" ] || die "run this script from the unpacked release directory (meridian binary not found)"
[ -f "$HERE/agent/meridian-agent-linux-amd64" ] && [ -f "$HERE/agent/meridian-agent-linux-arm64" ] \
  || die "the agent binaries are missing from $HERE/agent"
"$HERE/meridian" version >/dev/null 2>&1 || die "this release does not match the server's CPU architecture"

if [ -n "$DOMAIN" ]; then
  echo "$DOMAIN" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$' || die "--domain must be a plain host name"
fi
[ -n "$EMAIL" ] && [ -z "$DOMAIN" ] && die "--email only matters with --domain (it is the Let's Encrypt contact)"
[ -n "$LISTEN" ] && [ -n "$DOMAIN" ] && die "use either --domain (HTTPS on 443) or --listen (plain HTTP), not both"
case "$DATABASE" in ""|postgres|sqlite) ;; *) die "--database is postgres or sqlite" ;; esac

# checks before anything changes: the ports are free (or ours), and the domain points here
port_user() { ss -Hltnp "sport = :$1" 2>/dev/null | head -n1; }
if [ "$ACTION" = "install" ] && command -v ss >/dev/null 2>&1; then
  ports="${LISTEN:-:8080}"; ports="${ports##*:}"
  [ -n "$DOMAIN" ] && ports="80 443"
  for p in $ports; do
    used="$(port_user "$p")"
    if [ -n "$used" ] && ! echo "$used" | grep -q '"meridian"'; then
      die "port $p is already in use: $(echo "$used" | grep -o 'users:.*') - stop that program or choose another --listen port"
    fi
  done
fi
if [ "$ACTION" = "install" ] && [ -n "$DOMAIN" ]; then
  resolved="$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')"
  [ -n "$resolved" ] || die "$DOMAIN does not resolve yet - create its DNS A record pointing at this server first"
  mine="$(curl -4 -fsS --proto '=https' --max-time 8 https://1.1.1.1/cdn-cgi/trace 2>/dev/null | sed -n 's/^ip=//p')"
  if [ -n "$mine" ] && [ "$resolved" != "$mine" ]; then
    die "$DOMAIN points to $resolved, but this server's public address is $mine - point the DNS A record here (with no CDN proxy in front: the certificate check needs to reach this server) and run this again. Behind your own TLS proxy, use --listen 127.0.0.1:8080 instead of --domain"
  fi
fi

say "Installing Meridian $("$HERE/meridian" version | awk '{print $2}')"
id meridian >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin meridian
install -d -m 0700 -o meridian -g meridian "$DATA"
install -d -m 0755 "$LIB/agent" "$ETC"
install -m 0755 "$HERE/meridian" "$BIN.new" && mv -f "$BIN.new" "$BIN"
install -m 0644 "$HERE/agent/meridian-agent-linux-amd64" "$HERE/agent/meridian-agent-linux-arm64" "$LIB/agent/"

# The database: a new panel gets PostgreSQL (unless --database sqlite); one that has data keeps its own.
setup_postgres() {
  if ! command -v psql >/dev/null 2>&1; then
    say "Installing PostgreSQL"
    if command -v apt-get >/dev/null 2>&1; then
      DEBIAN_FRONTEND=noninteractive apt-get install -y -q postgresql >/dev/null || return 1
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y -q postgresql-server >/dev/null || return 1
      [ -d /var/lib/pgsql/data/base ] || postgresql-setup --initdb >/dev/null || return 1
    else
      return 1
    fi
  fi
  systemctl enable --now postgresql >/dev/null 2>&1 || return 1
  for _ in $(seq 1 30); do
    runuser -u postgres -- psql -Atqc 'SELECT 1' >/dev/null 2>&1 && break
    sleep 1
  done
  # the role and the database: the panel's own system user signs in over the local socket (peer)
  runuser -u postgres -- psql -Atqc "SELECT 1 FROM pg_roles WHERE rolname = 'meridian'" | grep -q 1 \
    || runuser -u postgres -- psql -qc "CREATE ROLE meridian LOGIN" >/dev/null || return 1
  runuser -u postgres -- psql -Atqc "SELECT 1 FROM pg_database WHERE datname = 'meridian'" | grep -q 1 \
    || runuser -u postgres -- psql -qc "CREATE DATABASE meridian OWNER meridian ENCODING 'UTF8' TEMPLATE template0" >/dev/null || return 1
  umask 077
  echo "postgres://meridian@/meridian?host=/var/run/postgresql" > "$DATA/database.url"
  umask 022
  chown meridian:meridian "$DATA/database.url"
}
if [ "$ACTION" = "install" ] && [ ! -e "$DATA/meridian.db" ] && [ ! -e "$DATA/database.url" ]; then
  if [ "$DATABASE" != "sqlite" ]; then
    if setup_postgres; then
      say "The panel keeps its data in PostgreSQL (database meridian)"
    else
      [ "$DATABASE" = "postgres" ] && die "PostgreSQL could not be set up (this installer knows apt and dnf) - install it yourself, or run again with --database sqlite"
      say "PostgreSQL could not be set up here - the panel keeps its data in SQLite ($DATA/meridian.db); meridian db to-postgres moves it later"
    fi
  fi
elif [ -n "$DATABASE" ] && [ "$ACTION" = "install" ]; then
  echo "This panel has its data already: it keeps its database (meridian db status shows which; meridian db to-postgres / to-sqlite move it)."
fi

if [ "$ACTION" = "install" ] || [ ! -f "$ETC/meridian.env" ]; then
  umask 077
  {
    echo "# Meridian panel settings - restart after changes: systemctl restart meridian"
    echo "MERIDIAN_DATA=$DATA"
    echo "MERIDIAN_AGENT_DIR=$LIB/agent"
    if [ -n "$DOMAIN" ]; then
      echo "MERIDIAN_DOMAIN=$DOMAIN"
      [ -n "$EMAIL" ] && echo "MERIDIAN_EMAIL=$EMAIL"
    else
      echo "MERIDIAN_LISTEN=${LISTEN:-:8080}"
    fi
    echo "# Reverse proxies whose X-Forwarded-For may be believed, comma-separated CIDRs:"
    echo "#MERIDIAN_TRUSTED_PROXIES=127.0.0.1/32"
  } > "$ETC/meridian.env"
  umask 022
fi

cat > "$UNIT" <<'EOF'
[Unit]
Description=Meridian panel
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=meridian
Group=meridian
EnvironmentFile=/etc/meridian/meridian.env
ExecStart=/usr/local/bin/meridian serve
Restart=always
RestartSec=3
LimitNOFILE=65536
# ports 80/443 for automatic HTTPS, nothing else
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/meridian
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
EOF

# The updater: when the panel leaves a release it downloaded and checked in $DATA/update, root checks
# it again (its signature against the key built into the installed binary, its checksum, that it is
# newer) and installs it with that release's own install-panel.sh --upgrade (Settings > Updates).
cat > "$UPDATER.path" <<'UNIT_EOF'
[Unit]
Description=Meridian panel updates (starts the updater when the panel asks for one)

[Path]
PathExists=/var/lib/meridian/update/request.json
Unit=meridian-update.service

[Install]
WantedBy=multi-user.target
UNIT_EOF
cat > "$UPDATER.service" <<'UNIT_EOF'
[Unit]
Description=Meridian panel update (installs a signed release the panel downloaded)

[Service]
Type=oneshot
EnvironmentFile=/etc/meridian/meridian.env
ExecStart=/usr/local/bin/meridian update-apply
TimeoutStartSec=15min
PrivateTmp=true
UNIT_EOF

systemctl daemon-reload
systemctl enable --now meridian-update.path >/dev/null 2>&1 || die "could not start the updater (meridian-update.path)"
if [ "$ACTION" = "upgrade" ]; then
  systemctl restart meridian
  say "Meridian upgraded. Servers keep running; upgrade their agents in Settings > Updates (Upgrade all agents) when convenient."
  exit 0
fi
if systemctl is-active --quiet meridian; then
  systemctl restart meridian      # a reinstall: start again with the new settings (servers keep running)
fi
systemctl enable --now meridian

say "Waiting for the panel to answer"
health="http://127.0.0.1:${LISTEN##*:}/healthz"
[ -z "$LISTEN" ] && health="http://127.0.0.1:8080/healthz"
[ -n "$DOMAIN" ] && health=""
ok=""
for _ in $(seq 1 30); do
  if [ -n "$health" ]; then
    curl -fsS --max-time 2 "$health" >/dev/null 2>&1 && ok=1 && break
  else
    systemctl is-active --quiet meridian && ok=1 && break
  fi
  sleep 1
done
[ -n "$ok" ] || die "the panel did not start - see: journalctl -u meridian -n 50 --no-pager"

for _ in $(seq 1 10); do
  [ -f "$DATA/initial-admin.txt" ] && break
  sleep 1
done
echo
if [ -f "$DATA/initial-admin.txt" ]; then
  say "Sign in with:"
  sed 's/^/  /' "$DATA/initial-admin.txt"
  echo "  (this note is deleted after your first sign-in - change the password and turn on two-factor sign-in)"
else
  echo "The panel is running. If this is a reinstall, sign in with your existing account."
fi
echo
if [ -n "$DOMAIN" ]; then
  say "Open https://$DOMAIN"
else
  case "${LISTEN:-:8080}" in
    :*) say "Open http://<this server's address>${LISTEN:-:8080}" ;;
    *) say "Open http://${LISTEN}" ;;
  esac
  echo "Tip: serve the panel over HTTPS - reinstall with --domain, or put it behind a TLS reverse proxy."
fi
echo "Logs: journalctl -u meridian -f"
