# Getting started

From nothing to the first person connected, in about ten minutes.

## 1. Install the panel

You need a small Linux server for the panel (any distribution with systemd; 1 CPU and 512 MB are plenty) and,
ideally, a domain name pointing to it.

Download the release for the server's CPU from the project's Releases page (`linux-amd64` for most
servers, `linux-arm64` for ARM), check it against `SHA256SUMS`, unpack it and run the installer as
root:

```bash
sha256sum -c SHA256SUMS --ignore-missing
tar xzf meridian-0.4.0-linux-amd64.tar.gz
cd meridian-0.4.0-linux-amd64
sudo ./install-panel.sh --domain panel.example.com --email you@example.com
```

- `--domain` makes the panel serve HTTPS with an automatic Let's Encrypt certificate. Ports 80 and
  443 must be reachable from the internet. Using it accepts Let's Encrypt's terms.
- No domain? Use `--listen :8080` and put the panel behind a TLS reverse proxy (see
  [Operations](operations.md#behind-a-reverse-proxy)). Plain HTTP works, but your sign-in and the
  install commands then travel unencrypted.

The installer prints the supervisor's first sign-in (user `admin` and a generated password). There
is one supervisor account: it runs the panel. The people who use the servers are **users** - they
get links and their own page, never the panel. Sign in, then right away:

1. **Settings › Security**: change the password and turn on two-factor sign-in.
2. **Settings › Panel**: check the public URL (servers use it to reach the panel) and the timezone
   (days and monthly resets follow it).

The panel runs as the unprivileged user `meridian`. Its database is `/var/lib/meridian/meridian.db`,
its settings `/etc/meridian/meridian.env`.

## 2. Add a server

**Servers › Add server**. Give it a name ("Tokyo 1") and, optionally, the domain or IP clients
should use (empty = the IP the server reports). An IP typed here also places the server on the map:
the panel looks it up in DB-IP right away (unless you set the location by hand). The server page
then shows a guide:

1. **Check the server** - Linux with systemd (Debian 11+, Ubuntu 20.04+, AlmaLinux / Rocky 8+,
   Fedora, Arch) or Alpine Linux (OpenRC), amd64 or arm64, root access, and the server can reach
   the panel. On Alpine the proxies (Xray, Hysteria2) work as the system comes; WireGuard, kernel
   port forwards, country rules and IP blocks also need `apk add nftables iproute2` - without them
   the server's page says what does not apply, and forwards use realm.
2. **Paste the command** it shows on the server as root:

   ```bash
   { curl -fsSLo meridian-install.sh https://panel.example.com/agent/install.sh || wget -qO meridian-install.sh https://panel.example.com/agent/install.sh; } && echo '<checksum>  meridian-install.sh' | sha256sum -c - && MERIDIAN_TOKEN='<token>' sh meridian-install.sh --api-port 50000
   ```

   It works the same on every distribution (curl or busybox's wget, plain `sh`). The command checks
   the installer's checksum, the installer checks the agent's, and the agent checks every core it
   downloads. The token in it is the server's secret - treat it like a
   password; if it leaks, use **More actions › Rotate agent token** on the server page and run the
   new command on the server. Traffic keeps flowing meanwhile (proxy passes from it too: they switch
   to new credentials when the reinstalled agent connects).

The guide turns green when the agent connects (usually within seconds). If it does not, the guide
lists what to check: the command's output, `systemctl status meridian-agent` (on Alpine
`rc-service meridian-agent status`), the server's clock and whether it can reach the panel's address.

### Servers whose provider decides the ports (NAT, LXC, Incus)

A NAT VPS or a container shares its provider's IP and is reachable only on the ports the provider
forwards to it - "ports 20000-20019", or "public 40001-40010 go to 10001-10010". Tick **Its
provider decides the ports** when adding the server (or set **Ports from the provider** in the
server's **Edit** dialog later) and enter them as the provider lists them:

| Provider's page says | Enter |
| --- | --- |
| Ports 20000-20019 | `20000-20019` |
| Public 40001-40010 → 10001-10010 on the server | `40001-40010:10001-10010` |
| Public 10443 → 443, SSH on 10022 → 22 | `10443:443, 10022:22` |
| TCP only on 20000-20009, UDP only on 20010-20019 | `20000-20009/tcp, 20010-20019/udp` |

Protocols and forwards then get one of those ports (a usual public number first, e.g. the port the
provider forwards 443 to for REALITY), other ports are refused with the list, and links carry the
number devices connect to. Hysteria2 and WireGuard need a port forwarded for UDP. Let's Encrypt
certificates need public port 80 forwarded (to any port: the agent answers there); without it, use
a self-signed or pasted certificate. Changing the list restarts nothing; protocols left outside it
are listed on the server's page.

The address clients use is the provider's public IP (the agent reports it), not the server's own
`10.x` or `192.168.x` address. If the provider forwards through a proxy rather than NAT, the server
sees the provider's address instead of the devices': device counts, country rules and IP blocks
cannot tell devices apart there.

## 3. Add protocols

**Protocols › Add protocol**, pick the server and a starting point:

| Starting point | When |
| --- | --- |
| **VLESS · REALITY** | The default for everyone. No domain or certificate; looks like a visit to a well-known site. |
| **Hysteria2** | Long or lossy routes (QUIC over UDP). |
| **VLESS · WebSocket · CDN** | Hides the server behind Cloudflare or another CDN. Needs a domain on the CDN. |
| **VMess · WebSocket · TLS** | Works in almost every app, including Surge. |
| **Trojan · TLS** | Looks like an HTTPS server; best with a real domain. |
| **Shadowsocks 2022** | Simple and supported everywhere, TCP and UDP - including Surge and Quantumult X. |
| **WireGuard** | A full VPN for laptops, phones and offices. |
| **SOCKS5 / HTTP proxy** | For apps that only speak a plain proxy. |

Each is only a starting point: change the protocol, transport (raw, WebSocket, gRPC, HTTPUpgrade,
XHTTP), security (REALITY, TLS, none) and every other setting in the same form.

The form only offers settings that fit together, and checks the whole combination before saving:
anything that would not work - on the server or in the apps - is refused with a reason and what to
do instead. Below the form you see which apps can use the protocol as configured.

- **REALITY** borrows a well-known site (the panel tests candidates from the server and picks one
  that answers quickly) or fronts **your own website** running on the server.
- **TLS** uses a self-signed certificate pinned by the apps that can check one (no domain needed;
  apps that cannot are left out - links are never made insecure), a **Let's
  Encrypt** certificate the agent obtains and renews (needs a domain pointing at the server and
  port 80 free), or a certificate you paste.
- Ports are chosen for you (443 first for TLS, REALITY and Hysteria2 where it is free; 8388 for
  Shadowsocks, 51820 for WireGuard) and can be changed. On a server whose provider decides the
  ports, only those ports are used (see above).
- **One address per protocol**: on a server with several IP addresses, give a protocol one of them
  (**Server address for this protocol**, under *Port, name and more*). It listens there, its
  traffic leaves from there, and links use it - so several protocols can each have port 443 on
  their own address. The agent lists the server's addresses after it connects (agent 0.6).
- **Names**: a protocol with a name is listed in apps under that name alone; without one, as
  "server · protocol". Repeated names are numbered, so every app accepts the list.
- **Certificate**: self-signed, Let's Encrypt, pasted, or **shared** - one certificate kept once in
  *Settings › Certificates* and replaced there once for every server (see below).
- **WireGuard** tunnels carry IPv4 by default: devices keep their own IPv6 and full tunnels do not
  route `::/0`. Turn on **IPv6 through the tunnel** where the server has IPv6 (agent 0.6): devices
  then get an IPv6 address inside and reach IPv6 sites through the server.
- **Proxy pass** sends a protocol's traffic out through a protocol on another server. Whether users
  may also connect to that exit directly is up to you: untick **Users can also connect to the exit
  directly** (or tick **Only for proxy passes** on the exit) and the exit leaves users' links and
  accepts only the pass. The exit may pass on once more - a relay - so a chain has two passes at
  most (entry › relay › exit), each to another server; the list of exits shows a relay with where it
  leads (`Tokyo · REALITY → Frankfurt · REALITY`). While something passes through a protocol, its
  own exit must leave the internet itself.

**Already running Xray, V2Ray, x-ui, 3x-ui, sing-box or Hysteria2 on the server?** Use **Import
existing setup** on the server page. The agent reads their configuration (it changes nothing), you
pick what to bring over, and each protocol keeps its keys, and each user keeps their ID or password
- devices keep working. With **Take over**, the old service is stopped and Meridian serves the same
ports; without it, nothing is stopped and the imports use free ports.

### IP version

A server uses IPv4 and IPv6 by default. In its **Edit** dialog, **IP version** can make it IPv4
only (protocols reach sites over IPv4, links use the IPv4 address, WireGuard carries no IPv6) or
IPv6 only. A server whose kernel has IPv6 turned off is used as IPv4 only by itself - nothing IPv6
is configured there, so applying never fails over it. Changing it applies to Xray live; Hysteria2
protocols restart once (their devices reconnect by themselves).

### Shared certificates

When several servers use one certificate - a wildcard such as `*.example.com`, or one your own
ACME client renews - keep it once in **Settings › Certificates** and choose **Shared certificate**
in each protocol. Replacing it there updates every server that uses it: Xray loads the new one
within ten minutes without disconnecting anyone, Hysteria2 restarts once. The list shows, per
server and protocol, whether it is serving the new certificate yet (the agent checks what each
TLS port actually presents). A renewal hook can do the same with one API call - the key and the
token go through a pipe and a file descriptor, never on a command line other users can read:

```bash
jq -n --rawfile c fullchain.pem --rawfile k privkey.pem '{cert_pem: $c, key_pem: $k}' |
  curl -fsS -X PATCH https://panel.example.com/api/certs/ID \
    -K <(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN") \
    -H "Content-Type: application/json" --data-binary @-
```

A replacement that no longer covers a domain some protocol uses is refused.

### Configuration as code

For what the forms do not offer, each protocol and each server take your own settings as code,
merged on top of what the panel generates. Only the syntax is checked: Xray and Hysteria decide the
rest. If the core refuses the result, the server's page shows why and the running configuration
stays. **What the server gets** shows the merged result.

**A protocol's own settings** (the **Advanced settings** panel on the right of the protocol's
window) - JSON, comments allowed. They take precedence: whatever they set overrides the form, and
the form is locked while they are on (port, name, address and proxy pass stay editable). Turning
them off removes them when you save:

- its fields are merged into the protocol's inbound: `sniffing`, `streamSettings.sockopt`,
  `fallbacks`, ... Its tag, port and users stay the panel's;
- `outbounds` are added, with your own tags (`direct` and `block` are the panel's). A tag is one
  outbound on the server: protocols naming the same tag share the first one, and the server's
  settings below replace it - define an outbound several protocols use there once;
- `rules` route this protocol's traffic only (private addresses stay blocked).

```jsonc
{
  "sniffing": { "enabled": true, "destOverride": ["http", "tls", "quic"] },
  "outbounds": [ { "tag": "warp", "protocol": "wireguard", "settings": { /* ... */ } } ],
  "rules": [ { "domain": ["geosite:openai"], "outboundTag": "warp" } ]
}
```

A Hysteria2 protocol takes YAML in the same panel; `auth` and `trafficStats` stay Meridian's, and
saving restarts it. Apps' links are always built from the form: advanced settings that change how
apps connect (transport, security, keys) make the links stop working.

**A server's settings** (the server's **Configuration as code** section) - Xray JSON for everything
on it, merged last:

- `outbounds` are added, or replace the one with the same tag;
- `routing.rules` come before the panel's own rules;
- `inbounds` with a protocol's tag (`n12`, listed in the section) change that protocol; others are
  added as your own inbounds, with the users written in them;
- other sections (`dns`, `fakedns`, ...) are merged; `api`, `stats`, `log` and `policy` stay
  Meridian's.

## 4. Add users

**Users › New user**. Each user gets:

- a **subscription link** - one link for all their apps; every app picks its own format from it;
- a **username and password** for their own page at `https://panel.example.com/me` (turn it off
  if you only want to hand out links). A generated password is shown once - copy the text with the
  sign-in address, username, password and link and pass it on.

Options:

- **Access**: everything (including servers you add later), or only some - whole servers (with the
  protocols added to them later) and single protocols, in any mix.
- **Data per cycle, reset day, valid until, device limit**: these only raise alerts. Nothing is ever
  paused or cut off automatically - pausing is always your click.
- **How many**: create `team-01` … `team-20` in one go, each with their own password.

On the user's page in the panel you see their link with a QR code and import buttons, who is
connected right now, IP history, destinations and daily traffic.

## 5. What users see

A user signs in at `/me` with the username and password you gave them and sees:

- their usage this cycle, what is left, when it starts over and until when their access runs;
- usage **per server** (today, this cycle, 30 days) and per day;
- the devices connected right now (address, place, network, server, protocol);
- their link with a QR code and one-tap import buttons for every app;
- which servers are up, on the globe; and they can change their own password.

## 6. The status page (optional)

**Settings › Panel › Status page**:

| Setting | Effect |
| --- | --- |
| **Only at /me** | The front page is the panel; users sign in at `/me`. |
| **Front page** | The site's front page is the status page; the panel stays at `/overview`. |
| **At /status** | The status page is at `/status`; the panel stays at its address. |
| **Its own domain** | Point a domain such as `status.example.com` at the panel and enter it: that domain shows only the status page and the users' sign-in, never the panel. |

What it shows depends on who looks:

- **Visitors** see every server on a live globe and in detail, like a probe page: where it is and
  its IP addresses, up or down, availability for 24 hours, 30 days and each day, live throughput and
  daily traffic, load, memory, disk and connections, the system it runs, bandwidth used this month
  and the day its paid period ends (the server's **Renews on** date). Prices, users, protocols and
  ports are never shown. **Sign in** at the top right takes users to their own page.
  Turn off **Show IP addresses** to keep the addresses to yourself, or **Show the servers to
  everyone** to leave visitors only the logo, the panel name (Settings › Panel › Panel name), your
  line of text and a sign-in form.
- **Users** who sign in see their own page: the data they have left, the devices connected now,
  their link with QR code and one-tap import, and their usage per server.
- **You** (signed in with the supervisor account) always see every server, addresses included, and
  what needs you - such as a paid period ending within a week. **The panel on the globe** draws an
  arc from every server to the panel's city.

On each server page, **On the status page** leaves a server off the globe, gives it another name
there or corrects where it sits (IP databases often place data-centre addresses at the provider's
office).

## 7. Watch it

- **Overview**: servers, people online, throughput, today's traffic and everything that needs you.
- **Monitor › Online now**: every connection - user, IP, place, network, server, protocol.
- **Monitor › IP history**: which IPs used which users' links. A link used from many networks or
  countries at once is probably shared; open the user and look at their IP history.
- **Monitor › Destinations**: where traffic goes (domains for Xray and Hysteria, exact bytes for
  WireGuard with DNS logging on).
- **Block** an abusive IP from any of these lists; **Pause** a user to stop them.
- **Notifications** (Settings › Notifications): problems that need you, sent as they happen to a
  Telegram chat and/or an HTTPS webhook (Slack, Discord and Mattermost work as they are) - servers
  going offline or coming back, a machine that restarted, a configuration a server refused, a core
  that crashed, users who used up their data or whose access ended, expiring shared certificates,
  and (if you want) sign-ins. Create a bot with @BotFather, send it a message, paste its token and
  press **Find chats**. Turning notifications on never sends the past, and they never pause anyone.

## 8. Country rules (optional)

**Access**:

- **Country rule for your servers** - block the listed countries, or allow only them, on every
  protocol and forward (SSH is never touched). Connections that are already open from a blocked
  country stop at once. A server can follow its own rule instead (server page › Country rule).
- **Who may open this site** - the same by country for the panel, the users' pages, the status page
  and (optionally) subscription links. The panel refuses a rule that would lock you out; if you
  ever are locked out later (travelling, a changed address), see
  [Operations](operations.md#locked-out-by-the-site-country-rule).

The country lists come from DB-IP's free database, downloaded by the panel once a month.

## 9. Your name and logo (optional)

**Settings › Panel › Name and logo**: the panel's name, your own logo (SVG, PNG, JPEG or WebP, up to
128 KB) and how it moves - the umbrella assembling, Rise, Pulse, Spin or None. The preview shows it
as users will see it; **Use the umbrella** brings the built-in logo back.

## Next steps

- [Proxy pass](architecture.md#proxy-pass): users connect to a nearby server and exit elsewhere.
- **Port forwards** (server page): relay a port to another host with exact byte counts.
- [API tokens and MCP](mcp.md): let scripts or an AI assistant use the panel.
- [Operations](operations.md): backups, upgrades, troubleshooting.
