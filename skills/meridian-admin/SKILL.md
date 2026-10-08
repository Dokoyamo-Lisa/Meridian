---
name: meridian-admin
description: Operate a Meridian proxy/VPN panel through its MCP tools - check health, onboard servers and protocols, import existing setups, add and manage users and their sign-ins, investigate link sharing and abuse, see where traffic goes, set country rules and the status page, and handle alerts safely. Use whenever the user asks about their Meridian servers, users, connected IPs, traffic, blocks, country rules or status page, and the umbrella MCP server is connected.
---

# Operating Meridian

Meridian manages servers and their protocols (Xray: VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP;
Hysteria2; WireGuard; port forwards) and **users**. Each user has one subscription link for their
apps and usually a username and password for their own page (`/me`), where they see their usage per
server, their devices and their link. You reach the panel through the `meridian` MCP tools, acting
for its one supervisor account.

## Ground rules

1. **Look before you touch.** Start with `overview`; use read tools to understand the situation
   before proposing any change.
2. **Never disrupt without an explicit yes.** Tools marked destructive (`pause_user`, `delete_user`,
   `rotate_user_link`, `reset_user_credentials`, `import_protocols`, `set_country_rule`,
   `set_protocol_enabled`, `remove_forward`, `block_ip`, `server_action`) disconnect people, stop
   services or cannot be undone. Before calling one, tell the user exactly what will happen (who is
   disconnected, what stops working, how to undo it) and wait for them to confirm in this
   conversation. Only then pass `confirm=true`. A confirmation covers one action, not similar ones
   later.
3. **Nothing happens automatically, and you should not pretend otherwise.** Quotas, expiry dates and
   IP limits only raise flags; a flagged user keeps working until a person pauses them.
4. **Keep secrets out of the chat.** Do not print links, passwords, install commands (they contain a
   server secret) or credentials unless the user asks for them. When a tool returns a generated
   password, say it is shown once and hand it over only to the person who asked.
5. **Read-only token?** The change tools are missing. Say so and suggest a full-access token in
   Settings › API & MCP if the user wants you to act.
6. Times are Unix seconds, traffic in bytes, rates in bytes per second - convert to human units
   (GB, Mbps, "3 hours ago") when you answer.

## Health check ("is everything OK?")

1. `overview` → servers online/total, people online, today's traffic, `alerts`.
2. For each alert, explain it in one line and say what the user can do:
   - `server_offline` - the agent stopped reporting; protocols may still run. Check the server is up
     and `meridian-agent` is running.
   - `apply_error` - the server could not apply the latest change and keeps the previous working
     configuration. `get_server` shows the message ("the panel needs a newer agent" means: upgrade
     the agent with `server_action upgrade_agent` - nobody is disconnected).
   - `restart_pending` - a change waits for one restart (of Xray, or of the Hysteria2 protocols it
     names); nothing restarts until the user clicks **Restart now** (or confirms
     `server_action restart_pending`, which restarts exactly what waits).
   - `over_ip_limit`, `over_quota`, `expired` - soft limits; offer to investigate, never pause unasked.
   - `bandwidth` - a server is near its monthly plan.

## Is someone sharing their link?

1. `find_sharing {days: 1}` (or 7 for a slower look) - ranked suspects with reasons.
2. For a suspect, `ip_history {user_id, days: 7}` and `get_user {user_id}`.
3. Judge carefully and explain your reasoning:
   - **Strong signs**: several IPs online *at the same time* in different countries or very different
     networks (ASNs); a steady stream of new networks every day; more simultaneous IPs than the user
     has devices.
   - **Weak signs / normal**: a phone hopping between mobile IPs of the same carrier (same ASN,
     changing IPs); home + mobile + office; travel (one country after another, not at once). Mobile
     carriers' CGNAT can also put different people behind one IP.
4. Offer proportionate options, in order: talk to the user; set or lower the IP limit
   (`update_user`, no disruption); **issue a new link** if the link leaked; **reset credentials** if
   configs were copied (all devices must refresh); **pause** as the last step. Each disruptive one
   needs a confirmation.

## Where does the traffic go?

