# Changelog

## 1.0.0 - 2026-10-09

**Upgrading keeps everything as it is**: an existing panel keeps its SQLite database (move it to
PostgreSQL whenever you like: `meridian db to-postgres`), and agents from 0.7 keep working - upgrade
them for the new features (Settings › Updates › Upgrade all agents - nobody is disconnected).
**One change of behaviour**: changes from Telegram (pausing users, deciding about health risks) now
need a listed Telegram user or your own linked Telegram account - an empty list no longer lets
everyone in the chat make changes.

### New

- **PostgreSQL**: a new panel keeps its data in PostgreSQL, installed from the distribution's
  packages and reached over its local socket without a password (`--database sqlite` keeps SQLite).
  `meridian db status`, `meridian db to-postgres URL` and `meridian db to-sqlite` move the data either
  way, with the counts checked and the old database kept. Backups are SQLite files either way and
  restore into either. Settings › Updates shows the database.
- **A server's history on the status page**: a server's details have charts of processor, memory,
  disk, disk activity, network, load, connections and temperature (where the host has sensors), from
  an hour to 30 days, as areas, lines or bars, smoothed or as measured, with the time a server did
  not report hatched; hovering one chart shows the same moment on all of them. Each viewer picks the
  charts they want; you decide which visitors get (Settings › Panel › Status page). An icon for each
  operating system, what the host runs in (KVM, Xen, OpenVZ, LXC ...) and its kernel.
- **Ping monitors** (Monitor › Ping): the servers measure the way to addresses you choose, every 10
  seconds to an hour - three ICMP echoes or three TCP connections a round. Rounds measured while a
  server cannot reach the panel arrive later, so no gap is left that the server did not have. Their
  charts are in each server's details (visitors see public monitors by name, never their address);
  `/ping` in the Telegram bot.
- **Telegram for users**: users link up to two Telegram accounts - signing in once in the bot's Mini
  App (the password goes to the panel, never through the chat) or with a code from their page - and
  ask the bot for their data left (`/usage`) and devices (`/devices`), or open their page in
  Telegram. They can unlink one a month; you can unlink any at any time (Settings › Notifications).
- **The panel in Telegram**: your own linked Telegram accounts open the panel in the Mini App and use
  the bot's commands in a private chat (allowed only from a signed-in browser). A sign-in from
  Telegram cannot change passwords, two-factor, API tokens, sessions, the console, plugins or the
  site rule. Sign-ins from Telegram are guarded like the sign-in page.
- **Shared servers**: a server can report to up to two more panels. The other panel adds it as
  shared and gives a code; with it the agent runs that panel's protocols, users, forwards and rules
  too - everything but the console, upgrades, the country rule and relaying, which stay with the
  server's own panel. Ports and networks in use are refused to the other panel.
- **Limits per protocol**: at most so much data through one protocol each cycle, for a user or in a
  plan, with alerts when one is used up - or the protocol stops serving them until the cycle starts
  over. The users' circles show what is left: full at the start of a cycle, running down.
- **Traffic in colour**: users' charts show what each protocol (or server) carried.
- **Subscription links**: a provider's subscription read on a schedule; its nodes work as exits, in
  load balancers and for users.
- **The console**: a root shell on each server, in the panel, for the supervisor only (with the
  password asked again).
- **Backups**: download, or WebDAV and S3 on a schedule (encrypted), and restore from the panel.
- **Users**: what counts toward the quota, a start date, resets every N days, speed and device limits
  enforced by the servers, preset plans.
- **Sign-in protection**: escalating shut-outs, no lockout of the owner, Cloudflare Turnstile on the
  panel's own pages, and maintenance mode.
- **Your own styles**: CSS for the panel and for the status page and users' pages.
- **Status page switches**: whether visitors and users get the overview and the events.
- **Protocols page**: search, filters, and choosing who gets each protocol in a few clicks.
- Also in 1.0 (merged earlier): agent relays and a WebSocket link to the panel, health checks and the
  Telegram bot, plugins, traffic splitting with external nodes, dynamic DNS and IPv6-only servers,
  VLESS Encryption, Hysteria2 port hopping and Trojan with REALITY.

### Security

- Changes from Telegram need an explicitly allowed account (see above).
- The health check reads only regular files, opened without blocking: an `authorized_keys` turned
  into a FIFO can no longer hang it, and a linked one is followed only to another `authorized_keys`.
- A server shared with the panel cannot be set to use or to be a relay (its agent ignored it).

### API and MCP

