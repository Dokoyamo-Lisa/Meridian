#!/bin/sh
# Rosélune agent installer (the program and its service keep the name meridian-agent).
# POSIX sh: runs under bash, dash and busybox (Alpine Linux).
#
# Copy the exact command from the panel (server page): it checks this script's checksum before
# running it, and this script checks the agent binary against checksums written into it by the
# panel - so nothing can be swapped on the way, even over plain HTTP.
#
#   install:    MERIDIAN_TOKEN=<token> sh meridian-install.sh [--api-port 50000]
#               (--token <token> works too, but puts the token where other users can read it)
#   uninstall:  meridian-agent uninstall
set -eu

PANEL="__PANEL_URL__"
SHA_AMD64="__SHA_AMD64__"
SHA_ARM64="__SHA_ARM64__"
TOKEN="${MERIDIAN_TOKEN:-}"
API_PORT=""
ACTION="install"
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="${2:-}"; shift 2 ;;
    --panel) PANEL="${2:-}"; shift 2 ;;
    --api-port) API_PORT="${2:-}"; shift 2 ;;
    --uninstall) ACTION="uninstall"; shift ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "run as root (sudo sh $0 ...)"
if [ -d /run/systemd/system ]; then
  :
elif [ -d /run/openrc ] && [ -x /sbin/openrc-run ]; then
  :
else
  die "this server runs neither systemd nor OpenRC - the agent needs one of them"
fi
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required (coreutils or busybox)"

# fetch URL FILE: curl where there is one, otherwise wget (busybox's on Alpine)
fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --proto '=http,https' -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "curl or wget is required"
  fi
}

BIN=/usr/local/bin/meridian-agent

if [ "$ACTION" = "uninstall" ]; then
  if [ -x "$BIN" ]; then "$BIN" uninstall; else say "meridian-agent is not installed"; fi
  exit 0
fi

[ -n "$TOKEN" ] || die "missing --token (copy the full command from the panel)"
PANEL="${PANEL%/}"
if [ -n "$API_PORT" ]; then
  case "$API_PORT" in
    *[!0-9]*) die "--api-port must be a number such as 50000" ;;
  esac
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64; WANT="$SHA_AMD64" ;;
  aarch64|arm64) ARCH=arm64; WANT="$SHA_ARM64" ;;
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac
[ "${#WANT}" -eq 64 ] || die "the panel has no agent build for $ARCH"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "Downloading meridian-agent ($ARCH) from $PANEL"
fetch "$PANEL/agent/v1/download/meridian-agent-linux-$ARCH" "$TMP/agent" || die "download failed"
GOT="$(sha256sum "$TMP/agent" | cut -d' ' -f1)"
[ "$GOT" = "$WANT" ] || die "the downloaded agent does not match its checksum - copy a fresh command from the panel"
mkdir -p "$(dirname "$BIN")"
cp "$TMP/agent" "$BIN.new"
chmod 0755 "$BIN.new"
mv -f "$BIN.new" "$BIN"

# the token goes through the environment, not the command line (which other users can read)
if [ -n "$API_PORT" ]; then
  MERIDIAN_TOKEN="$TOKEN" exec "$BIN" install --panel "$PANEL" --api-port "$API_PORT"
fi
MERIDIAN_TOKEN="$TOKEN" exec "$BIN" install --panel "$PANEL"
