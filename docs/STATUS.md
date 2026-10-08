# Meridian status

Last updated 2026-10-08 (version 0.7.1).

## Tested end to end for 0.7.1 (local VMs: Ubuntu with systemd, Alpine with OpenRC)

- **Agent upgrades** 0.7.0 -> 0.7.1 through **Upgrade all agents**, and again per server: Xray,
  every Hysteria2 node and the realm forwards kept their PIDs on both servers.
- **The operator's own inbounds** (server code, a VLESS one with its user and a SOCKS5 one with an
  account): adding, pausing and resuming a user left their listening sockets as they were (same
  socket inode, same Xray PID); changing the inbound itself re-opened it with the new user.
- **A Hysteria2 protocol Hysteria refuses** (advanced settings with an unknown bandwidth unit): the
  apply error names the protocol and gives Hysteria's reason, the server page's core row says
  "not running: failed to load server config: ..."; fixing the settings cleared both and the node
  started, without Hysteria's update check. Other cores untouched.
- **Kernel forward ports with a country rule**: allow-only JP on the server; a connection from the
  server to 1.1.1.1 / 1.0.0.1 with source port 30080 (a kernel forward's port) worked (301), no
  packets dropped by the rule; a client on a Chinese address was still refused, and got in again
  once the rule was removed. The ruleset loads on nftables (Ubuntu and Alpine).
- **Rotating the token of a proxy-pass entry**: the pass kept working after the rotation and a full
  recompile of the exit; after reinstalling the agent with the new command both ends switched to
  new credentials and the pass worked (Xray PID unchanged).
- **Protocol window**: a Hysteria2 bandwidth change asked "Restart this protocol?", a port change
  "Devices must refresh (port) ... Hysteria2 also restarts once"; Escape closed only the
  confirmation (the window kept the edit); saving restarted only that node. Making a protocol
  pass-only asked first. Labels focus their fields.
- **Proxy pass through two servers** (a third Alpine VM for the far exit): Ubuntu entry -> Alpine
  relay -> far exit carried traffic (204); the relay's log showed the entry's pass user going on to
  its own exit, the far exit's log the relay's pass user leaving directly. The entry's card showed
  the chain; the protocol window listed the relay as "Alpine Test · REALITY :10001 → C Test ·
  REALITY", offered the relay only exits that leave the internet themselves, and locked the far
  exit's pass with the reason. Far exit turned off: entry and relay both blocked (no direct exit),
  back on: 204 again, no Xray restart on either. A third pass was refused by the API.
  (Test-setup note: on Alpine VMs without nftables, plain-UDP DNS from Xray was answered by the Mac's
  network with fake addresses, which the private-address guard rightly blocked; DNS over HTTPS in
  the server's Xray code avoided it.)
- **Users' page**: WireGuard tunnels with file and QR code; protocol names as in the apps. With the
  API failing (502) the page said the panel cannot be reached and recovered by itself when it was
  back; signing out while unreachable said it failed and kept the session, and worked afterwards.
- **Certificates**: each card shows its ID; the renewal command shown (pipe + `curl -K`) replaced the
  certificate as is.

## Tested end to end this round (local VMs: Ubuntu with systemd, Alpine with OpenRC)

- **A protocol's own Xray settings** (panel 0.6.1, agent 0.5): a REALITY protocol's code added two
  outbounds and two rules - one site through its own freedom outbound, another into its own
  blackhole - live (same Xray PID), while the same sites through Shadowsocks on that server were
  untouched. The merged inbound kept REALITY, its port and its three users, with sniffing and
  sockopt merged in. Code Xray refuses (an unknown outbound protocol) showed Xray's reason on the
  server and the running configuration stayed; removing the code restored the original.

- **Every protocol with real clients** (`test/e2e/matrix.py`) on the Ubuntu server: 62 of 62 work in
  sing-box, mihomo and Xray - names without the server prefix for named protocols, WireGuard now
  IPv4 only (no `::/0`).
- **One address per protocol**: with a second address on the VM, two VLESS protocols shared port
  9443 on 192.168.64.222 and 192.168.64.2 (`ss` shows both), each with a `bind-n<id>` outbound
  sending from its address; a third on all addresses was refused. Both carried traffic.
- **Access per protocol**: a user given one protocol got exactly that one in their link and it worked;
  the same protocol refused that user's id on another protocol of the server.
- **IPv4 only**: Xray took it live (same PID) with the `direct-ipver` outbound; Hysteria2 configs got
  mode 4; traffic flowed.
- **Shared certificate** on the Alpine server: files written root-only, Xray took it live (same PID)
  and the agent reported it served within about a minute; a replacement was installed within 10 s
  and served after Xray's reload.
- **Configuration code**: a routing rule from the operator's JSON blocked one site live (same Xray
  PID) while others passed; a Hysteria2 YAML setting was merged (Meridian's auth kept) and the
  protocol restarted once and worked; a `dns` section stayed "restart needed" across the agent's
  checks (it used to drop after one) until the restart, which applied it.
- **Firewall rules** for WireGuard SNAT and IPv6 NAT checked with `nft -c` on nftables 1.1.5 and 1.1.6.

## Tested end to end in the local VM (0.4.0)

A Lima VM (Ubuntu, arm64) runs the agent; the panel runs on the Mac; real clients run in a network
namespace inside the VM (`test/e2e/`).

- **Every protocol with real clients** (`test/e2e/matrix.py`): 22 protocols on one server - VLESS
  (REALITY over raw / gRPC / XHTTP; TLS over raw with Vision / gRPC / HTTPUpgrade / XHTTP), VMess
  (raw, WebSocket+TLS, gRPC, HTTPUpgrade), Trojan (raw / WebSocket / gRPC with TLS), Shadowsocks
  (2022 and classic), SOCKS5, HTTP, Hysteria2 (plain and obfuscated), WireGuard. Each proxy in each
  subscription format was used for a real request: **53 of 53 work** - sing-box (20 proxies from the
  sing-box format), mihomo (22 from the Clash format) and Xray (11 from the share links). A format
  contains exactly the protocols the panel says it supports.
- **Never insecure**: no format contains `insecure`, `allowInsecure` or `skip-cert-verify`;
  self-signed certificates are pinned (mihomo `fingerprint`, sing-box `certificate`, Xray's
  `pinnedPeerCertSha256` checked by hand: right pin works, wrong pin is refused).
- **No restarts**: adding 18 protocols, an agent upgrade and an import with take-over all happened
  with the same Xray process (PID unchanged throughout).
- **Country rules**: a client on a Chinese address kept requesting every 2 s while CN was blocked:
  the open connection stopped within one request, the exception for its address restored it, and
  removing the rule kept it working.
- **Import with take-over**: an Xray with its own users (VMess, Shadowsocks 2022, VLESS REALITY)
  installed as `xray.service`; the scan found it running with its users; the import created the users
  and protocols, stopped and disabled the old service, and the devices' *original* configs kept
  working on the same ports.
- **Usage per user and server**: the user's own page showed the test traffic under the right server,
  today and this cycle.
- **Devices**: one client connected through VLESS REALITY and Hysteria2 at once showed as one device
  on the user's page ("Lima Test · REALITY, Hy2", 1 of 3 allowed) and on the server's row.
- **Public address behind NAT**: DB-IP and Cloudflare both answered over HTTPS with the right address.
- **The deploy skill, rehearsed from scratch** (`skills/meridian-deploy`): in the VM, the release
  archive was checked against `SHA256SUMS` and installed with the release's `install-panel.sh`; then,
  exactly as the skill says, `meridian token` (refused as root, with the fix), settings and logo
  through `api.sh` (a logo with a script refused with the reason), a server created, its install
  command run, `wait-server.sh` READY, `ports.sh`, a user created, and `check.sh` all ok.
- **Renamed back to Meridian**: binaries, services, paths, the firewall table and the agent channel;
  the agent reinstalled under the new names and all 62 proxies worked again (sing-box, mihomo, Xray).
- **Your own logo**: uploaded in Settings, shown in the panel, on both loading screens (swapped by the
  server before any script runs), on the status page, on subscription pages and in the browser tab.
- **Status page**: visitors get only the sign-in; a user gets their own page (data left, devices,
  link, usage per server); the supervisor gets every server on the globe. Sign-in and loading show
  the umbrella assembling and opening into the page. The globe's dots stay evenly spaced from the
  small panel to full screen at the largest zoom, and a server's pin sits on its city on the
  coastline drawing (checked for Los Angeles at full zoom).

## Done earlier, still covered by tests

- Agent channel (signed, sealed, replies bound to requests), desired state with long-poll,
  exactly-once traffic accounting, verified core downloads, live pause/resume, IP blocks that cut
  open sessions, proxy pass, port forwards (nftables and realm).
- `make check`: race-enabled Go tests (API isolation, user sessions, token scopes, MCP, every route
  documented, every accepted protocol renders and works in at least one app, every format never
  insecure, the status page's data, country rules, import), vet and staticcheck for macOS and Linux,
  govulncheck, npm audit.

## Not tested live

1. Let's Encrypt certificates issued by the agent (needs a public domain pointing at a server).
2. Proxy pass across two VMs (unit-tested; one VM here).
3. Third-party apps that are not built on sing-box, mihomo or Xray (Shadowrocket, Surge, Quantumult X,
   Stash, Loon): their formats are generated and escaped by tested code, but were not run. Loon's
   output (every protocol, WireGuard included) was parsed with Sub-Store's Loon parsers: all lines
   read back with their keys, pins and REALITY settings intact.

## Hard rules

- Never pause a user automatically. Only a manual click pauses.
- Never restart a core unless the change cannot be applied live; ask first when it would drop
  connections.
- Never make a link insecure: pin self-signed certificates or leave the protocol out.
- English only. Test locally; deploy or upload nothing without being asked.
