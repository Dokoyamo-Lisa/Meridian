#!/bin/bash
# pause-live.sh - start a long Hysteria2 download, report when it ends
ip netns exec client /tmp/sing-box run -c /tmp/vm2.json > /dev/null 2>&1 & pid=$!
sleep 1.5
ip netns exec client curl -s -m 60 --limit-rate 300k -x socks5h://127.0.0.1:21082 -o /dev/null -w "download ended: http=%{http_code} bytes=%{size_download} after %{time_total}s\n" "https://speed.cloudflare.com/__down?bytes=25000000"
kill $pid; wait $pid 2>/dev/null