New endpoints: ping monitors (`/api/ping-monitors`), a server's charts (`/api/servers/{id}/series`,
and for the status page `/api/status/servers/{id}/series`), Telegram links (`/api/telegram/*`,
`/api/portal/telegram*`, the Mini App's `/api/tg/*`); `status_charts`, `telegram_user_link` and
`telegram_panel` in the settings; `database` in `GET /api/update`. MCP tools: `list_ping_monitors`,
`set_ping_monitor`, `remove_ping_monitor`, `server_charts`, `list_telegram_links`,
`unlink_telegram`; `set_status_page` takes `charts`, `set_notifications` takes `user_link`.

## 0.7.4 - 2026-10-08

**Security: upgrade the panel and the agents** (Settings › Updates › Upgrade all agents - nobody is
disconnected). Meridian is now built with Go 1.27.2 and golang.org/x/net 0.60.0, which fix twelve
published issues in Go's HTTP server and client, HTTP/2, TLS, header parsing and HTML templates
(GO-2026-6599, 6600, 6603, 6605, 6607 to 6613 and 6617). `go.mod` names the toolchain, so builds
from source use it.

- **IP addresses are off the status page by default**, and the switch now applies to everyone who
  opens it - you included (the panel always shows them). A panel that never changed the switch
  stops showing addresses when it upgrades; to show them again, turn on Settings › Panel › Status
  page › **Show IP addresses**.

## 0.7.3 - 2026-10-08

The agents have no changes in this version: upgrading them is optional.

- **Every IP address on the status page**: a server's list now also has the addresses its protocols
  give users - an address of their own, or one set for their links. Behind NAT these differ from the
  address the agent finds, which is the one the server reaches the internet from (servers behind
  the same NAT showed only that shared address). For a server behind NAT without protocols, set
  **Address clients connect to** on its page to show where it is reached.

## 0.7.2 - 2026-10-08

**After upgrading, a status page that is turned on shows every server to everyone, IP addresses
included.** To keep the addresses to yourself turn off Settings › Panel › Status page › **Show IP
addresses**; to give visitors only the sign-in, as before, turn off **Show the servers to everyone**.
The agents have no changes in this version: upgrading them is optional.

- **The status page is a probe page**: after the umbrella loads, visitors see every server on the
  live globe and in detail - where it is and its IP addresses, up or down, availability, throughput,
  load, memory, disk, connections, the system it runs, bandwidth and traffic this month and the day
  its paid period ends (the server's Renews on date). Prices, users, protocols and ports are never
  shown. **Sign in** at the top right takes users to their own page, with their own use shown on
  each server too; the supervisor signs in from the same button.
- **More detail per server**: cards show the addresses (click one to copy it), the system, load,
  memory and disk used of total, bandwidth used with what was sent, received and today and when it
  resets, how long the machine has been running, connections and "paid until"; the servers table has
  an Expires column; a server's panel adds its addresses, resources (all three load averages, swap),
  system (OS, architecture, cores, processor, memory, disk, paid until) and what its bandwidth
  counts. A paid period ending within a week, or ended, is shown to the supervisor as needing
  attention.
- API: `GET /api/status` and `/api/status/live` answer visitors while the status page shows the
  servers (`status_public`, and the addresses with `status_ips`); a wrong API token is refused, not
  treated as a visitor. New fields per server: `addrs`, `host`, `cycle`, `expires`, `bandwidth.mode`,
  and in `sys` the load averages, memory, swap and disk used and total, connections and `booted`;
  `supervisor` says whether the supervisor is signed in. MCP: `set_status_page` takes `public` and
  `show_ips`.
- **Linux only**: Meridian runs on Linux - the panel, the agent and the command-line tool. Releases
  no longer include command-line builds for macOS or Windows; to connect an AI assistant from a
  desktop, use the panel's MCP address over HTTPS (docs/mcp.md).

## 0.7.1 - 2026-10-08

Upgrade the agents too (Settings › Updates › Upgrade all agents - nobody is disconnected): several
fixes below are theirs. Apps now name servers by the name users see (Server › Status page › Name
users see) where one is set, so such entries are renamed once in users' apps.

- **What a change does to devices is said before saving**: the protocol window asks "Devices must
  refresh" for every change apps connect with - now also the port, the address, the address override
  and Hysteria2's obfuscation - and a Hysteria2 change that restarts it asks first and says so
  (it used to say nothing restarted). Changing a server's IP version asks before Hysteria2 restarts.
  Making a protocol, or the exit of a proxy pass, serve only proxy passes asks first: its users are
  cut off.
- **MCP: update_protocol and remove_protocol** change or remove a protocol in place (assistants used
  to add a second protocol to change one). update_protocol asks for confirm=true when devices must
  refresh or Hysteria2 restarts. find_sharing looks at the whole period asked for (a link shared last
  week was missed when only one device connected today).
- **WireGuard on the users' page**: each WireGuard tunnel with its file and a QR code for the
  WireGuard app, which does not take the link. The users' page names protocols as their apps do.
- **Certificates**: a self-signed certificate pasted as an own or shared certificate is refused (no
  app can check it - the Self-signed option makes one apps pin); a protocol whose certificate comes
  from a private authority, or lacks its intermediate, says on its card that apps refuse it. An
  imported self-signed certificate is kept and pinned in links. A certificate pasted into a protocol
  (or imported from certbot's files) raises an alert 14 days before it expires - nothing renews it.
  Each shared certificate shows its ID, and the renewal command comes ready for it.
- **Proxy pass through two servers**: an exit may pass on once more, so a chain can have two passes
  (entry › relay › exit, each on another server) - for example a relay nearby in front of a far exit.
  The list of exits shows where a relay leads; a protocol's card shows its whole chain; a relay can
  serve only proxy passes. Longer chains, and chains back to a server they passed, are refused - they
  used to be accepted half-way and then left traffic blocked or going out directly. Removing an exit
  protocol blocks the protocols that passed through it until they get another exit (it used to send
  their users out directly from their own servers), as removing its server does. Rotating a server's
  agent token no longer breaks its proxy passes: they switch to new credentials when the reinstalled
  agent connects.
- **Import** says who must refresh: users of a VLESS protocol with a flow other than the one
  Meridian keeps, devices of a single-user Shadowsocks 2022 server, and when a certificate read from
  files expires (Meridian does not renew it).
- Agent: inbounds from a server's own Xray configuration are no longer closed and reopened on every
  user change; a Hysteria2 protocol that Hysteria refuses to run says why (on the server page, and as
  an apply error naming the protocol) instead of restarting in a loop unnoticed; reports go on while
  a new Xray is downloaded (a slow download marked the server offline); with a country rule or IP
  blocks, the server's own outgoing connections that happened to get a kernel forward's port as
  their source port were dropped at random. Hysteria no longer checks its developers' server for
  updates (versions are the panel's to choose; this takes effect when a Hysteria2 protocol restarts).
- A protocol's own address and Hysteria2's own settings are refused on servers whose agent is older
  than 0.6 (it would leave them out without a word); the server page says what waits for the upgrade.
  A WireGuard protocol with IPv6 on stays editable when its server no longer routes IPv6.
- Status page and users' page: when the panel cannot be reached (it restarts), the page says so and
  tries again by itself instead of showing the sign-in; "Signed out" only when signing out worked;
  a wrong two-factor code keeps the code step; users allowed in from abroad are no longer told the
  site is not available in their region; daily bars show their values on a tap; the dashboard fits
  from 1280 px.
- Panel: Escape closes only the confirmation, not the window under it; "Another site" keeps the
  camouflage's forward address; Manage certificates opens in a new tab and the protocol window keeps
  its draft; form fields are tied to their labels for screen readers; the Access page counts the
  exceptions before warning about a lock-out; the API reference states who may call /mcp and the
  protocol check; ports.sh opens SOCKS5 UDP and the port Let's Encrypt checks. Docs keep private
  keys and tokens off command lines.

## 0.7.0 - 2026-10-08

Upgrade the agents too (Settings › Updates › Upgrade all agents - nobody is disconnected): the
security fixes below are theirs. After the agent upgrade, servers with Hysteria2 protocols show
"Waiting for a restart" once, for its stricter blocks: click Restart now when convenient.

- **The panel updates itself.** Settings › Updates shows the newest release and installs it with one
  click: the panel downloads it, checks that it carries Meridian's release signature (a key built
  into the panel; a release without it is never installed) and its checksum, backs up its database,
  and a small updater service installs it - checking all of it again - with the release's own
  install-panel.sh. If the new version does not stay up, the previous one is put back by itself.
  Proxies keep running; the panel restarts once. **Install new releases by themselves** does the same
  at night (03:00-05:00 panel time). Run install-panel.sh --upgrade once from this release to set up
  the updater. New MCP tools: check_updates, update_panel.
- **Upgrade all agents** at once (Settings › Updates, or the button on Servers when some are older);
  offline servers upgrade as soon as they connect. Agents restart themselves; nobody is disconnected.
  MCP: upgrade_all_agents.
- **Usage by protocol**: each user's page shows their usage on every protocol - this cycle and all
  time, exact as each server counted it - and users see the same on their own page. Usage so far is
  filled in from the daily records. Also in the API (/api/users/{id} usage) and MCP (get_user).
- **The status page on its own domain** is a choice of its own (Settings › Status page › Its own
  domain): visitors see the sign-in, users their own page and links, and you see every server on
  the live globe when you sign in there. The panel and its API still never open on that domain.
- **Hysteria and realm keep their version** until you upgrade them (server page › More actions ›
  Upgrade Hysteria / Upgrade realm): a new version in Settings used to reach running servers at
  once, and a download that failed held back every other change on them. Now a failed download
  holds back only that core.

- **Security: users can no longer reach the server itself through a proxy.** The block of private
  addresses only matched addresses typed as numbers, so a user who asked for `localhost` (or any
  name that resolves to the server, a private network or the cloud's metadata address) reached them -
  including Xray's API, which runs without a password on loopback. The agent now marks every
  connection Xray makes for users, and the kernel refuses those that lead to this host or to a
  private, link-local or metadata address, whatever name was asked for. Hysteria2 always keeps its
  blocks first, even when a protocol's own settings bring an `acl` (an `acl.file` is refused), and
  also refuses `0.0.0.0` and `::`, which Linux treats as this host. WireGuard devices reach the
  internet only: not private networks, the metadata address, each other, or the server's own
  services (DNS aside). Without nftables, Xray resolves names before its routing rules instead.
  No inbound, fallback, REALITY site or Hysteria2 masquerade may send strangers to the agent's own
  ports.
- **Changes that need a restart wait for one - Hysteria2 too.** An upgraded agent that writes a
  Hysteria2 protocol's configuration differently no longer restarts it: the server page shows what
  waits, and **Restart now** restarts exactly that (Xray only when its own settings wait).
- **`meridian restore`**: restoring a backup by copying it over the database could be undone - or the
  database damaged - by the old write-ahead log, which SQLite lays over the copy on the next start.
  `meridian restore FILE` refuses while the panel runs, checks the backup, removes what must not
  survive and keeps the replaced database. The panel also closes its database cleanly when it stops,
  and two panels can no longer run on the same data directory.
- **Removing a server or protocol takes it out of users' access.** Before, its id stayed behind: such
  users could not be edited any more ("there is no protocol 12"), a user left with nothing could only
  be given everything, and - since ids were handed out again - the next new protocol went to whoever
  had the removed one. Ids are now never reused; a user whose every server and protocol was removed
  has no access (the timeline says so) instead of everything; editing a user leaves their access as
  it is unless it is changed. In MCP, `update_user` with two empty lists is refused - `everything:
  true` says "every server".
- **Timed IP blocks end on time.** "For 1 day" stayed in force until something else changed the
  servers; expired blocks are lifted within seconds and no longer listed or counted.
- **Changing the time zone no longer resets this month's usage** of every user and server.
  Next-reset dates for reset days 29-31 match the day the reset really happens, and "today" /
  "tomorrow" are counted by calendar day in the panel's time zone (the day before a reset said
  "today").
- **WireGuard and forward traffic is counted once across agent restarts and upgrades.** A restarted
  agent counted again everything the kernel still held (weeks of WireGuard traffic, forwards, the
  country rule's refusals) and read the access logs again from the start. Where counting stood is now
  saved with the data, before each report goes out.
- **Live routing changes keep balancers**: with balancers in the server's configuration code, any
  change to the rules wiped them and left Xray with a partial rule list (and the error vanished a
  minute later). Rules now go with the running balancers; a rule for a new balancer waits for the
  restart; a change that could not be applied is retried and keeps being reported.
- VLESS protocols without Vision (WebSocket, gRPC, XHTTP, HTTPUpgrade, CDN) could not be saved any
  more: the form turned Vision on for every VLESS protocol it opened. A protocol keeps the flow it has;
  a change that leaves no room for Vision takes it away.
- The copy button in "What the server runs for it" saved the protocol and closed the window.
- "Reset credentials" now also retires the credentials a user kept from an import, which kept working.
- Stash: WireGuard got mihomo's key names, so the pre-shared key was dropped and the tunnel never
  connected. Loon: WireGuard on an IPv6 address was written without brackets.
- With the status page on its own domain, users who sign in there get links that work there (they
  answered 404), and `status.example.com.` (with a trailing dot) no longer opens the panel.
- **Notifications** (Settings › Notifications): what needs you reaches you outside the panel - a
  Telegram chat and/or an HTTPS webhook (Slack, Discord and Mattermost work as they are). Choose the
  groups: servers (offline and back, a machine that restarted, a refused configuration, a crashed
  core), users (data used up, access ended or ending within 3 days, too many devices), certificates
  (shared ones expiring within 14 days), and optionally sign-ins and security changes. "Find chats"
  picks the Telegram chat for you; "Send a test message" checks each channel. Events go out in order,
  never twice, and never from before notifications were on; nothing is ever paused by them. The
  timeline now also records used-up quotas, ended and ending access, and expiring certificates. New
  MCP tools: get_notifications, set_notifications, test_notifications.
- **A protocol bound to an address the server does not have no longer takes Xray down**: after a
  reboot (or when an address was removed), Xray refused to start at all and every protocol on the
  server stayed down. The agent now leaves out only that protocol, says so on the server's page (by
  name), and adds it back by itself, live, as soon as the address returns. Agents report a change of
  addresses at once instead of within 10 minutes.
- **Proxy pass never leaks to the entry's own address**: while an exit is turned off (or its server
  was removed) the entry blocks its traffic instead of quietly leaving from the entry server - and its
  card says why. Every change to an exit (turned on or off, its port, address or address override)
  now reaches the protocols that pass through it at once; before, only changes to its settings did.
  Turning off or removing an exit names the protocols that pass through it.
- A server that rebooted is reported as rebooted, not as "agent restarted (traffic was not affected)".
- A shared certificate that apps will refuse (self-signed, a private CA, or a missing intermediate)
  is flagged on its card: links never pin shared certificates.
- Monitor and users' pages name protocols as their cards do ("REALITY", "VLESS gRPC TLS" or their own
  name) instead of "VLESS"; "1 IP on 1 user" instead of "1 IPs on 1 users"; a subscription page says
  "2 servers, 34 ways to connect" instead of "34 servers available".
- Phone width: the server page's section buttons and a certificate's fingerprint no longer push the
  page wider than the screen.
- A reused two-factor code is explained ("already used - wait for the next one") instead of being
  called wrong.

- **Advanced settings have their own place**: a protocol's window shows its form on the left and its
  advanced settings (Xray JSON or Hysteria2 YAML) on the right, switched on and off there. They take
  precedence: while they are on, the form's settings are locked and greyed out (port, name, address
  and proxy pass stay editable); turning them off removes them when you save. "What the server runs
  for it" shows the protocol's saved configuration under the editor.
