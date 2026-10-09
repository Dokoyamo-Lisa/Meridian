---
name: meridian-admin
description: Operate a Meridian proxy/VPN panel through its MCP tools - check health, onboard servers and protocols, import existing setups, add and manage users and their sign-ins, investigate link sharing and abuse, see where traffic goes, split traffic with rules, load balancers and external nodes (imported proxies), set country rules and the status page, review signs of a break-in on the servers (health risks), set up the Telegram bot, and handle alerts safely. Use whenever the user asks about their Meridian servers, users, connected IPs, traffic, routing, blocks, country rules or status page, and the umbrella MCP server is connected.
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
   `set_protocol_enabled`, `remove_protocol`, `remove_forward`, `block_ip`, `server_action`,
   `remove_external_node`) disconnect people, stop services, block traffic or cannot be undone.
   Before calling one, tell the user exactly what will happen (who is disconnected, what stops
   working, how to undo it) and wait for them to confirm in this conversation. Only then pass
   `confirm=true`. A confirmation covers one action, not similar ones later. Some changes need it
   only sometimes (`update_protocol` when devices must refresh or Hysteria2 restarts,
   `update_server` when the IP version restarts Hysteria2, `set_protocol_code` and
   `replace_certificate` for Hysteria2, `update_external_node` when turning off a node in use
   blocks traffic or a new link moves it, `set_traffic_rule` for a rule that blocks everything): the
   tool then answers with what would happen instead of doing it - tell the user, and call again with
   `confirm=true` only after their yes.
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
   - `health_risk` - a server's health check found something high or critical that is still open (a
     crypto-miner, a new SSH key or account, a program run from a temporary folder...). See "Signs of a
     break-in" below.
   - `over_ip_limit`, `over_quota`, `expired` - soft limits; offer to investigate, never pause unasked.
   - `bandwidth` - a server is near its monthly plan.
   - `panel_trouble` - a server keeps losing the panel (offline again and again although it is up:
     a poor route to the panel). Offer to let it reach the panel through another server - see
     "A server that keeps losing the panel" below.
   - `relay_failed` - a server's relay could not be reached; it reaches the panel directly meanwhile.
     Check that the relay server is online and that a firewall at its provider lets its `relay_port`
     (TCP, in `get_server` of the relay) in.

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

## Signs of a break-in (health risks)

Every agent (1.0 and later) checks its server every five minutes: crypto-miners, programs run from
temporary folders, new ports, accounts and SSH keys, changed administrator rights, scheduled tasks and
services, kernel modules, SSH sign-ins, traffic Meridian does not account for, Meridian's own programs
changed. What it finds are *risks*. They only tell - nothing was stopped, and you cannot run commands
on the server through the panel.

1. `list_risks` - the open risks, most serious first. Narrow it with `severity: "high"`, `server_id`,
   or see decided ones with `status: "all"`. Expected answer (an empty list `[]` means nothing is open):

   ```json
   [{"id": 12, "server_id": 3, "server": "Tokyo", "key": "port:tcp:8080", "kind": "port",
     "severity": "warning", "title": "A new port is open: 8080/tcp",
     "detail": "Process 4242 runs /usr/bin/python3 as root, listening on all addresses. ...",
     "first_seen": 1791500000, "last_seen": 1791500300, "count": 1, "active": true,
     "status": "open", "decided_by": "", "decided_at": 0, "expected_everywhere": false}]
   ```

2. Explain each in one line, most serious first, with what the user can look at on the server:
   - `critical` (a miner, `/etc/ld.so.preload`, a second uid-0 account, the agent not this panel's
     build, Hysteria not the published release) - treat it as a break-in until the user shows
     otherwise;
   - `high` (a new SSH key or account that can sign in, a program from `/tmp` or deleted, sudoers
     changed, a password sign-in as root, far more traffic than Meridian carries) - likely serious;
   - `warning` and `info` (new ports, services, modules, scheduled tasks, sign-ins with a key) - often
     the user's own doing.
   `active: true` means it still holds (the process runs, the port is open).
