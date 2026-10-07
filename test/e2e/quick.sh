#!/bin/bash
# quick.sh LABEL - in the VM, as root: one request through each protocol from the client namespace.
# Prints the HTTP status per protocol; 204 means it works, 000 means it does not.
for i in 1 2 3 4; do
  ip netns exec client /tmp/sing-box run -c /tmp/vm$i.json > /dev/null 2>&1 & pid=$!
  sleep 1.5
  r=$(ip netns exec client curl -s -m 15 -x socks5h://127.0.0.1:$((21080+i)) -o /dev/null -w "%{http_code}" https://www.gstatic.com/generate_204)
  printf "%s " "$r"
  kill $pid; wait $pid 2>/dev/null
done
echo " <- REALITY Hy2 SS WG ($1)"
