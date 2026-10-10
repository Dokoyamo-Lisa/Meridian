#!/usr/bin/env bash
# install-panel.sh - install or upgrade the Rosélune panel on THIS host from a GitHub release.
# Run it as root on the panel's host:
#
#   sudo bash install-panel.sh --domain panel.example.com [--email you@example.com]
#   sudo bash install-panel.sh --listen 127.0.0.1:8080        # behind your own TLS reverse proxy
#   sudo bash install-panel.sh --upgrade                      # newest release, settings kept
#   add --version 0.4.0 to any of them for a particular release
#
# It downloads the release for this CPU over HTTPS, checks its SHA-256 against the release's
# SHA256SUMS (and stops if they differ), unpacks it into a temporary directory and runs the
# release's own install-panel.sh with the same options. Nothing is installed if any check fails.
set -euo pipefail

REPO="${MERIDIAN_REPO:-Dokoyamo-Lisa/Meridian}"
VERSION=""
PASS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) PASS+=("$1"); shift ;;
  esac
done

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "run as root: sudo bash $0 ..."
case "$(uname -s)" in Linux) ;; *) die "the panel runs on Linux (this is $(uname -s))" ;; esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU $(uname -m) - Rosélune runs on amd64 and arm64" ;;
esac
for c in curl sha256sum tar systemctl; do
  command -v "$c" >/dev/null 2>&1 || die "$c is missing - on Debian/Ubuntu: apt-get install -y curl coreutils tar (systemd is required)"
done

get() { curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --connect-timeout 15 "$@"; }

if [ -z "$VERSION" ]; then
  VERSION="$(get "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)" \
    || die "could not ask GitHub for the newest release of $REPO - check this host can reach https://api.github.com, or pass --version"
fi
VERSION="${VERSION#v}"
echo "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || die "no usable release version found (got '$VERSION') - pass --version, e.g. --version 0.4.0"

NAME="meridian-$VERSION-linux-$ARCH"
BASE="https://github.com/$REPO/releases/download/v$VERSION"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

say "Downloading Rosélune $VERSION for $ARCH"
get -o SHA256SUMS "$BASE/SHA256SUMS" || die "release v$VERSION has no SHA256SUMS at $BASE - is the version right?"
get -o "$NAME.tar.gz" "$BASE/$NAME.tar.gz" || die "download failed: $BASE/$NAME.tar.gz"
grep " $NAME.tar.gz\$" SHA256SUMS > want.sum || die "SHA256SUMS does not list $NAME.tar.gz"
sha256sum -c want.sum >/dev/null 2>&1 || die "checksum mismatch for $NAME.tar.gz - the file is not the one released; do not install it"
say "Checksum OK"

tar xzf "$NAME.tar.gz"
cd "$NAME"
./install-panel.sh ${PASS[@]+"${PASS[@]}"}
