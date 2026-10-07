#!/bin/bash
# setup-client.sh - prepare the VM's client side for protocol tests (run as root after every boot):
# a "client" network namespace (10.99.0.2) and the clients the tests use - sing-box, mihomo and
# Xray (the agent's own binary) - in /tmp.
set -e
cd /tmp
case "$(uname -m)" in
  aarch64|arm64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac
if [ ! -x /tmp/sing-box ]; then
  curl -fsSL -o sb.tgz "https://github.com/SagerNet/sing-box/releases/download/v1.14.2/sing-box-1.14.2-linux-$arch.tar.gz"
  tar xzf sb.tgz && cp "sing-box-1.14.2-linux-$arch/sing-box" /tmp/sing-box
fi
if [ ! -x /tmp/mihomo ]; then
  v=v1.19.32
  curl -fsSL -o mihomo.gz "https://github.com/MetaCubeX/mihomo/releases/download/$v/mihomo-linux-$arch-$v.gz"
  gunzip -f mihomo.gz && chmod +x mihomo
fi
x=$(ls /var/lib/meridian-agent/cores/xray/*/xray 2>/dev/null | head -1 || true)
[ -n "$x" ] && ln -sf "$x" /tmp/xray
python3 -c 'import yaml' 2>/dev/null || apt-get install -y -qq python3-yaml >/dev/null
if ! ip netns list | grep -q client; then
  ip netns add client
  ip link add veth-c type veth peer name veth-h
  ip link set veth-c netns client
  ip addr add 10.99.0.1/24 dev veth-h && ip link set veth-h up
  ip netns exec client ip addr add 10.99.0.2/24 dev veth-c
  ip netns exec client ip link set veth-c up
  ip netns exec client ip link set lo up
  ip netns exec client ip route add default via 10.99.0.1
fi
# replies to the client namespace carry the server's own address (as on a single-address server):
# otherwise UDP replies leave with 10.99.0.1 and strict QUIC clients drop them
srv=$(ip -4 -o addr show lima0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)
[ -n "$srv" ] && ip route replace 10.99.0.0/24 dev veth-h src "$srv"
# the client namespace reaches the internet (its own DNS lookups) through NAT on the VM
iptables -t nat -C POSTROUTING -s 10.99.0.0/24 ! -d 10.99.0.0/24 -j MASQUERADE 2>/dev/null || \
  iptables -t nat -A POSTROUTING -s 10.99.0.0/24 ! -d 10.99.0.0/24 -j MASQUERADE
/tmp/sing-box version | head -1
/tmp/mihomo -v | head -1
[ -x /tmp/xray ] && /tmp/xray version | head -1 || echo "Xray: install the agent first (it brings Xray)"
