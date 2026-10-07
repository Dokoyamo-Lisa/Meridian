#!/usr/bin/env bash
# check.sh - is the deployment healthy? Checks the panel answers, the token works, every server is
# online with its configuration applied, and lists anything the panel says needs a person.
# Exit 0 = healthy, 1 = something needs attention (printed).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
command -v python3 >/dev/null 2>&1 || { echo "error: python3 is needed (apt-get install -y python3)" >&2; exit 2; }
url="${MERIDIAN_URL:?set MERIDIAN_URL}"
if [ "$(curl -sS --max-time 15 "${url%/}/healthz" 2>/dev/null)" != "ok" ]; then
  echo "FAIL: ${url%/}/healthz does not answer ok - the panel is down or the address is wrong" >&2
  echo "On the panel's host: systemctl status meridian; journalctl -u meridian -n 50 --no-pager" >&2
  exit 1
fi
echo "ok   the panel answers at $url"
"$HERE/api.sh" GET /api/me >/dev/null && echo "ok   the API token works"
servers="$("$HERE/api.sh" GET /api/servers)"
overview="$("$HERE/api.sh" GET /api/overview)"
printf '%s\n%s\n' "$servers" "$overview" | python3 -c '
import json, sys
servers = json.loads(sys.stdin.readline())
ov = json.loads(sys.stdin.readline())
bad = 0
for s in servers:
    if not s.get("first_seen_at"):
        print("FAIL %s: the agent never connected - run its install command on the server" % s["name"]); bad += 1
    elif not s.get("online"):
        print("FAIL %s: offline - check meridian-agent on the server" % s["name"]); bad += 1
    elif s.get("apply_errors"):
        print("FAIL %s: could not apply its configuration: %s" % (s["name"], s["apply_errors"])); bad += 1
    else:
        print("ok   %s is online" % s["name"])
if not servers:
    print("note no servers yet")
for a in ov.get("alerts") or []:
    print("ATTN %s" % (a.get("message") or a.get("text") or a))
    if a.get("level") in ("crit", "warn"):
        bad += 1
print("ok   %d of %d servers online, %d users, %d connected now" % (ov["servers"]["online"], ov["servers"]["total"], ov.get("users", 0), ov.get("online_users", 0)))
sys.exit(1 if bad else 0)
'
