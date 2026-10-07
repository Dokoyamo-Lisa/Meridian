#!/usr/bin/env bash
# api.sh METHOD PATH [BODY] [CONTENT_TYPE] - call the Meridian API.
#
#   export MERIDIAN_URL=https://panel.example.com
#   export MERIDIAN_TOKEN=mrd_...        # sudo -u meridian meridian token  (on the panel's host)
#   api.sh GET /api/overview
#   api.sh PUT /api/settings '{"site_title":"Acme Net"}'
#   api.sh POST /api/users @users.json
#   api.sh PUT /api/settings/logo @logo.svg image/svg+xml
#
# BODY is JSON text, or @FILE to send a file. Prints the answer. Exits 1 and prints the panel's own
# explanation on any error, so a failed step is never mistaken for a done one.
set -euo pipefail

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
[ $# -ge 2 ] || { echo "usage: api.sh METHOD PATH [JSON | @FILE] [CONTENT_TYPE]" >&2; exit 2; }
url="${MERIDIAN_URL:-}"
tok="${MERIDIAN_TOKEN:-}"
[ -n "$url" ] || die "set MERIDIAN_URL first, e.g. export MERIDIAN_URL=https://panel.example.com"
[ -n "$tok" ] || die "set MERIDIAN_TOKEN first (create one on the panel's host: sudo -u meridian meridian token --name agent)"
case "$url" in
  https://*|http://127.0.0.1:*|http://127.0.0.1|http://localhost:*|http://localhost) ;;
  *) die "MERIDIAN_URL must start with https:// - a token must never travel unencrypted (plain http only to 127.0.0.1)" ;;
esac
method="$1" path="$2" body="${3:-}" ctype="${4:-application/json}"
case "$path" in /*) ;; *) die "PATH must start with /, e.g. /api/servers" ;; esac

args=(-sS --connect-timeout 15 --max-time 120 -X "$method" -o "$(mktemp)" -w '%{http_code} %{filename_effective}')
if [ -n "$body" ]; then
  args+=(-H "Content-Type: $ctype" --data-binary "$body")
fi
# the token goes in through a file descriptor, never on the command line (other users can read those)
res="$(curl -K <(printf 'header = "Authorization: Bearer %s"\n' "$tok") "${args[@]}" "${url%/}$path")" || die "could not reach $url - is the address right and the panel running? (curl $url/healthz should say ok)"
code="${res%% *}"
file="${res#* }"
out="$(cat "$file")"
rm -f "$file"
if [ "$code" -ge 400 ] || [ "$code" -lt 200 ]; then
  msg="$(printf '%s' "$out" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("error", ""))' 2>/dev/null ||
    printf '%s' "$out" | sed -n 's/.*"error": *"\(.*\)".*/\1/p')"
  die "$method $path answered $code: ${msg:-$out}"
fi
printf '%s\n' "$out"