3. Ask whether it is theirs. **Never decide on your own**: a risk may be a real break-in, and marking
   it expected hides it for good.
   - Theirs, and it may come back (their port, their key, their service):
     `decide_risk {risk_id: 12, decision: "expected"}` - expected answer
     `{"risk": {..., "status": "expected", "decided_by": "Owner (API token ...)"}, "changed": 1}`.
   - Theirs on every server (say first: it is then never flagged on any server, including servers
     added later; wait for their yes): `decide_risk {risk_id: 12, decision: "expected", scope: "all",
     confirm: true}`. Without `confirm` the tool answers with what it would do instead.
   - Seen, but flag it again if it happens again (a one-off change, a password they changed
     themselves): `decide_risk {risk_id: 12, decision: "acknowledged"}`.
   - To undo either: `decision: "open"`.
4. If it looks like a break-in, say so plainly and give the steps from the documentation (docs/health.md,
   "If a server was broken into"): stop the process, remove the SSH keys, accounts, scheduled tasks and
   services it added, change passwords, turn SSH passwords off, and reinstall the server when unsure.
5. When it fails:
   - `decide_risk` is missing, or answers "This token is read-only" - the token cannot decide; the user
     needs a full-access token (Settings › API & MCP).
   - "decision is expected, acknowledged or open" or "scope is server or all" - fix the argument.
   - "not found" - wrong id, or the server was deleted: `list_risks` again.
   - A server never has risks and `get_server` shows `agent_version` below 1.0 - its agent does not
     check yet: offer to upgrade it (nobody is disconnected) and, after the user's yes,
     `server_action {server_id, action: "upgrade_agent", confirm: true}`.

## The Telegram bot

`get_notifications` shows the bot: `telegram_commands` (it answers `/status`, `/servers`, `/traffic`,
`/users`, `/risks` ... in the chat), `telegram_report` and `telegram_report_hour` (a daily report),
`telegram_changes` (buttons and `/pause` from Telegram), `telegram_users` and `telegram_bot_status`.

1. Telegram must be set up first (`telegram_token` and `telegram_chat`; see `set_notifications`).
2. `set_notifications {commands: true}` makes the bot answer commands; `set_notifications
   {daily_report: true, report_hour: 9}` sends a report every day at 09:00 (the panel's time zone).
   Expected answer: the notification settings with `telegram_commands: true` / `telegram_report: true`.
   "set up the Telegram bot token and chat first" means step 1 is missing.
3. A minute later, `get_notifications`: `telegram_bot_status` should be `"listening"`. Otherwise it
   says why - "the bot token was refused by Telegram" (copy the token again from @BotFather),
   "another program reads this bot's messages" (the bot has a webhook or a second panel reads it:
   Meridian needs a bot of its own).
4. Allowing changes from Telegram, and who may use the bot, are set only by the user in the panel
   (Settings › Notifications › Telegram bot) - never through MCP. If asked, say where.

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
  `rules` for its traffic only; Hysteria2 takes YAML (it restarts). Never change how apps connect
  there (transport, security, ports): links do not follow code, so every device would stop working -
  use `update_protocol` for that. For a whole server,
  `update_server {server_id, xray_code}` merges the operator's own Xray JSON on top (outbounds,
  routing rules first, inbounds by tag `n<id>`, dns...). An empty string removes the code. Only the
  syntax is checked: afterwards read `apply_errors` in `get_server` - a refusal leaves the running
  configuration as it was.
- **Protocols**: draft with `check_protocol` first - it says whether the combination works, what to
  change if not, and which apps can use it (for a change to an existing protocol pass `protocol_id`:
  what you leave out keeps its value, exactly as saving does). Then `add_protocol {server_id, kind, ...}` (applied
  live) for a new one, or `update_protocol {protocol_id, ...}` to change one in place - never add a
  second protocol to change the first: it would land in every user's link. Behind Cloudflare:
  `transport: "ws"`, `security: "none"`, `cdn: true`, `cdn_host`. TLS with a real certificate:
  `cert_mode: "acme"` with a domain pointing at the server. For proxy pass, set `exit_protocol_id` to
  a protocol on another server; that exit may pass on once more (a relay), so a chain has two passes
  at most, each to another server - get_server's `pass_name` shows the chain. `remove_protocol`
  removes one (destructive).
- **An existing setup** (Xray, V2Ray, x-ui, 3x-ui, sing-box, Hysteria2 already on the server):
  `scan_server`, wait for `action_status` done, `get_scan`, then `import_protocols {server_id, items,
  take_over?}`. Without `take_over` nothing is stopped; with it the old service stops and its users'
  devices reconnect to Meridian on the same ports - confirm first.
- **Forwards**: `add_forward {server_id, target: "ip:port"}`; the kernel engine needs an IP.

## Traffic splitting and external nodes