- Saving a protocol without touching how apps connect asked "Devices must refresh" anyway (an unset
  CDN option counted as a change); it now asks only when something changed.

- Ports: editing a running realm forward (or giving a protocol its own address, or turning on SOCKS5
  UDP) failed with "port in use by another program" - the program was the forward or protocol itself.
- Switching a TLS protocol to REALITY kept its TLS domain as the camouflage (often this server's
  own, so REALITY handed every stranger to itself); a well-known site is used now, and the server
  tries the usual ones.
- Importing: users matched by name who could only use other servers get the imported protocol (their
  devices were cut off after a takeover); two new users whose names make the same sign-in name both
  import (the import failed); REALITY with eight short ids (3x-ui's default) imports; an inbound that
  listened only on loopback (behind nginx) is no longer opened to the internet.
- Throughput charts: the status page's total showed one server at a time, and the overview counted a
  server once per report (about three times the real throughput).
- Proxy pass: a server's first contact, a new address, and an exit's automatic camouflage switch now
  reach the protocols that pass through it, and country rules let every address of the panel's
  servers in (a protocol's own address included).
- Surge and sing-box profiles with no protocol the app can use refuse traffic instead of sending
  everything out directly. Hysteria2 speed limits reach apps the right way round. Expiry dates on
  the link's page and in Shadowrocket follow the panel's time zone. Loon has its one-tap import on
  the users' page.
- Security: the agent's install command passes the server's token in the environment, never on a
  command line other users can read; two-factor sign-in cannot be replaced without turning it off
  (which asks for the password); the core mirror fetches only versions the panel uses (anyone may
  ask it); port forwards may not lead to this server or the cloud's metadata, also through a name
  (refused when saved, and checked again by the agent on every pass); MCP calls that restart
  Hysteria2 need confirm=true; get_server no longer hands configuration code to assistants.
- A country rule stays on its servers when the panel restarts before its country database is back
  (it was taken off until something else changed them).
- A shared certificate replaced with the same certificate and its intermediate added now reaches the
  servers. An agent too old for its panel no longer asks for the state in a tight loop. Read-only API
  tokens can check protocols (check_protocol). Addresses of containers, VPNs and tunnels are not
  offered as a protocol's own address.
- Small things: cancelling "Turn off?" no longer says it was turned off; a WireGuard protocol's IPv6
  box can be unticked when the server no longer uses IPv6; a REALITY protocol with its own site
  opens showing it; an empty username keeps a user's sign-in (or makes one from the name) instead of
  removing it; quotas below a GB are kept as they are; Add forward starts with realm on servers
  without nftables; the API example and the user actions in the API reference are right.

## 0.6.2 - 2026-10-07

Panel only: agents stay as they are.

- **WireGuard in Loon**: Loon was sent WireGuard as a `wireguard://` share link, which it does not
  read, so the tunnel never showed up. Loon now gets every protocol in its own proxy format (as its
  manual and Sub-Store write it): WireGuard appears, self-signed certificates are pinned
  (`tls-cert-sha256`), and gRPC, HTTPUpgrade and XHTTP - which Loon's format does not have - are
  listed as not working in Loon instead of being sent. Loon has a one-tap import button now.
- **Shadowsocks 2022 in Surge and Quantumult X**: their lines turned the `=` at the end of the keys
  into `-`, so those protocols could not connect; WebSocket paths with `=` (such as `?ed=2048`) were
  changed the same way. Values now go in as they are; one with a comma, a double quote or a line
  break - which these formats cannot carry - leaves that protocol out with the reason.
- **A protocol that does not work cannot be saved**: the Save button only looked faded (and still
  lit up under the mouse), so "This combination does not work" seemed to be ignored. It is now grey,
  says "Does not work yet - see why", and a click points at the warning instead of doing nothing.
- **The check matches saving**: when editing, the check now knows the stored protocol - leaving the
  key empty to keep the current one no longer shows a false warning (which blocked saving), and the
  server's IP version and shared certificates count when adding. Switching a self-signed protocol to
  "My own certificate" no longer pre-fills (or keeps) the self-signed certificate and key: an
  unpinned self-signed certificate would have failed in every app. The certificate choices wrap
  instead of running out of the form. Assistants can check a change with `check_protocol
  {protocol_id, ...}`.

## 0.6.1 - 2026-10-07

Panel only: agents stay as they are - 0.5 and 0.6 agents both take everything here.

- **Advanced Xray settings for each protocol** (a protocol's *Port, name and more*): JSON, comments
  allowed, merged into that protocol alone - fields of its inbound such as `sniffing`,
  `streamSettings.sockopt` or `fallbacks`, outbounds of your own, and routing rules for its traffic
  only (private addresses stay blocked). Its tag, port and users stay the panel's. Only the syntax is
  checked: what Xray refuses shows on the server's page and the running configuration stays as it
  was. Protocols with their own settings say so on their cards; assistants set them with the new
  `set_protocol_code` MCP tool.
- A protocol's app list put the reason an app cannot use it on top of the app's name; it now sits
  under it.

## 0.6.0 - 2026-10-07

Upgrade agents for the new server-side parts (server page › More actions › Upgrade agent - nobody is
disconnected): own addresses for protocols, IPv6 through WireGuard and shared certificates need
agent 0.6. 0.5 agents keep working with this panel for everything else.

- **What each user gets, per protocol**: a user's access is everything, or any mix of whole servers
  (with protocols added to them later) and single protocols. Their links and the servers carry
  exactly that.
- **One address per protocol**: on a server with several IP addresses each protocol can have its
  own - it listens there, its traffic leaves from there, and links use it, so several protocols can
  each have port 443 on their own address. The agent lists the server's addresses.
- **IPv4 or IPv6 only**: a server's IP version decides how its protocols reach sites, which address
  links use and whether WireGuard routes IPv6. A server whose kernel has IPv6 off is used as IPv4
  only by itself, so applying a configuration never fails over IPv6. Xray takes a change live;
  Hysteria2 protocols restart once.
- **WireGuard carries IPv4 unless asked to carry IPv6**: tunnels used to hand devices an IPv6
  address and route `::/0` that the server never forwarded, so devices' IPv6 went nowhere. Now
  tunnels are IPv4 only by default (devices keep their own IPv6), and **IPv6 through the tunnel**
  really routes it (the agent turns on IPv6 forwarding - keeping routes learnt from router
  advertisements - and NATs it). Every app format follows the tunnel's routes.
- **Shared certificates** (Settings › Certificates): keep a certificate once, use it in protocols on
  any server, replace it once - in the panel or with one API call from a renewal hook - and every
  server gets it: Xray loads it within ten minutes without disconnecting anyone, Hysteria2 restarts
  once. Each server reports what it holds and what each TLS port serves, so the list shows where
  the new one is live. A replacement that drops a domain in use is refused.
- **Configuration as code**: a server's Xray configuration can take your own JSON (comments
  allowed) on top of what the panel generates - outbounds, routing rules (first), changes to a
  protocol by its tag, your own inbounds, dns and more - and a Hysteria2 protocol its own YAML. Only
  the syntax is checked; the cores decide the rest, and a refusal leaves the running configuration
  as it was. "What the server gets" shows the merged result.
- **Proxy pass exits for passes only**: an exit can stop taking direct connections - it leaves users'
  links and accepts only the pass. The entry's proxy pass setting offers it right there.
- **Names in links**: a protocol with a name is listed in apps under that name alone (no server
  prefix). Repeated names are numbered: apps such as Clash refused a list with two equal names.
- WireGuard links fetched while the server was being prepared could give a user keys the server
  never got; keys and addresses are now chosen in one step.

## 0.5.0 - 2026-10-07

The agent contract is unchanged: 0.4 agents keep working with this panel. Upgrade an agent when
convenient (server page › More actions › Upgrade agent - nobody is disconnected); Alpine servers
and servers whose provider decides the ports install the new one anyway.

- **The status page's globe keeps its room**: with a dozen servers the list under it squeezed the
  globe to a sliver on wide screens. Long lists now fold - the servers, resources, bandwidth and
  events show what fits the screen (the first few on phones and tablets) and "Show all" opens the
  rest - so the dashboard is one screen again. The throughput chart has a sensible height and the
  events moved under it, instead of one tall, mostly empty chart on the right. Servers close
  together share one pin on the globe, and DB-IP's district names are left out of places ("Los
  Angeles", not "Los Angeles (Central-Alameda)").
- **Alpine Linux**: the agent runs on Alpine (OpenRC) as well as on every systemd distribution. The
  install command works with busybox's `wget` and plain `sh`; the agent, Xray, Hysteria2 and each
  realm forward run as OpenRC services (`rc-status` lists them), realm as `nobody`. Upgrading the
  agent still disconnects nobody. On Alpine the proxies work as the system comes; WireGuard, kernel
  port forwards, country rules and IP blocks also need `apk add nftables iproute2` - until then the
  server's page says what does not apply, those are refused with that command, and forwards use
  realm. Installing nftables later is noticed within seconds.
- **Servers whose provider decides the ports** (NAT servers, LXC and Incus containers): **Ports
  from the provider** on the server (when adding it, or in its Edit dialog; `public_ports` in the
  API and MCP) takes the ports exactly as the provider lists them - `20000-20019`, or
  `40001-40010:10001-10010` when the provider's numbers differ from the server's, with `/tcp` or
  `/udp` where only one is forwarded. New protocols and forwards get one of those ports (a usual
  public number first), other ports are refused with the list, and links and proxy passes carry the
  number devices connect to. Let's Encrypt certificates work where the provider forwards port 80,
  to any port. Changing the list restarts nothing; protocols left outside it are listed on the
  server's page.
- **An IP set by hand is where the server is**: typing an IP as a server's address looks it up in
  DB-IP right away (and on later reports), so the map and the status page show where devices
  actually connect; the server's page says which address its place comes from. A location set by
  hand still wins; a domain leaves the place to the IP the agent reports.
- Proxy passes follow their exit server when its address, IP or ports change.
- Activity shows a camouflage-site check you started in words instead of its raw data (automatic
  checks already had their own line), and a form's error scrolls into view when it appears above
  the button you pressed.

## 0.4.3 - 2026-10-07

- The agent's firewall rules load on every nftables version: nft before 1.1 (Ubuntu 24.04, Debian 12)
  refused how the agent wrote its address and port sets, so those servers reported "could not apply
  its configuration" (nothing was changed on them - nft applies all or nothing). Upgrade the agent
  on such servers (server page › More actions › Upgrade agent - nobody is disconnected).

## 0.4.2 - 2026-10-07

- **Agent ports are a setting** (Settings › Panel › Cores › Agent ports; `agent_port` in the API):
  new agents put the Xray API on that port and Hysteria2's auth hook on the next, both on 127.0.0.1
  only. The default moves from 62789 - the port x-ui and 3x-ui use for their own Xray API - to
  50000, so Meridian runs beside them. The installer takes the first free pair from there; agents
  already installed keep their ports (moving them would restart Xray and Hysteria2), and each
  server's page shows its own.

## 0.4.1 - 2026-10-07

- The status page's header is simpler: the brand block in its middle (logo, name, "Service status"
  and its outline) is gone - the clock and the page links sit on the left, the buttons on the right.
  The sign-in page still shows your logo and name.

## 0.4.0 - 2026-10-07

One supervisor, users with their own page, a status page, country rules and every Xray protocol.
The agent contract is now version 4: upgrade each server's agent after upgrading the panel (server
page › More actions › Upgrade agent - nobody is disconnected).

