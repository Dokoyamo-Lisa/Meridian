#!/bin/bash
# usage: client.sh CONFIG PORT [label] - run sing-box in the client netns and test through it
cfg=$1; port=$2; label=${3:-test}
mkdir -p /root/mt
ip netns exec client /tmp/sing-box run -c "$cfg" > /root/mt/client-$port.log 2>&1 &
pid=$!
sleep 2
for i in 1 2 3; do
  ip netns exec client curl -s -m 25 -x socks5h://127.0.0.1:$port -o /dev/null -w "$label: %{http_code} %{time_total}s\n" https://www.gstatic.com/generate_204
done
ip netns exec client curl -s -m 40 -x socks5h://127.0.0.1:$port -o /dev/null -w "$label 5MB download: %{http_code} %{speed_download} B/s\n" "https://speed.cloudflare.com/__down?bytes=5000000"
kill $pid; wait $pid 2>/dev/null
grep -i "error" /root/mt/client-$port.log | tail -2
