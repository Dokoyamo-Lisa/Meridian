# Changelog

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