- **Accounts**: one supervisor runs the panel; the people who connect are **users**. Each user has a
  subscription link and, optionally, a username and password for their own page (`/me`) with their
  usage per server and per day, the devices connected now, their link with QR code and one-tap
  import, and a password change. Users can be created in batches with generated passwords (shown
  once). Earlier admin accounts are folded into the supervisor's on upgrade.
- **Protocols**: VLESS, VMess, Trojan, Shadowsocks (2022 and classic ciphers), SOCKS5 and HTTP on
  Xray over raw, WebSocket, gRPC, HTTPUpgrade or XHTTP, with REALITY, TLS or none, Vision flow and
  CDN mode; Hysteria2; WireGuard. Only combinations that work can be saved, and each shows which
  apps can use it - computed from the same code that writes the configs. TLS certificates can be
  self-signed and pinned, obtained from Let's Encrypt by the agent, or pasted. REALITY can front your
  own website on the server.
- **Import**: the agent finds Xray, V2Ray, x-ui, 3x-ui, sing-box and Hysteria2 already on a server;
  their protocols are imported with the same keys, and users keep their IDs and passwords. **Take
  over** stops the old service and serves the same ports.
- **Status page** (the former stand-alone status site, merged in): the site's front page, at
  `/status`, on its own domain, or only at `/me`. Visitors see a sign-in form; users see their own
  page (data left, devices, link, usage per server); the supervisor sees every server on a live
  globe with availability per day, throughput, load, monthly bandwidth and outages. Server
  locations can be set by hand where IP databases are wrong; the panel can be drawn on the globe.
