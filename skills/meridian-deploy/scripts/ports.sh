#!/usr/bin/env bash
# ports.sh SERVER_ID - the ports a server's protocols and forwards listen on, one per line as
# PORT/tcp or PORT/udp: what its firewall (ufw, firewalld, a cloud security group) must let in.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
[ $# -eq 1 ] || { echo "usage: ports.sh SERVER_ID" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "error: python3 is needed (apt-get install -y python3)" >&2; exit 2; }
"$HERE/api.sh" GET "/api/servers/$1" | python3 -c '
import json, sys
s = json.load(sys.stdin)["server"]
udp_only = {"hysteria2", "wireguard"}
both = {"shadowsocks"}
out = []
for n in s.get("nodes") or []:
    if not n.get("enabled"):
        continue
    p, k = n["port"], n["kind"]
    if k in udp_only:
        out.append("%d/udp" % p)
    elif k in both:
        out += ["%d/tcp" % p, "%d/udp" % p]
    else:
        out.append("%d/tcp" % p)
for f in s.get("forwards") or []:
    p, net = f.get("listen_port"), f.get("network") or "tcp"
    if not p:
        continue
    if "tcp" in net:
        out.append("%d/tcp" % p)
    if "udp" in net:
        out.append("%d/udp" % p)
print("\n".join(dict.fromkeys(out)))
'
