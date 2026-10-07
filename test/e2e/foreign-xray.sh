#!/bin/bash
# foreign-xray.sh - in the VM, as root: start an Xray that "another panel" installed, for testing the
# import and take-over. It runs as xray.service from /usr/local/etc/xray/config.json with three
# inbounds and two users (carol, dave), and writes sing-box client configs that use the ORIGINAL
# credentials to /tmp/foreign-{vmess,ss,reality}.json (local proxy ports 22001-22003).
#
# After the import with take-over these same client configs must keep working.
# Clean up: systemctl disable --now xray; rm -rf /usr/local/etc/xray /etc/systemd/system/xray.service
set -e
X=/usr/local/bin/xray-foreign
cp "$(ls /var/lib/meridian-agent/cores/xray/*/xray | head -1)" "$X"
srv=$(ip -4 -o addr show lima0 | awk '{print $4}' | cut -d/ -f1 | head -1)
u1=$($X uuid); u2=$($X uuid)
keys=$($X x25519)
priv=$(echo "$keys" | awk -F': ' '/Private/ {print $2}')
pub=$(echo "$keys" | awk -F': ' '/Password|Public/ {print $2}' | head -1)
sskey=$(openssl rand -base64 16); k1=$(openssl rand -base64 16); k2=$(openssl rand -base64 16)
mkdir -p /usr/local/etc/xray
cat > /usr/local/etc/xray/config.json <<EOF
{
  "log": {"loglevel": "warning"},
  "inbounds": [
    {"tag": "vmess-in", "port": 18001, "protocol": "vmess",
     "settings": {"clients": [{"id": "$u1", "email": "carol"}, {"id": "$u2", "email": "dave"}]},
     "streamSettings": {"network": "raw"}},
    {"tag": "ss-in", "port": 18002, "protocol": "shadowsocks",
     "settings": {"method": "2022-blake3-aes-128-gcm", "password": "$sskey", "network": "tcp,udp",
                  "clients": [{"password": "$k1", "email": "carol"}, {"password": "$k2", "email": "dave"}]}},
    {"tag": "reality-in", "port": 18003, "protocol": "vless",
     "settings": {"decryption": "none", "clients": [{"id": "$u1", "email": "carol", "flow": "xtls-rprx-vision"}]},
     "streamSettings": {"network": "raw", "security": "reality",
       "realitySettings": {"target": "www.apple.com:443", "serverNames": ["www.apple.com"], "privateKey": "$priv", "shortIds": ["a1b2c3d4"]}}}
  ],
  "outbounds": [{"protocol": "freedom"}]
}
EOF
cat > /etc/systemd/system/xray.service <<EOF
[Unit]
Description=Xray (installed by another panel)
After=network.target
[Service]
ExecStart=$X run -config /usr/local/etc/xray/config.json
Restart=on-failure
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now xray >/dev/null 2>&1
sleep 1
systemctl is-active xray
mk() { # name port outbound-json
  cat > "/tmp/foreign-$1.json" <<EOF
{"log":{"level":"error"},"dns":{"servers":[{"type":"udp","tag":"d","server":"8.8.8.8"}],"final":"d"},
 "inbounds":[{"type":"mixed","listen":"127.0.0.1","listen_port":$2}],
 "outbounds":[$3],
 "route":{"rules":[{"action":"sniff"}],"final":"p","default_domain_resolver":"d"}}
EOF
}
mk vmess 22001 "{\"type\":\"vmess\",\"tag\":\"p\",\"server\":\"$srv\",\"server_port\":18001,\"uuid\":\"$u1\",\"security\":\"auto\",\"alter_id\":0}"
mk ss 22002 "{\"type\":\"shadowsocks\",\"tag\":\"p\",\"server\":\"$srv\",\"server_port\":18002,\"method\":\"2022-blake3-aes-128-gcm\",\"password\":\"$sskey:$k1\"}"
mk reality 22003 "{\"type\":\"vless\",\"tag\":\"p\",\"server\":\"$srv\",\"server_port\":18003,\"uuid\":\"$u1\",\"flow\":\"xtls-rprx-vision\",\"tls\":{\"enabled\":true,\"server_name\":\"www.apple.com\",\"utls\":{\"enabled\":true,\"fingerprint\":\"chrome\"},\"reality\":{\"enabled\":true,\"public_key\":\"$pub\",\"short_id\":\"a1b2c3d4\"}}}"
echo "wrote /tmp/foreign-vmess.json /tmp/foreign-ss.json /tmp/foreign-reality.json"