- **No insecure links**: self-signed certificates are pinned in the formats that can pin them and left
  out of the others; nothing ever turns certificate checks off. (Xray 26 also refuses
  `allowInsecure`, which share links used for VMess before.)
- **Country rules**: block countries (or allow only some) on every server, enforced by nftables in
  both directions so open connections stop at once; per-server rules; and a rule for who may open
  the panel, the users' pages, the status page and links. `meridian reset-site-access` recovers from
  a lockout.
- **Add server** is now a guide that follows the agent live and lists what to check if it does not
  connect. New **Protocols** and **Access** pages.
- **Your name, logo and animation** (Settings › Panel › Name and logo): upload your own logo - SVG,
  PNG, JPEG or WebP - and choose how it moves while pages load and when someone signs in (Assemble,
  Rise, Pulse, Spin or None). It replaces the umbrella everywhere: top bar, sign-in and loading
  screens, the status page, subscription pages and the browser tab. Uploaded SVGs are rebuilt from
  drawing elements only and served sandboxed, so a logo can never run code.
- **Made for AI agents too**: `AGENTS.md` and the `meridian-deploy` skill walk Codex, Claude Code and
  other agents through installing, configuring, upgrading and backing up a panel step by step,
  through supported interfaces only. `meridian token` creates an API token on the panel's host
  (no browser needed).
