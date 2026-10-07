#!/bin/bash
# block-live.sh N [COUNT] - in the VM as root: keep one client running for protocol N (1=REALITY
# 2=Hysteria2 3=SS 4=WireGuard) and make a request every 2 seconds over the same session. Block
# 10.99.0.2 in the panel while it runs: requests must start failing within seconds; unblock and they
# recover.
n=${1:-2}; port=$((21080+n))
ip netns exec client /tmp/sing-box run -c /tmp/vm$n.json > /dev/null 2>&1 & pid=$!
sleep 1.5
for i in $(seq 1 ${2:-15}); do
  code=$(ip netns exec client curl -s -m 4 -x socks5h://127.0.0.1:$port -o /dev/null -w "%{http_code}" https://www.gstatic.com/generate_204)
  echo "$(date +%T) $code"
  sleep 2
done
kill $pid; wait $pid 2>/dev/null
