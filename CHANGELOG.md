# Changelog

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