- The installer checks first that the domain points at the server and the ports are free, and a
  re-run with new settings applies them. The supervisor's first password is no longer written to
  the service log - only to the note the installer shows.
- MCP: 42 tools, including users' passwords, imports, country rules, the status page and branding.
- A new logo - an umbrella seen from above - that assembles panel by panel while pages load and,
  after signing in, opens: the page spreads out of it. Sign-in forms sit on the page (no pop-ups).
- **Globe**: the dots are spread evenly at a spacing that suits the globe's size on screen (no
  more grain on the small globe), coastlines are drawn exactly (Natural Earth 1:50m), every server
  city has an exact pin - dashed while its place comes only from the IP database - with a label
  joined to it by a leader line, and links to the panel fade out at the horizon. Servers in the same
  city share one pin; moving a server or setting its place by hand updates the globe at once.
- **Devices** are counted the way the IP limit counts them - one per address - everywhere: a phone
  on two protocols or two servers is one device on the user's page (listing each server and
  protocol it uses), in the overview and in country counts. IPv4 addresses that arrive in IPv6 form
  (`::ffff:1.2.3.4`) are the same device.
- **Server locations**: a server whose address changes never keeps the old address's coordinates;
  IPv6-only servers are located by their IPv6 address; a server first seen before the location
  database finished downloading is located as soon as it is there; without the city database the
  country still shows.
