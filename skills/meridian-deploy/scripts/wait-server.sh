#!/usr/bin/env bash
# wait-server.sh SERVER_ID [SECONDS] - wait until a server's agent is connected and has applied its
# configuration (default: up to 300 seconds). Prints the server's protocols when it is ready, or
# what to check when it is not. Exit 0 = ready, 1 = not ready.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
[ $# -ge 1 ] || { echo "usage: wait-server.sh SERVER_ID [SECONDS]" >&2; exit 2; }
id="$1" limit="${2:-300}"
command -v python3 >/dev/null 2>&1 || { echo "error: python3 is needed to read the panel's answers (apt-get install -y python3)" >&2; exit 2; }
end=$(( $(date +%s) + limit ))
while :; do
  json="$("$HERE/api.sh" GET "/api/servers/$id")"
  verdict="$(printf '%s' "$json" | python3 -c '
import json, sys, time
s = json.load(sys.stdin)["server"]
fresh = time.time() - (s.get("last_seen_at") or 0) < 60     # a report now, not data from before a reinstall
if s.get("apply_errors") and fresh:
    print("error\t" + s["apply_errors"].replace("\n", " | "))
elif s.get("online") and s.get("applied_rev") and fresh and s.get("applied_rev") == (s.get("desired_rev") or s.get("applied_rev")):
    nodes = [n for n in s.get("nodes") or [] if n.get("enabled")]
    on = lambda n: "%s on %s" % (n["kind"], n["port"]) + (" (devices use %s)" % n["public_port"] if n.get("public_port") else "")
    print("ready\t%s (%s, agent %s, Xray %s): %s" % (s["name"], s.get("ipv4") or s.get("ipv6") or "?", s.get("agent_version") or "?",
        s.get("xray_version") or "-", ", ".join(on(n) for n in nodes) or "no protocols yet")
        + "".join("\nNOTE: " + l for l in s.get("limits") or []))
elif s.get("first_seen_at"):
    print("waiting\tconnected, applying the configuration")
else:
    print("waiting\tthe agent has not connected yet")
')"
  state="${verdict%%$'\t'*}" detail="${verdict#*$'\t'}"
  case "$state" in
    ready) echo "READY: $detail"; exit 0 ;;
    error) echo "NOT READY - the server could not apply its configuration: $detail" >&2
           echo "The previous working configuration keeps running. Read the message, fix what it names (in the panel), and run this again." >&2
           exit 1 ;;
  esac
  if [ "$(date +%s)" -ge "$end" ]; then
    echo "NOT READY after ${limit}s: $detail" >&2
    echo "Check on the server: systemctl status meridian-agent; journalctl -u meridian-agent -n 50 --no-pager" >&2
    echo "  (Alpine: rc-service meridian-agent status; grep meridian-agent /var/log/messages | tail -50)" >&2
    echo "The server must reach the panel: curl -sS \$MERIDIAN_URL/healthz (run it ON the server) must print ok." >&2
    echo "If the install command failed, copy a fresh one: api.sh GET /api/servers/$id (field .install) and run it again as root." >&2
    exit 1
  fi
  echo "... $detail"
  sleep 5
done