Traffic rules decide where users' traffic leaves the internet. The first rule that matches sends it
directly (from the server the user is connected to), through a protocol on another server, through
an **external node** (a provider's or a friend's proxy imported into the panel), to a **load
balancer**, or blocks it; what no rule takes leaves as before. Rules apply within seconds and restart
nothing. They split only Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP) - never
Hysteria2 or WireGuard - and traffic passing through a server from other servers never follows that
server's rules.

**Look first.** `get_routing` returns `rules` in order (each `target` is `direct`, `block`,
`node:<protocol id>`, `ext:<external node id>` or `lb:<load balancer id>`), `balancers`, `exits`
(every target a rule can use, with its name, e.g. `{"target": "node:12", "name": "Tokyo · REALITY"}`)
and `problems`. `list_external_nodes` shows each node with `used_by`.

**Send some sites elsewhere** ("ChatGPT and Claude through Tokyo"):

1. `get_routing` → pick the target from `exits`. No protocol on that server yet? `add_protocol` first.
2. `set_traffic_rule {name: "AI sites", sites: ["openai", "anthropic"], target: "node:12"}`.
   Expected: the routing view, with the new rule last in `rules`.
3. Mind the order: the first match wins. If an earlier rule takes the same traffic (an "everything"
   rule, say), add with `before_rule_id`, or `order_traffic_rules {rule_ids: [...]}` listing every id.
4. Read `problems` in the answer. Empty: tell the user it applies within seconds on every server it
   covers. Otherwise each line says on which servers and why that traffic is blocked - see *When
   something does not work* below.