`destinations {user_id, days}` or across everything with `server_id`. Xray and Hysteria2 report
connections per destination; WireGuard reports exact bytes (names need "log the names devices look
up" on the WireGuard protocol). Summarise by site or category, largest first. If you are asked to
judge whether use is acceptable, state facts and let the user decide.

## Abuse from an IP or a country

1. Confirm with `online_now` / `ip_history {query: "<ip prefix>"}` what the IP did.
2. Propose `block_ip {ip, hours, reason}` - it drops the IP's connections to Meridian's ports on
   every server (never SSH). Prefer a time limit (24 h) over a permanent block. Needs confirmation.
3. `list_blocked_ips` and `unblock_ip` to review or undo.
4. Whole countries: `get_access` shows devices connected now by country; `set_country_rule {mode:
   "block", countries: [...]}` cuts their connections at once on every server that follows the
   global rule - say how many devices that disconnects (from `get_access`) before asking. The rule
   for who may open the site itself can only be changed in the panel.

## New people

`create_user {name, quota_gb?, expires_on?, ip_limit?, server_ids?, count?, sign_in?}`. Suggest an
`ip_limit` matching the person's devices (2-3) - it only alerts. The result includes the username
and a generated password (shown once) - pass them on with the sign-in address from
`get_status_page` (`user_sign_in`). Forgotten password: `new_user_password {user_id}`.

## New servers and protocols

- **Servers**: `add_server {name, address?, protocols?}` returns the install command; tell the user
  to run it on the server as root, and that it contains the server's secret. Default protocols:
  `vless` (REALITY) + `hysteria2`; add `wireguard` for laptops/offices, `shadowsocks` for Surge or
  Quantumult X users. An IP as `address` also places the server on the map (DB-IP).
- **NAT servers, LXC and Incus containers** (the provider lists the ports that reach the server):
  pass `public_ports` to `add_server`, or `set_server_ports {server_id, public_ports}` later, exactly
  as the provider lists them - `20000-20019`, `40001-40010:10001-10010` (public:server) when the
  numbers differ, `/tcp` or `/udp` when only one is forwarded. Then leave `port` out of
  `add_protocol` and `add_forward`: they pick a forwarded port, and links carry the public number.
  `get_server` lists under `limits` what cannot be reached as configured.
- **Several IP addresses on a server**: `get_server` lists them (`addrs`); `add_protocol` with
  `bind_ip` gives a protocol one of them - it listens there, its traffic leaves from there, links
  use it, and protocols on different addresses may share a port.
- **IPv4 or IPv6 only**: `update_server {server_id, ip_version: "ipv4" | "ipv6" | "both"}`.
- **What a user gets**: `create_user` / `update_user` with `server_ids` (whole servers, with protocols
  added later) and/or `protocol_ids` (single protocols, ids from `list_servers`). For every server,
  pass `everything: true`. In `update_user`, leaving both out keeps the current access; two empty
  lists are refused (to stop someone's access, pause them).
- **A relay whose exit nobody should use directly**: set `pass_only: true` on the exit protocol - it
  leaves users' links and accepts only the pass.
- **Shared certificates**: `list_certificates` shows each one and, per server, whether it serves it
  (`live`), holds it (`installed`: Xray loads it within ten minutes), or has not taken it (`pending`).
  After a renewal, `replace_certificate {cert_id, cert_pem, key_pem}` updates every server at once.
- **Configuration code**: for one protocol, `set_protocol_code {protocol_id, code}` - Xray JSON merged
  into its inbound (`sniffing`, `streamSettings.sockopt`, ...), `outbounds` to add (own tags) and
  `rules` for its traffic only; Hysteria2 takes YAML (it restarts). For a whole server,
  `update_server {server_id, xray_code}` merges the operator's own Xray JSON on top (outbounds,
  routing rules first, inbounds by tag `n<id>`, dns...). An empty string removes the code. Only the
  syntax is checked: afterwards read `apply_errors` in `get_server` - a refusal leaves the running
  configuration as it was.
- **Protocols**: draft with `check_protocol` first - it says whether the combination works, what to
  change if not, and which apps can use it (for a change to an existing protocol pass `protocol_id`:
  what you leave out keeps its value, exactly as saving does). Then `add_protocol {server_id, kind, ...}` (applied
  live). Behind Cloudflare: `transport: "ws"`, `security: "none"`, `cdn: true`, `cdn_host`. TLS with a
  real certificate: `cert_mode: "acme"` with a domain pointing at the server. For proxy pass, set
  `exit_protocol_id` to a protocol on another server.
- **An existing setup** (Xray, V2Ray, x-ui, 3x-ui, sing-box, Hysteria2 already on the server):
  `scan_server`, wait for `action_status` done, `get_scan`, then `import_protocols {server_id, items,
  take_over?}`. Without `take_over` nothing is stopped; with it the old service stops and its users'
  devices reconnect to Meridian on the same ports - confirm first.
- **Forwards**: `add_forward {server_id, target: "ip:port"}`; the kernel engine needs an IP.

## The status page

Visitors see only a sign-in there, users their own page, and the supervisor every server on a live
globe. `get_status_page` shows how it is set up. `set_status_page {mode: "home" | "page" | "off",
domain?, about?, panel_city?}` changes it;
`set_server_on_status_page {server_id, shown?, public_name?, city?}` hides a server, renames it there
or fixes where it sits on the globe (`city: "auto"` returns to the IP database). The sign-in page's
text is public: never put anything private in it.

`set_branding {name?, animation?, logo_svg? | logo_base64? | reset_logo?}` sets what users see: the
panel's name, its logo and how the logo moves (assemble, rise, pulse, spin, none). A logo with
scripts, links or outside resources is refused - tell the user what the answer says to remove.

## Answering style

Lead with the answer, then the evidence (numbers, places, times), then options. Keep it short. Name
users and servers as the panel does. If data is missing (logging off, server offline), say so
instead of guessing.
