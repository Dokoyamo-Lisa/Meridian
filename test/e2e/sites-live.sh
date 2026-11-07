#!/bin/bash
# sites-live.sh SUBSCRIPTION_LINK - in the VM as root: for each protocol that can block sites, one
# request by name to a blocked site and its subdomain, one with only the address in the request (the
# name is in the TLS handshake) and one to a site that is not blocked (Google). Block anthropic.com on the
# protocols first: the first three columns must be 000, the last 200.
link="$1"
[ -n "$link" ] || { echo "usage: $0 SUBSCRIPTION_LINK" >&2; exit 1; }
here="$(cd "$(dirname "$0")" && pwd)"
curl -fsS "$link?client=singbox" -o /tmp/sub.json
curl -fsS "$link?client=clash" -o /tmp/sub.yaml
python3 - <<'PY'
import yaml
c=yaml.safe_load(open('/tmp/sub.yaml'))
p=[x for x in c['proxies'] if x['type']=='mieru']
if p:
    yaml.safe_dump({'mixed-port':21095,'mode':'global','log-level':'silent','proxies':p,'proxy-groups':[{'name':'GLOBAL','type':'select','proxies':[p[0]['name']]}],'rules':['MATCH,GLOBAL']},open('/tmp/mieru.yaml','w'))
PY
addr=$(getent hosts www.anthropic.com | awk '{print $1; exit}')
row() { # label port
  a=$(ip netns exec client curl -s -m 8 -x socks5h://127.0.0.1:$2 -o /dev/null -w '%{http_code}' https://anthropic.com/)
  b=$(ip netns exec client curl -s -m 8 -x socks5h://127.0.0.1:$2 -o /dev/null -w '%{http_code}' https://www.anthropic.com/)
  c=$(ip netns exec client curl -s -m 8 -x socks5://127.0.0.1:$2 --resolve www.anthropic.com:443:$addr -o /dev/null -w '%{http_code}' https://www.anthropic.com/)
  d=$(ip netns exec client curl -s -m 8 -x socks5h://127.0.0.1:$2 -o /dev/null -w '%{http_code}' https://www.google.com/)
  printf '%-10s %-8s %-8s %-8s %s\n' "$1" "$a" "$b" "$c" "$d"
}
printf '%-10s %-8s %-8s %-8s %s\n' protocol name subdom by-addr other
i=1
for t in vless shadowsocks hysteria2 anytls; do
  tag=$(python3 -c "import json,sys; d=json.load(open('/tmp/sub.json')); print(next((o['tag'] for o in d['outbounds'] if o['type']==sys.argv[1]),''))" $t)
  [ -n "$tag" ] || continue
  port=$((21090+i)); i=$((i+1))
  python3 "$here/make-client.py" /tmp/sub.json "$tag" $port > /tmp/site$port.json
  ip netns exec client /tmp/sing-box run -c /tmp/site$port.json > /dev/null 2>&1 & pid=$!
  sleep 1.5
  row "$t" $port
  kill $pid; wait $pid 2>/dev/null
done
if [ -f /tmp/mieru.yaml ]; then
  ip netns exec client /tmp/mihomo -f /tmp/mieru.yaml -d /tmp/mihomo-site > /dev/null 2>&1 & pid=$!
  sleep 2
  row mieru 21095
  kill $pid; wait $pid 2>/dev/null
fi