What a rule matches (giving any of these replaces the rule's whole match):

- `sites`: Xray's site list names - `openai`, `anthropic`, `google`, `youtube`, `netflix`,
  `telegram`, `cn`, `category-ads-all` ... (`get_routing` lists common ones in `sites`).
- `domains`: `example.com` (with its subdomains), `full:www.example.com`, `keyword:example`,
  `regexp:^ad\.`.
- `countries` and `ips` match only connections made to an address. Most apps ask for names, so pair a
  country with its site list: `sites: ["cn"], countries: ["CN"]`.
- `ports` (`"443"`, `"80,443,8000-9000"`), `network` (`tcp` or `udp`), `bittorrent: true`, or
  `everything: true` alone.
- Where it applies: `servers` or `protocols` (ids from `list_servers`, Xray protocols only), not
  both; neither = every server.

Examples: Chinese sites directly - `{name: "China directly", sites: ["cn"], countries: ["CN"],
target: "direct"}`, first in the list; ads blocked - `{name: "Ads", sites: ["category-ads-all"],
target: "block"}`. A rule that blocks *everything* answers with what it would do instead of saving
it: tell the user that everyone on those protocols could reach nothing, and only after their yes call
again with `confirm=true`.

**A provider's or a friend's proxy (external node):**

1. `import_external_nodes {url: "https://..."}` (a subscription address, fetched once, HTTPS only) or
   `{text: "..."}` (share links one per line, a subscription's base64 content, or Clash YAML).
   Expected: `added` (names, ids) and `skipped` (each with a `reason`). Relay the reasons plainly:
   nodes that turn certificate checks off or send traffic unencrypted are refused on purpose -
   suggest asking the provider for a TLS or REALITY node; TUIC, AnyTLS and the like cannot be used
   by Xray. Names that were taken get a number (`HK 2`).
2. Never print links or credentials; no tool shows them again anyway.
3. Use the node: as a rule's `target` (`ext:<id>`), as a load balancer member, or as a protocol's
   proxy pass - `update_protocol {protocol_id, exit_external_node_id: <id>}`. A pass changes where
   that protocol's users appear on the internet: say so before doing it.
4. Not sure it works? `check_external_node {node_id, server_id}` - the server opens a connection (and
   a TLS handshake for TLS and REALITY nodes). Expected: `Tokyo reaches HK at 203.0.113.5:443 in
   41 ms, with a valid certificate for its server name.` A failure says why (`refused - nothing
   listens on that port`, `the name cannot be found`, `the TLS handshake for ... failed`). `still
   running` → ask `action_status` with the action id it gives; `older than 1.0` → that server's
   agent needs `server_action upgrade_agent` (nobody is disconnected; confirm first). Hysteria2 and
   WireGuard nodes speak UDP and cannot be checked this way.
5. Turning a node off blocks the traffic of everything that uses it (protocols passing through it,
   rules sending traffic there) until it is on again - it never leaves directly instead.
   `update_external_node {node_id, enabled: false}` then answers with who is affected: tell the user,
   call again with `confirm=true` after their yes. `remove_external_node` always needs `confirm=true`;
   its answer lists what is `blocked` now.

**Load balancers** (several exits sharing one rule's traffic):

1. `set_load_balancer {name: "Streaming", strategy: "roundRobin", members: ["node:12", "ext:3",
   "direct"]}` - `random`, `roundRobin` (taking turns) or `leastPing` (the fastest; then also
   `fallback: "block"` or `"direct"`: where traffic goes when no member answers). `direct` is the
   server itself.
2. `set_traffic_rule {sites: ["netflix", "youtube"], target: "lb:<id>"}`.
3. `leastPing` needs Xray's latency checks, which start with one Xray restart on each server that
   uses it: `overview` then shows `restart_pending` there, and it picks at random until a person
   restarts (**Restart now**, or `server_action restart_pending` after they confirm - it disconnects
   that server's Xray users for a moment). Say so; never restart unasked.
4. A load balancer that a rule uses cannot be removed: send that rule elsewhere first.

**When something does not work** - `problems` in `get_routing`, `route_notes` per server in
`get_server` and `list_servers`; what cannot be used is blocked there, never sent out directly:

- `Rule "X" does not work: its external node (N) is turned off` (or `was removed`, or `the exit (...)
  is turned off`, `... passes on ...`) → turn the exit on, or send the rule elsewhere:
  `set_traffic_rule {rule_id, target: ...}`.
- `Load balancer "L" has no member it can use` / `leaves out a member` → turn members on, or change
  them with `set_load_balancer {balancer_id, members: [...]}`.
- `applies only to servers or protocols that were removed` → `set_traffic_rule {rule_id, servers:
  [...]}` (or `protocols`), or `remove_traffic_rule`.
- `reaches no Xray protocol that is turned on` → those servers have only Hysteria2 or WireGuard, or
  their Xray protocols are off.
- `Xray refuses a site list or country name (...)` → a typo in `sites`: fix the rule; until then those
  servers keep their previous configuration.

Usage is counted where users connect, wherever their traffic leaves. At an external node Meridian sees
nothing - not its bandwidth, its quota or whether it is up (beyond `check_external_node`).

## A server that keeps losing the panel

A server whose route to the panel is poor (often one in another country than the panel) drops off
again and again although it and its users are fine. It can reach the panel through another of the
user's servers with a good route to both - a relay. Nothing restarts and nobody is disconnected, so
this needs no confirmation; but which server relays is the user's choice - ask.

1. `list_servers`. The server in question has `panel_trouble` set, e.g. *"Lost the panel 4 times in
   the last 24 hours"*. Candidates for the relay: other servers with `status: "online"`, an empty
   `panel_trouble`, `panel_relay: 0` (a relay must reach the panel directly) and no `relay_name`;
   `get_server` on a candidate must show `caps.relay: true` (agent 1.0 or later).
2. Propose one or two candidates - ideally near the troubled server or with a known good route to
   the panel - and let the user pick.
3. `set_panel_relay {server_id, relay_server_id}`. Expected answer:
   `{"id": 7, "name": "Shanghai-2", "panel_relay": 3, "relay_name": "Tokyo", "relay_port": 41234, ...}`.
4. Tell the user: the relay listens on TCP `relay_port`; if a firewall (security group) at the relay's
   provider filters what comes in, that port must be let in.
5. After a minute or two, `get_server {server_id}`: `panel_path` should be `"relay"`. If it is
   `"direct"` with a `relay_error`, the agent could not use the relay and went directly (it keeps
   working):
   - *refused* - the relay's agent is not running, or its firewall closes the port;
   - *closed the connection* - the relay does not know the server's address yet (wait a minute and
     look again), or cannot reach the panel itself;
   - *no answer* - a firewall drops the port.
6. If `set_panel_relay` refuses, its message says why and what to do:
   - "... agent cannot relay yet" or "cannot reach the panel through a relay yet" - that agent is
     older than 1.0: offer to upgrade it (nobody is disconnected), and after the user's yes call
     `server_action {server_id, action: "upgrade_agent", confirm: true}`; once `action_status` says
     done, try again;
   - "... reaches the panel through another server itself" - choose another relay (one hop only);
   - "... passes X through to the panel" - the server is a relay for others and must stay direct;
   - "no free port left on ..." - the relay is a NAT server with every forwarded port taken: free one
     or choose another relay.
7. Back to directly: `set_panel_relay {server_id, relay_server_id: 0}`.

The panel can also do this by itself: **Settings › Panel › Monitoring › When a server keeps losing
the panel, relay it through** (`auto_relay` in the settings, not an MCP tool) moves such a server to
the named server once and notifies the user. If the user asks for that, tell them where it is.

## The status page

By default visitors see every server there - on a live globe and in detail: place, up or down,
load, memory, disk, bandwidth, traffic and the day its paid period ends (never IP addresses unless
turned on; never prices, users, protocols or ports) - and sign in from the top-right button; users
then see their own page, the supervisor everything else. `get_status_page` shows how it is set up
(`status_public`: visitors see the servers; `status_ips`: the page shows their IP addresses, to
everyone who opens it, the supervisor included). `set_status_page {mode: "home" | "page" | "off",
public?, show_ips?, domain?, about?, panel_city?}` changes it: `show_ips: true` publishes every
server's addresses - only when the operator asks for exactly that, after saying anyone can then find
and block the servers; `public: false` leaves visitors only the sign-in. Answer with what visitors
will now see, from the reply's `status_public` and `status_ips`;
`set_server_on_status_page {server_id, shown?, public_name?, city?}` hides a server, renames it there
or fixes where it sits on the globe (`city: "auto"` returns to the IP database). The sign-in page's
text is public: never put anything private in it.

`set_branding {name?, animation?, logo_svg? | logo_base64? | reset_logo?}` sets what users see: the
panel's name, its logo and how the logo moves (assemble, rise, pulse, spin, none). A logo with
scripts, links or outside resources is refused - tell the user what the answer says to remove.

## Plugins

Plugins are the operator's own additions: styles and scripts for the panel and the status page, and
programs beside the panel that can filter what servers run and what users' apps receive and add API
routes, pages, MCP tools and timers. You can see them, never change them: installing, updating,
turning on or off and removing happen only in the panel (Settings › Plugins) or on the panel's host.

1. **"Which plugins are there?" / "Is the X plugin working?"** → `list_plugins`. Expected: a list
   with `id`, `name`, `version`, `enabled`, `state` and `warnings`. Report `state` in words: `running`
   (its program is up), `on` (styles and scripts only), `starting`, `restarting` (its program crashed
   and starts again shortly), `off`, `held` (the panel was started without plugins -
   `started_without_plugins` is true), `broken` (its files are damaged: upload it again). If
   `last_error` is set, quote it and say what it means. An empty list means no plugins are installed.
2. **A plugin's own tools** appear in your tool list as `<plugin id>__<tool>` (for example
   `hello__summary`); their description ends with "(from the plugin ...)". Use them like the panel's
   own - read the description first, and the same confirmation rule applies to destructive ones. They
   are only as reliable as the plugin: if one fails, say which plugin it came from.
3. **"Install / turn on / remove this plugin"** → you cannot. Tell the user to do it in the panel
   under Settings › Plugins (Upload a plugin, then Turn on), and to read the list of what it may do
   before agreeing: a plugin that is on can do whatever it asks for, and a server plugin can read
   every key and password Meridian holds. Never suggest a plugin from a source the user does not
   trust; [docs/plugins.md](../../docs/plugins.md) explains every permission.
4. **"The panel broke after I turned on a plugin"** (blank pages, errors everywhere, servers that
   stopped working right after a plugin started):
   - `list_plugins` (if MCP still answers) to find plugins that are on - the newest one first.
   - Tell the user to turn it off on the panel's host:
     `sudo -u meridian meridian plugins disable <id> --data /var/lib/meridian` - expected output
     `The plugin <id> is off.`; the running panel follows within a few seconds, no restart.
   - If the panel does not start or answer at all: add `MERIDIAN_NO_PLUGINS=1` to
     `/etc/meridian/meridian.env` and run `sudo systemctl restart meridian` - the panel then runs
     without any plugin (proxies keep running during the restart). Remove the line and restart again
     once the bad plugin is off or removed.
   - Servers whose configuration a plugin changed get the panel's own configuration back as soon as
     the plugin is off: say that this can disconnect whatever depended on the plugin's changes.

## Answering style

Lead with the answer, then the evidence (numbers, places, times), then options. Keep it short. Name
users and servers as the panel does. If data is missing (logging off, server offline), say so
instead of guessing.