- Connection logs and destinations now also cover imported SOCKS5 and HTTP users (they were
  dropped), and are attributed to the protocol the connection really came in on.
- The agent finds its public address behind NAT by asking DB-IP or Cloudflare over HTTPS only (an
  answer ends up in users' links); Surge configs test connectivity over HTTPS.
- The agent no longer reports Xray's deprecation warnings as failed changes, and finds proxy
  programs under renamed binaries (`xray-linux-amd64`, `Xray`, ...) when scanning; it forgets
  Hysteria2 devices unseen for a week (memory stays bounded).
- English only throughout.

## 0.3.0 - 2026-10-06

First complete version.

- Web UI: overview, servers (protocols, proxy pass, forwards, actions), subscriptions (sharing page,
  QR codes, import buttons, IPs, destinations, traffic, config preview), monitor (online now, IP
  history, destinations, events, blocked IPs), accounts, settings. Five colour tones, phone layout.
- API tokens (read-only or full access, with expiry) and a generated OpenAPI 3.1 reference.
- MCP server at `/mcp` (30 tools; disruptive ones need `confirm=true`) and a stdio bridge
  (`meridian mcp`). Claude skills for operating a panel and for developing Meridian.
- Automatic HTTPS (`--domain`), panel installer with a hardened systemd unit, `meridian backup`.
- Security: replies on the agent channel are bound to each request (contract version 3); installer,
  agent upgrades and core downloads are checksum-pinned through the signed channel; loopback control
  ports reachable by root only; TOTP replay protection; session list and sign-out-others; strict
  validation of names, addresses, camouflage targets and forward targets; escaping in every
  subscription format.
- Hysteria2: resuming a subscription reconnects instantly (online-only kicks).
