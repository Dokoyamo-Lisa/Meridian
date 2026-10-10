<p align="center"><img src="docs/logo.svg" width="72" height="72" alt=""></p>

# Rosélune

A panel for running proxy and VPN servers for a team or a small company. One supervisor adds
servers and protocols and creates users; each user gets a subscription link for their apps and a
sign-in to see their own usage; the supervisor watches every server on a live globe.

Rosélune was called Meridian until 1.3. Its programs, services, paths and this repository keep the
old name (`meridian`, `meridian-agent`, `/var/lib/meridian`), so nothing changes on your servers.

**[The guide](https://doc.losantos.space)** · **[Try the demo](https://deep.losantos.space)** (made-up
servers and people; look around, nothing can be changed) · [Releases](https://github.com/Dokoyamo-Lisa/Meridian/releases)

- **Simple to run.** One command installs the panel with HTTPS; one command (copied from the panel)
  installs the agent on each server. Protocols are added from guided forms that only accept
  combinations that work, and show which apps can use each one. The panel updates itself (only to
  releases signed with Rosélune's release key) and upgrades every agent with one click.
- **Never disruptive on its own.** A user who uses up their data is off every server until it starts
  over, then back by themselves; expiry dates and IP limits only raise alerts, and only you pause.
  Users, keys and settings change live through the cores' APIs; a core restarts only when you click
  a button that says it will.
- **Accountable.** Every connecting IP is recorded with its user, server, place and network;
  destinations are recorded per user; traffic is counted exactly once per user, protocol and day,
  and each user's usage is shown per protocol.
- **Users see their own usage.** Each user signs in to their own page: usage per server, per
  protocol and per day, devices connected now, their link with QR code and one-tap import for every app.
  They can link up to two Telegram accounts and ask the bot what they have left, or open their page
  in Telegram.
- **A status page built in.** As the site's front page, at `/status`, on its own domain, or only at
  `/me`. Everyone sees every server on a live globe and in detail - where it is, up or down,
  availability, throughput, load, memory and disk, bandwidth used and the day its paid period ends
  (never IP addresses unless you turn them on, never prices, users or protocols) - and signs in from
  the top right; users then see their own page. One switch leaves visitors only the sign-in. On its
  own domain the panel itself never opens. A server's details chart its processor, memory, disk,
  network, load and ping monitors over up to 30 days - each viewer picks the charts, you decide which
  visitors get.
- **Country rules.** Block countries (or allow only some) on every server, cutting open connections
  at once; separately choose which countries may open the panel, the users' pages and the status page.
- **Traffic splitting.** Send some sites, countries, ports or BitTorrent directly, through another
  server, through a provider's proxy, across a load balancer, or nowhere - for every server, some
  servers or some protocols, applied live. Proxies elsewhere are imported from their links, a
  subscription or a Clash file as exits; any that would turn certificate checks off or travel
  unencrypted are refused.
- **Servers anywhere.** A server whose IP address changes is reached by its dynamic DNS name - the
  panel checks the name and can keep it up to date in Cloudflare - and servers with IPv6 only are
  linked only where both ends can reach each other. Every link follows a server's new address at
  once.
- **Brings existing setups along.** The agent finds Xray, V2Ray, x-ui, 3x-ui, sing-box and Hysteria2
  already on a server and imports their protocols with the same keys and passwords - devices keep
  working.
- **Automatable.** A documented REST API with scoped tokens, and an MCP server so AI assistants can
  answer "who is sharing their link?" or add users - asking first before anything disruptive.
- **Watches for break-ins.** Every few minutes each agent checks its server for crypto-miners,
  programs run from temporary folders, ports nobody opened, new accounts and SSH keys, changed
  scheduled tasks and services, SSH sign-ins, traffic Rosélune does not account for and Rosélune's
  own programs changed. You mark each finding as yours or seen - here or on every server; nothing is
  stopped on its own.
- **A Telegram bot.** Notifications, a daily report (traffic per server and the top users,
  availability, what ends soon, data running out, health risks) and commands - `/status`,
  `/servers`, `/traffic`, `/users`, `/risks`, `/ping` and more - in your chat only; buttons to decide
  about health risks or pause a user, each confirmed, once you allow changes from Telegram. Its Mini
  App opens users' pages - and, if you allow it, the panel - inside Telegram.
- **Shared servers.** A server can serve two more panels besides its own: a friend's panel runs its
  own protocols and users on it, never its console, upgrades or country rule.
- **PostgreSQL or SQLite.** New panels keep their data in PostgreSQL; a panel on SQLite keeps it, and
  `meridian db to-postgres` / `to-sqlite` move it either way.
- **Yours to change.** Plugins restyle the panel and the status page, add pages, API routes, MCP
  tools and timers, and filter what servers run and what apps receive. They are installed only from
  a signed-in browser and stay off until you agree to what each may do.

## Your name, your logo

Rosélune is the software; what your users see is yours. In **Settings › Panel › Name and logo** you
set the panel's name and its logo - Rosélune's rose (five petals, a blush bloom and the moon at its
heart), the umbrella, or your own (SVG, PNG, JPEG or WebP) - and choose how it moves while pages load
and when someone signs in: assembling piece by piece, rising, pulsing, spinning, or not at all. Name
and logo appear everywhere users look: the top bar, the sign-in and loading screens, the status page,
subscription pages, the browser tab and users' apps. **Look** sets how every page looks to people
who have not picked one themselves: **Romance**, Rosélune's own (blush paper, rose ink and a serif
voice; the sign-in among falling petals), **Umbrella** (black glass, white type and one signal red - a
corporate laboratory after hours), one of the quiet tones, or the device's choice of Ice or Paper. No
files to edit; the API (`PUT /api/settings`, `PUT /api/settings/logo`) and the MCP tool
`set_branding` do the same.

## What it runs

| Protocol | Core | Transports and security |
| --- | --- | --- |
| VLESS | Xray | raw, WebSocket, gRPC, HTTPUpgrade, XHTTP · REALITY, TLS or none · Vision flow · post-quantum VLESS Encryption · CDN |
| VMess | Xray | raw, WebSocket, gRPC, HTTPUpgrade, XHTTP · TLS or none · CDN |
| Trojan | Xray | raw, WebSocket, gRPC, HTTPUpgrade, XHTTP · TLS or REALITY · CDN |
| Shadowsocks | Xray | 2022 ciphers (AES-128/256) and classic AEAD ciphers, TCP and UDP |
| SOCKS5, HTTP proxy | Xray | username and password per user |
| Hysteria2 | official Hysteria server | QUIC, port hopping, optional Salamander obfuscation, bandwidth limits |
| WireGuard | Linux kernel | official apps; destinations logged per device |
| mieru | mieru's server (mita) | TCP or UDP; one port and one small process per user, so usage, cuts and limits are exact per user |
| Snell | Surge's snell-server 5 | apps speak version 4 (Surge, Stash, mihomo apps, sing-box); one port and one small process per user |
| Port forwards | nftables (kernel) or realm | TCP/UDP relays with exact byte counts |

TLS certificates are obtained from Let's Encrypt by the agent, pasted in, self-signed, or
**shared**: kept once and replaced once for every server, with each server's copy checked - a
self-signed certificate is pinned in every app that can check it and left out of the others:
certificate checks are never turned off. REALITY can borrow a well-known site or front your own website on the server.
**Proxy pass** chains a protocol through another server: users connect to a nearby entry server and
leave the internet at the exit server (which can be kept for passes only) - or through two, with a
relay in between, or through a provider's proxy imported as an **external node**. **Traffic rules**
send chosen sites elsewhere ([routing](docs/routing.md)).
Each protocol can have **its own server address** (listening, outgoing and in links), each user can
get **whole servers or single protocols**, servers can be **IPv4 or IPv6 only**, and anything the
forms do not offer can be written as **configuration code** merged on top of the generated one.
A server whose route to the panel is poor can **reach the panel through another server** - its
agent's connection passed on, still encrypted end to end - chosen by you, or by itself once a server
keeps losing the panel.

Links work in Clash Verge / FlClash / mihomo, Stash, sing-box, Shadowrocket, Surge, Quantumult X,
Hiddify, Loon, v2rayN / v2rayNG / v2Box, NekoBox / Karing and the official WireGuard apps. The link
detects the app; browsers get a page with QR codes and one-tap import buttons. Every protocol page
lists exactly which of these apps can use it, computed from the same code that writes the configs,
and the tests check every combination with sing-box, mihomo and Xray themselves
([test/formats](test/formats/README.md)).

## Quick start

1. **Install the panel** on a Linux server (amd64 or arm64, systemd) with a domain pointing to it.
   Rosélune runs on Linux only: the panel, the agent (systemd or OpenRC, amd64 or arm64) and the
   command-line tool.
   This downloads the newest [release](https://github.com/Dokoyamo-Lisa/Meridian/releases), checks
   its SHA-256 and installs it:

   ```bash
   curl -fsSLO https://raw.githubusercontent.com/Dokoyamo-Lisa/Meridian/main/skills/meridian-deploy/scripts/install-panel.sh
   sudo bash install-panel.sh --domain panel.example.com
   ```

   Or by hand: download `meridian-<version>-linux-<arch>.tar.gz` and `SHA256SUMS` from the
   [releases](https://github.com/Dokoyamo-Lisa/Meridian/releases), then
   `sha256sum -c SHA256SUMS --ignore-missing`, unpack, and run `sudo ./install-panel.sh --domain ...`.

   It prints the supervisor's first sign-in. Change the password, then add a passkey or turn on
   two-factor sign-in (Settings › Security).

2. **Add a server** (Servers › Add server) and paste the command it shows on that server as root.
   The guide on the server page follows along until the server is connected.

3. **Add protocols** (Protocols › Add protocol). VLESS with REALITY suits most people and needs no
   domain. Or **Import existing setup** to bring over what the server already runs.

4. **Add users** (Users › New user). Each gets a link for their apps and a username and password for
   their own page at `https://panel.example.com/me`.

Details: [docs/getting-started.md](docs/getting-started.md).

## Documentation

| | |
| --- | --- |
| [Getting started](docs/getting-started.md) | Install, first server, protocols, users, the status page |
| [Routing](docs/routing.md) | Traffic rules, load balancers and external nodes: what goes where, what is refused and why |
| [IPv6 and dynamic DNS](docs/ipv6-and-dynamic-dns.md) | Servers whose IP address changes, servers with IPv6 only, Cloudflare updates |
| [Operations](docs/operations.md) | Backups, upgrades, lost access, reverse proxies, servers that keep losing the panel, troubleshooting |
| [API](docs/api.md) | Tokens, conventions, examples; the full reference is [docs/openapi.json](docs/openapi.json) and Settings › API & MCP |
| [MCP](docs/mcp.md) | Connecting Claude and other AI assistants |
| [Health checks](docs/health.md) | What the agents look for on each server, and deciding about what they find |
| [Telegram bot](docs/telegram.md) | Notifications, commands, the daily report and changes from Telegram |
| [Plugins](docs/plugins.md) | Changing the panel's looks and functions: what plugins can do, the manifest, the protocol, the JavaScript APIs, safety and recovery; [examples](examples/plugins) |
| [Deploying with an AI agent](skills/meridian-deploy/SKILL.md) | Step-by-step instructions Codex, Claude Code and other agents follow to install and configure a panel ([AGENTS.md](AGENTS.md) points them there) |
| [Architecture](docs/architecture.md) | How the panel and the agents work together |
| [Security](SECURITY.md) | Threat model, protections, reporting a problem |
| [Changelog](CHANGELOG.md) | What changed in each version |

## Building from source

Go 1.26 or newer (it fetches the toolchain `go.mod` names, currently Go 1.27.2) and Node 22+.

```bash
make            # UI, panel (dist/meridian) and agents (dist/meridian-agent-linux-*)
make test       # Go tests with the race detector + UI type check
make check      # test + vet + staticcheck + govulncheck + npm audit
make release    # dist/release: tarballs and CLI builds for linux/amd64 and linux/arm64, SHA256SUMS(.sig)
make dev        # panel on 127.0.0.1:18080 and the UI dev server on :5173
```

## Layout

```
cmd/meridian          panel binary (serve, backup, reset-password, reset-site-access, mcp bridge, openapi)
cmd/meridian-agent    agent binary (install, run, uninstall)
internal/panel        HTTP API, auth, compiler (desired state per server), status page, MCP, OpenAPI
internal/agent        agent: Xray / Hysteria / WireGuard / realm / nftables engines, ACME, import scan
internal/subgen       subscription formats and app support
internal/geo          IP locations and country lists (DB-IP Lite)
internal/seal         agent channel crypto (HMAC-signed requests, AES-GCM sealed bodies)
internal/proto        the panel <-> agent contract
web/                  the panel UI and the status page (Preact + Vite), embedded into the panel binary
deploy/               panel installer
examples/plugins/     example plugins: a theme and a server plugin written in Go
skills/               agent skills: meridian-deploy (install and configure), meridian-admin (operate through MCP)
test/e2e/             end-to-end test kit for a local VM
```

## Deploying with an AI agent

Codex, Claude Code and other coding agents can install and configure Rosélune for you: point them at
this repository - [AGENTS.md](AGENTS.md) sends them to the step-by-step
[deploy skill](skills/meridian-deploy/SKILL.md), which uses only the installer, the `meridian`
command and the API (never edits Rosélune's files) and checks every step.

## License

Rosélune is free software under the [GNU Affero General Public License v3.0](LICENSE): you may use,
study, change and share it; if you run a changed version as a service, its users are entitled to
its source.

## Credits

IP geolocation by [DB-IP](https://db-ip.com) (CC BY 4.0). Map data from
[Natural Earth](https://www.naturalearthdata.com) (public domain). Proxy cores:
[Xray](https://github.com/XTLS/Xray-core), [Hysteria](https://github.com/apernet/hysteria),
[realm](https://github.com/zhboner/realm), and the Linux kernel's WireGuard and nftables.
