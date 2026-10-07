#!/usr/bin/env python3
# alpine-probe.py LINK [SERVER] - on the Alpine VM, as root: one request through every proxy of the
# link's sing-box format whose name contains SERVER, with the server address pointed at 127.0.0.1
# (the client runs on the server itself). Needs /tmp/sing-box (the musl build) and curl.
import json, subprocess, sys, time, urllib.request

link = sys.argv[1]
name = sys.argv[2] if len(sys.argv) > 2 else ""
cfg = json.loads(urllib.request.urlopen(link + "?client=singbox", timeout=20).read())
outs = [o for o in cfg.get("outbounds", []) if name in o.get("tag", "") and o.get("server")]
# WireGuard in sing-box 1.12+ is an endpoint, not an outbound
eps = [e for e in cfg.get("endpoints", []) if name in e.get("tag", "")]
results = []
for i, o in enumerate(outs + eps):
    endpoint = i >= len(outs)
    o = json.loads(json.dumps(o))
    if "server" in o:
        o["server"] = "127.0.0.1"
    for p in o.get("peers", []):
        p["address"] = "127.0.0.1"
    port = 24200 + i
    c = {"log": {"level": "error"},
         "inbounds": [{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": port}],
         "outbounds": [], "route": {"rules": [{"inbound": ["in"], "outbound": o["tag"]}]}}
    if endpoint:
        c["endpoints"] = [o]
    else:
        c["outbounds"].append(o)
    open("/tmp/probe.json", "w").write(json.dumps(c))
    p = subprocess.Popen(["/tmp/sing-box", "run", "-c", "/tmp/probe.json"],
                         stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    time.sleep(1.5)
    code = "000"
    for _ in range(2):
        r = subprocess.run(["curl", "-s", "-m", "15", "-x", f"socks5h://127.0.0.1:{port}", "-o", "/dev/null",
                            "-w", "%{http_code}", "https://www.gstatic.com/generate_204"],
                           capture_output=True, text=True)
        code = r.stdout.strip() or "000"
        if code == "204":
            break
    p.terminate()
    try:
        err = p.communicate(timeout=5)[1].decode()[-160:]
    except Exception:
        err = ""
    results.append(code)
    print(f"{code}  {o['type']:12} {o['tag']}" + ("" if code == "204" else f"   {err.strip()}"))
print(f"{results.count('204')}/{len(results)} work")
sys.exit(0 if results and all(r == "204" for r in results) else 1)
