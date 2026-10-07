#!/usr/bin/env bash
# Meridian agent installer.
#
# Copy the exact command from the panel (server page): it checks this script's checksum before
# running it, and this script checks the agent binary against checksums written into it by the
# panel - so nothing can be swapped on the way, even over plain HTTP.
#
#   install:    bash meridian-install.sh --token <token>
#   uninstall:  meridian-agent uninstall
set -euo pipefail

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

[ "$(id -u)" = "0" ] || die "run as root (sudo bash $0 ...)"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required (coreutils)"

BIN=/usr/local/bin/meridian-agent

if [ "$ACTION" = "uninstall" ]; then
  if [ -x "$BIN" ]; then "$BIN" uninstall; else say "meridian-agent is not installed"; fi
  exit 0
fi

[ -n "$TOKEN" ] || die "missing --token (copy the full command from the panel)"
PANEL="${PANEL%/}"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64; WANT="$SHA_AMD64" ;;
  aarch64|arm64) ARCH=arm64; WANT="$SHA_ARM64" ;;
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac
[ "${#WANT}" -eq 64 ] || die "the panel has no agent build for $ARCH"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "Downloading meridian-agent ($ARCH) from $PANEL"
curl -fsSL --retry 3 --proto '=http,https' -o "$TMP/agent" "$PANEL/agent/v1/download/meridian-agent-linux-$ARCH" || die "download failed"
GOT="$(sha256sum "$TMP/agent" | cut -d' ' -f1)"
[ "$GOT" = "$WANT" ] || die "the downloaded agent does not match its checksum - copy a fresh command from the panel"
install -m 0755 "$TMP/agent" "$BIN.new"
mv -f "$BIN.new" "$BIN"

# the token goes through the environment, not the command line (which other users can read)
[ -z "$API_PORT" ] || echo "$API_PORT" | grep -Eq '^[0-9]{4,5}$' || die "--api-port must be a number such as 50000"
MERIDIAN_TOKEN="$TOKEN" exec "$BIN" install --panel "$PANEL" ${API_PORT:+--api-port "$API_PORT"}
