#!/usr/bin/env python3
"""matrix.py LINK [sing-box|mihomo|xray ...] - in the VM, as root, after setup-client.sh.

Fetches a user's subscription in three formats and sends one request through every proxy in each,
with a real client running in the "client" network namespace:

  sing-box  the sing-box format (?client=singbox)                         /tmp/sing-box
  mihomo    the Clash/mihomo format (?client=clash)                        /tmp/mihomo
  xray      the share links (?client=uri), turned into Xray client configs  /tmp/xray

Prints one line per proxy and client: 204 = it works, anything else = it does not. Exits 1 when any
proxy that a format contains does not work - a format must only contain proxies that work.
"""
import base64
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

import yaml  # python3-yaml

TEST_URL = "https://www.gstatic.com/generate_204"
PORT = 23800


def fetch(link, client):
    sep = "&" if "?" in link else "?"
    with urllib.request.urlopen(f"{link}{sep}client={client}", timeout=20) as r:
        return r.read().decode()


def probe(cmd, port, cfg_text, suffix):
    """Run a client with cfg_text in the namespace and send one request through its proxy."""
    with tempfile.TemporaryDirectory() as d:
        cfg = os.path.join(d, "config" + suffix)
        with open(cfg, "w") as f:
            f.write(cfg_text)
        argv = ["ip", "netns", "exec", "client"] + [a.replace("{cfg}", cfg).replace("{dir}", d) for a in cmd]
        p = subprocess.Popen(argv, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            # wait for the local proxy port
            for _ in range(40):
                r = subprocess.run(["ip", "netns", "exec", "client", "bash", "-c", f"exec 3<>/dev/tcp/127.0.0.1/{port}"],
                                   capture_output=True)
                if r.returncode == 0:
                    break
                if p.poll() is not None:
                    return "exit", p.stderr.read().decode()[-300:]
                time.sleep(0.15)
            # two tries: some clients set a tunnel up lazily on first use (mihomo's WireGuard)
            code = "000"
            for _ in range(2):
                r = subprocess.run(["ip", "netns", "exec", "client", "curl", "-s", "-m", "15", "-x", f"socks5h://127.0.0.1:{port}",
                                    "-o", "/dev/null", "-w", "%{http_code}", TEST_URL], capture_output=True, text=True)
                code = r.stdout.strip() or "000"
                if code == "204":
                    break
            return code, ""
        finally:
            p.terminate()
            try:
                p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                p.kill()


# ---------------------------------------------------------------- sing-box

def singbox_cases(link):
    cfg = json.loads(fetch(link, "singbox"))
    outs = [o for o in cfg.get("outbounds", []) if o.get("type") not in ("selector", "urltest", "direct", "block", "dns")]
    outs += cfg.get("endpoints", [])
    for o in outs:
        c = {
            "log": {"level": "error"},
            "dns": {"servers": [{"type": "udp", "tag": "d", "server": "8.8.8.8"}], "final": "d"},
            "inbounds": [{"type": "mixed", "listen": "127.0.0.1", "listen_port": PORT}],
            "outbounds": [o] if o in cfg.get("outbounds", []) else [],
            "route": {"rules": [{"action": "sniff"}], "final": o["tag"], "default_domain_resolver": "d"},
        }
        if o not in cfg.get("outbounds", []):
            c["endpoints"] = [o]
        yield o["tag"], json.dumps(c)


# ---------------------------------------------------------------- mihomo

def mihomo_cases(link):
    cfg = yaml.safe_load(fetch(link, "clash"))
    for px in cfg.get("proxies", []):
        c = {"mixed-port": PORT, "bind-address": "127.0.0.1", "allow-lan": False, "mode": "rule", "log-level": "error",
             "ipv6": False, "geodata-mode": False, "geo-auto-update": False, "unified-delay": False,
             "dns": {"enable": True, "nameserver": ["8.8.8.8"]},
             "proxies": [px], "rules": [f"MATCH,{px['name']}"]}
        yield px["name"], yaml.safe_dump(c, allow_unicode=True)


# ---------------------------------------------------------------- Xray from share links

def stream_from_query(net, q, host_default):
    s = {"network": {"tcp": "raw"}.get(net, net)}
    sec = q.get("security", "none")
    if sec == "reality":
        s["security"] = "reality"
        s["realitySettings"] = {"serverName": q.get("sni", ""), "fingerprint": q.get("fp", "chrome"),
                                "publicKey": q.get("pbk", ""), "shortId": q.get("sid", ""), "spiderX": q.get("spx", "")}
    elif sec == "tls":
        s["security"] = "tls"
        t = {"serverName": q.get("sni", host_default), "fingerprint": q.get("fp", "chrome")}
        if q.get("alpn"):
            t["alpn"] = q["alpn"].split(",")
        if q.get("allowInsecure") in ("1", "true"):
            t["allowInsecure"] = True
        s["tlsSettings"] = t
    else:
        s["security"] = "none"
    path, host = q.get("path", "/"), q.get("host", "")
    if net == "ws":
        s["wsSettings"] = {"path": path, "headers": {"Host": host} if host else {}}
    elif net == "httpupgrade":
        s["httpupgradeSettings"] = {"path": path, "host": host}
    elif net == "grpc":
        s["grpcSettings"] = {"serviceName": q.get("serviceName", ""), "multiMode": q.get("mode") == "multi"}
    elif net == "xhttp":
        s["xhttpSettings"] = {"path": path, "host": host, "mode": q.get("mode", "auto")}
    return s


def xray_outbound(line):
    scheme, _ = line.split("://", 1)
    if scheme == "vmess":
        v = json.loads(base64.b64decode(line[8:] + "=" * (-len(line[8:]) % 4)))
        q = {"security": "tls" if v.get("tls") == "tls" else "none", "sni": v.get("sni", ""), "fp": v.get("fp", "chrome"),
             "alpn": v.get("alpn", ""), "allowInsecure": v.get("allowInsecure", ""), "path": v.get("path", "/"),
             "host": v.get("host", ""), "serviceName": v.get("path", ""), "mode": v.get("type", "")}
        net = v.get("net", "tcp")
        if net == "grpc":
            q["mode"] = "multi" if v.get("type") == "multi" else "gun"
        return v.get("ps", "vmess"), {"protocol": "vmess", "settings": {"vnext": [{"address": v["add"], "port": int(v["port"]),
                "users": [{"id": v["id"], "alterId": int(v.get("aid", 0)), "security": v.get("scy", "auto")}]}]},
                "streamSettings": stream_from_query(net, q, v["add"])}
    u = urllib.parse.urlsplit(line)
    name = urllib.parse.unquote(u.fragment)
    q = dict(urllib.parse.parse_qsl(u.query))
    host, port = u.hostname, u.port
    if scheme in ("vless", "trojan"):
        net = q.get("type", "tcp")
        stream = stream_from_query(net, q, host)
        if scheme == "vless":
            user = {"id": urllib.parse.unquote(u.username), "encryption": q.get("encryption", "none")}
            if q.get("flow"):
                user["flow"] = q["flow"]
            return name, {"protocol": "vless", "settings": {"vnext": [{"address": host, "port": port, "users": [user]}]},
                          "streamSettings": stream}
        return name, {"protocol": "trojan", "settings": {"servers": [{"address": host, "port": port,
                      "password": urllib.parse.unquote(u.username)}]}, "streamSettings": stream}
    if scheme == "ss":
        userinfo = urllib.parse.unquote(u.username)
        try:
            method, password = base64.urlsafe_b64decode(userinfo + "=" * (-len(userinfo) % 4)).decode().split(":", 1)
        except Exception:
            method, password = userinfo.split(":", 1)
        return name, {"protocol": "shadowsocks", "settings": {"servers": [{"address": host, "port": port, "method": method,
                      "password": password}]}}
    if scheme in ("socks", "socks5", "http"):
        user = urllib.parse.unquote(u.username or "")
        pw = urllib.parse.unquote(u.password or "")
        if scheme != "http" and user and not pw:
            user, pw = base64.urlsafe_b64decode(user + "=" * (-len(user) % 4)).decode().split(":", 1)
        proto = "http" if scheme == "http" else "socks"
        srv = {"address": host, "port": port}
        if user:
            srv["users"] = [{"user": user, "pass": pw}]
        return name, {"protocol": proto, "settings": {"servers": [srv]}}
    return name, None  # hysteria2, wireguard: tested with sing-box and mihomo


def xray_cases(link):
    for line in fetch(link, "uri").splitlines():
        line = line.strip()
        if not line:
            continue
        name, ob = xray_outbound(line)
        if ob is None:
            continue
        ob["tag"] = "out"
        c = {"log": {"loglevel": "error"},
             "inbounds": [{"listen": "127.0.0.1", "port": PORT, "protocol": "socks", "settings": {"udp": True}}],
             "outbounds": [ob]}
        yield name, json.dumps(c)


CLIENTS = {
    "sing-box": (singbox_cases, ["/tmp/sing-box", "run", "-c", "{cfg}"], ".json"),
    "mihomo": (mihomo_cases, ["/tmp/mihomo", "-d", "{dir}", "-f", "{cfg}"], ".yaml"),
    "xray": (xray_cases, ["/tmp/xray", "run", "-c", "{cfg}"], ".json"),
}


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    link = sys.argv[1]
    which = sys.argv[2:] or list(CLIENTS)
    failed = 0
    total = 0
    for client in which:
        cases, cmd, suffix = CLIENTS[client]
        for name, cfg in cases(link):
            code, err = probe(cmd, PORT, cfg, suffix)
            total += 1
            ok = code == "204"
            failed += 0 if ok else 1
            print(f"{code:>4}  {client:8}  {name}" + (f"   {err.strip()[:200]}" if err else ""), flush=True)
    print(f"{total - failed}/{total} work")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
