#!/bin/bash
# country-live.sh CONFIG PORT [SECONDS] - in the VM, as root, after setup-client.sh and gen-clients.sh.
#
# Gives the client namespace a second, public address (101.0.0.2 - a Chinese network in the
# DB-IP database) and sends a request through one protocol every 2 seconds from that address,
# printing each result. Meanwhile, in the panel, block CN in Access › Country rule for your servers:
# requests must start failing within seconds (the open connection is cut too), and work again after
# the rule is removed or 101.0.0.2 is added as an exception.
#
#   bash country-live.sh /tmp/vm1.json 21081 90
set -e
cfg="$1"; port="$2"; secs="${3:-60}"
[ -f "$cfg" ] && [ -n "$port" ] || { echo "usage: $0 CONFIG PORT [SECONDS]" >&2; exit 1; }
srv=$(ip -4 -o addr show lima0 | awk '{print $4}' | cut -d/ -f1 | head -1)
ip addr show dev veth-h | grep -q 101.0.0.1/ || ip addr add 101.0.0.1/24 dev veth-h
ip netns exec client ip addr show dev veth-c | grep -q 101.0.0.2/ || ip netns exec client ip addr add 101.0.0.2/24 dev veth-c
ip route replace 101.0.0.0/24 dev veth-h src "$srv"
# from the namespace, the server is reached from 101.0.0.2
ip netns exec client ip route replace "$srv/32" via 101.0.0.1 src 101.0.0.2
trap 'ip netns exec client ip route del "$srv/32" 2>/dev/null; kill $pid 2>/dev/null' EXIT
ip netns exec client /tmp/sing-box run -c "$cfg" > /dev/null 2>&1 & pid=$!
sleep 1.5
end=$((SECONDS + secs))
while [ $SECONDS -lt $end ]; do
  r=$(ip netns exec client curl -s -m 4 -x socks5h://127.0.0.1:$port -o /dev/null -w "%{http_code}" https://www.gstatic.com/generate_204 || true)
  echo "$(date +%T) $r"
  sleep 2
done
