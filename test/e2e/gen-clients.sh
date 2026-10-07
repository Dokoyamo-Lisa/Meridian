#!/bin/bash
# gen-clients.sh SUBSCRIPTION_LINK - in the VM: write /tmp/vm1..4.json (REALITY, Hysteria2, SS, WireGuard)
# from the subscription's sing-box format. The link must be reachable from the VM.
set -e
link="$1"
[ -n "$link" ] || { echo "usage: $0 SUBSCRIPTION_LINK" >&2; exit 1; }
here="$(cd "$(dirname "$0")" && pwd)"
sb="$(mktemp)"
trap 'rm -f "$sb"' EXIT
curl -fsS "$link?client=singbox" -o "$sb"
i=1
for kind in vless hysteria2 shadowsocks wireguard; do
  tag=$(python3 -c "import json,sys; d=json.load(open(sys.argv[2])); print(next(o['tag'] for o in d.get('outbounds',[])+d.get('endpoints',[]) if o['type']==sys.argv[1]))" "$kind" "$sb")
  python3 "$here/make-client.py" "$sb" "$tag" $((21080+i)) > /tmp/vm$i.json
  i=$((i+1))
done
echo "wrote /tmp/vm1.json .. /tmp/vm4.json"
