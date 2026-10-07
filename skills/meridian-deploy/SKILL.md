---
name: meridian-deploy
description: Install, configure, upgrade, back up and move a Meridian proxy/VPN panel and its servers, step by step, using only Meridian's supported interfaces (the release installer, the meridian CLI, the REST API and the panel's settings) - never by editing Meridian's files. Use it whenever you are asked to deploy, set up, install, upgrade, migrate, rebrand or configure Meridian, or to add servers, protocols or users to it.
---

# Deploying and configuring Meridian

Meridian is a panel (one Linux host, web UI + API) that manages proxy servers (each runs a small
agent). You install the panel from a release, then add servers by running the install command the
panel gives you on each server. Everything else is set through the panel's API.

Follow this file **in order**. Each step says what to run, what you should see, and what to do if
you do not. **Do not skip a check, do not continue past a failure, and do not improvise.** When a
step says *ask the human*, stop and ask.

Helper scripts are in `scripts/` next to this file. Get them on the machine where you run commands
(your own, or the panel's host) and run everything below from that folder:

```bash
git clone --depth 1 https://github.com/Dokoyamo-Lisa/Meridian.git && cd Meridian/skills/meridian-deploy
```

They stop with a clear message when something is wrong. They need `bash`, `curl` and `python3`
(present on Debian and Ubuntu; otherwise `apt-get install -y curl python3`).

## Rules - read these first

**Always**

- Use only these ways to change Meridian: `scripts/install-panel.sh` (or the release's own
  `install-panel.sh`), the `meridian` / `meridian-agent` commands, `/etc/meridian/meridian.env`
  (only the variables listed under *Settings file* below), and the REST API (`scripts/api.sh`).
- Check every result. A command that printed an error **did not work** - fix the cause first.
- Use `https://` for the panel address once it has a domain. Never send a token over plain `http://`
  except to `127.0.0.1` on the panel's own host.
- Keep secrets out of logs, chats, commits, issue trackers and files others can read: the
  supervisor's password, API tokens (`mrd_...`), install commands (they contain a server's secret),
  users' passwords and subscription links. Hand them only to the human who asked.
- Before anything that disconnects people (pausing or deleting users, new links or credentials,
  restarting Xray, deleting servers or protocols, country rules, importing with take-over), tell the
  human exactly what will happen and wait for a clear yes.

**Never**

- Never edit Meridian's installed files: the binaries, `/etc/systemd/system/meridian.service`,
  anything in `/var/lib/meridian` (that includes the database), or on servers anything under
  `/etc/meridian-agent`, `/var/lib/meridian-agent` or the `meridian-*` systemd units (on Alpine the
  `/etc/init.d/meridian-*` scripts). The panel and
  the agent rewrite them; your edits would be lost or break things.
- Never edit Xray, Hysteria or WireGuard configuration files on a server - the agent writes them
  from the panel. Change protocols through the API.
- Never run `meridian` database commands as root: use `sudo -u meridian meridian ...`.
- Never restart `meridian-xray`, `meridian-hy2@*` or `meridian-agent` to "fix" something - a restart
  disconnects users. Read the logs and the panel's message instead.
- Never turn off certificate checks anywhere, and never use services or mirrors other than the ones
  named here (GitHub, Let's Encrypt, DB-IP, Cloudflare).
- Never guess values you were not given (domains, hosts, names, how many users). Ask.

## 0. What you need from the human

Ask for everything missing **in one message** before you start:

| You need | Example | Notes |
| --- | --- | --- |
| The panel's host | `203.0.113.10`, SSH as root or a sudo user | Debian 12 / Ubuntu 22.04+ (any systemd Linux works), amd64 or arm64, 1 GB RAM |
| The panel's domain | `panel.example.com` | Recommended: automatic HTTPS. Its DNS A record must point at the panel's host, **not** proxied by a CDN. Without a domain the panel can only be offered behind the human's own TLS proxy. |
| An e-mail for certificate notices | `ops@example.com` | Optional |
| The proxy servers | name, IP, SSH access for each | Any systemd Linux or Alpine Linux (OpenRC), amd64 or arm64. The panel's host can also be a proxy server |
| Ports, for NAT servers only | "ports 20000-20019", or "public 40001-40010 go to 10001-10010" | Only when the provider decides the ports: NAT VPS, LXC or Incus containers (SSH is then usually on an odd port, e.g. `ssh -p 10022`). Copy it exactly from the provider's page |
| What to run on them | "VLESS REALITY + Hysteria2" | That is the default - fine for most people |
| Users to create | names, monthly quota, expiry | Optional |
| Name and logo | "Acme Net", an SVG/PNG file | Optional (default: Meridian and the umbrella) |

## 1. Check the panel's host

On the panel's host, run and check each line:

```bash
uname -m                      # x86_64 or aarch64 - anything else: stop, Meridian cannot run there
systemctl --version | head -1 # systemd NNN - if "command not found": stop, systemd is required
sudo -n true && echo sudo-ok  # sudo-ok - you need root
curl -sS https://api.github.com >/dev/null && echo github-ok   # github-ok - downloads come from GitHub
```

With a domain, check it points here (both lines must print the same address):

```bash
getent ahostsv4 panel.example.com | awk 'NR==1{print $1}'
curl -4 -sS https://1.1.1.1/cdn-cgi/trace | sed -n 's/^ip=//p'
```

If they differ: **ask the human** to point the domain's A record at the panel's host (and turn off
any CDN proxy for it), then wait until both lines match. Do not install with `--domain` before that.

If a firewall is active on the panel's host (`sudo ufw status` says `active`, or
`sudo firewall-cmd --state` says `running`), open the web ports:

```bash
sudo ufw allow 80/tcp && sudo ufw allow 443/tcp                       # ufw
sudo firewall-cmd --permanent --add-service=http --add-service=https && sudo firewall-cmd --reload   # firewalld
```

Cloud providers (AWS, GCP, Azure, Oracle, ...) also have a firewall in their console ("security
group"): **ask the human** to allow TCP 80 and 443 to the panel's host there.

## 2. Install the panel

With a domain (recommended):

```bash
sudo bash scripts/install-panel.sh --domain panel.example.com --email ops@example.com
```

Behind the human's own TLS reverse proxy instead (no domain on this host):

```bash
sudo bash scripts/install-panel.sh --listen 127.0.0.1:8080
```

**You should see** `Checksum OK`, then `Installing Meridian ...`, then `Sign in with:` followed by a
username and a password, then `Open https://panel.example.com`.

- Copy the username and password **only into your reply to the human**, and tell them to sign in,
  change the password (Settings › Security) and turn on two-factor sign-in. The installer shows them
  once; if they are lost, `sudo -u meridian meridian reset-password admin` makes a new one.
- `error: ... points to ..., but this server's public address is ...` - the DNS is not ready: go back
  to step 1.
- `error: port 80 is already in use ...` (or 443) - another web server runs here. **Ask the human**:
  stop it, or install with `--listen 127.0.0.1:8080` behind that web server (see *Behind a reverse
  proxy* in `docs/operations.md`).
- `checksum mismatch` - do not install. Download again later; if it repeats, tell the human.
- Anything else: run `journalctl -u meridian -n 50 --no-pager` and read it.

**Check:**

```bash
curl -sS https://panel.example.com/healthz     # must print: ok
```

(Behind a proxy: `curl -sS http://127.0.0.1:8080/healthz` on the host.) The first visit can take
up to a minute while the certificate is issued.

## 3. Get API access

On the panel's host:

```bash
TOKEN=$(sudo -u meridian meridian token --name deploy-agent --scope full --days 1)
```

It prints a line about the token on stderr and the token itself (`mrd_...`) on stdout. The token
expires after one day; the human can revoke it any time in Settings › API & MCP.

Wherever you run the scripts:

```bash
export MERIDIAN_URL=https://panel.example.com      # with a reverse proxy on this host: http://127.0.0.1:8080
export MERIDIAN_TOKEN=mrd_...                      # the token from above - never print it again
scripts/api.sh GET /api/me                          # must show "username": "admin" (or the chosen name)
```

If `api.sh` says `answered 401`: the token is wrong or expired - make a new one.

## 4. Panel settings

`PUT /api/settings` changes only the fields you send. Set what the human asked for, for example:

```bash
scripts/api.sh PUT /api/settings '{"site_title":"Acme Net","timezone":"Europe/Berlin"}'
```

| Field | Meaning |
| --- | --- |
| `site_title` | the panel's name on every page and in users' apps (up to 64 characters) |
| `timezone` | IANA name, e.g. `Asia/Singapore`; days and monthly resets follow it |
| `public_url` | only behind a reverse proxy: the address users and servers use, e.g. `https://panel.example.com` (with `--domain` it is set for you) |
| `logo_animation` | `assemble`, `rise`, `pulse`, `spin` or `none` |
| `agent_port` | where new agents put their two local-only ports (default 50000, then 50001); change it **before** installing agents if something on the servers already uses them |
| `status_page` | `off` (users sign in at /me), `home` (the status page is the front page), `page` (at /status) |
| `status_about` | one public line on the sign-in page - nothing private |

The answer is the full settings object: check your values are in it.

**Logo** (optional): SVG, PNG, JPEG or WebP, at most 128 KB:

```bash
scripts/api.sh PUT /api/settings/logo @logo.svg image/svg+xml      # PNG: image/png, JPEG: image/jpeg
```

The answer must say `"custom": true`. A refused SVG says what to remove (scripts, links, outside
images or fonts) - tell the human; do not try to "fix" their logo yourself unless they ask.

## 5. Add each server

For each proxy server (one at a time):

**5a. Create it in the panel.** `address` is the IP or domain clients connect to.

```bash
scripts/api.sh POST /api/servers '{"name":"Tokyo 1","address":"198.51.100.20","protocols":["vless","hysteria2"]}' > server.json
python3 -c 'import json; d=json.load(open("server.json")); print("server id:", d["server"]["id"])'
```

Remember the server id. The install command is in `server.json` (field `install`) - it contains the
server's secret: do not print it, and delete `server.json` when the server is connected.

**NAT servers, LXC and Incus containers** (the provider gives a list of ports): add `public_ports`
to the same request, written exactly like this - the panel then puts protocols only on those ports
and links carry the provider's numbers:

```bash
# the provider forwards 20000-20019 to the same numbers:
scripts/api.sh POST /api/servers '{"name":"NAT 1","address":"198.51.100.21","protocols":["vless","hysteria2"],"public_ports":"20000-20019"}' > server.json
# the provider forwards public 40001-40010 to 10001-10010 on the server (public:server):
#   "public_ports":"40001-40010:10001-10010"
# only TCP, or only UDP, is forwarded: add /tcp or /udp, e.g. "20000-20009/tcp, 20010-20019/udp"
```

`address` is then the provider's public IP (the one you SSH to), never the server's own `10.x`,
`172.16-31.x` or `192.168.x` address. Forgot `public_ports`, or the provider changed them? Set them
later - nothing restarts: `scripts/api.sh PATCH /api/servers/ID '{"public_ports":"20000-20019"}'`.
A `400` answer quotes the part it could not read; fix exactly that part.

**5b. Run the install command on that server as root.** Pass it without printing it, e.g.:

```bash
python3 -c 'import json; print(json.load(open("server.json"))["install"])' | ssh root@198.51.100.20 bash
# signed in as a sudo user instead of root:   ... | ssh admin@198.51.100.20 sudo bash
```

**You should see** `Meridian agent installed and running.` If it fails:

- `download failed` or `cannot reach the panel at ...`: the server cannot reach `MERIDIAN_URL`.
  On the server, `curl -sS https://panel.example.com/healthz` must print `ok` - fix DNS or the
  panel host's firewall (step 1) until it does, then run the command again.
- `meridian-install.sh: FAILED` or `does not match its checksum`: the command is stale (the panel was
  upgraded since). Fetch a fresh one with `scripts/api.sh GET /api/servers/ID` (field `install`).
- `unsupported CPU architecture`: the server is not amd64/arm64 - it cannot run Meridian.

**5c. Wait until it is ready:**

```bash
scripts/wait-server.sh ID          # READY: Tokyo 1 (198.51.100.20, ...): vless on 443, hysteria2 on 443
```

Do not continue on `NOT READY` - follow what it prints. Lines starting with `NOTE:` are things that
do not work on that server as configured (for example a protocol on a port the provider does not
forward) - each says what to change.

**Alpine servers:** the proxies (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP, Hysteria2) work as
the system comes. WireGuard, kernel port forwards, country rules and IP blocks need nftables - if the
human wants any of them, run `apk add nftables iproute2` on that server first. Without it the panel
refuses those with a message saying exactly that, and `scripts/api.sh GET /api/servers/ID` lists it
under `limits`. Do not try to work around it.

**5d. Open the server's firewall for its protocols** - only if a firewall is active on it
(`ufw status` = active, or `firewall-cmd --state` = running):

```bash
for p in $(scripts/ports.sh ID); do ssh root@198.51.100.20 "ufw allow $p"; done
# firewalld: ssh root@198.51.100.20 "firewall-cmd --permanent --add-port=$p && firewall-cmd --reload"
```

Run `ports.sh` again after adding protocols or forwards later. Cloud firewalls/security groups:
**ask the human** to allow exactly the ports `ports.sh` prints (both TCP and UDP where listed).
On NAT servers `ports.sh` prints the ports on the server itself (what its own firewall must let in);
the provider's forwarding already decides what reaches it from outside.

## 6. More protocols (optional)

The defaults from 5a suit most people. To add another, first ask the panel whether it works:

```bash
scripts/api.sh POST /api/protocols/check '{"kind":"vless","settings":{"security":"reality"}}'
```

`"valid": true` means it can be added (`apps` lists the apps that can use it); otherwise `error`
says what to change - change exactly that and check again. Then add it to server ID with the same
body:

```bash
scripts/api.sh POST /api/servers/ID/nodes '{"kind":"vless","settings":{"security":"reality"}}'
```

It is applied live (nobody is disconnected). Then repeat 5d for the new port. On a server with
`public_ports`, leave `port` out (the panel picks a forwarded one) - a port outside the list is
refused with `400`. Kinds: `vless`,
`vmess`, `trojan`, `shadowsocks`, `socks`, `http`, `hysteria2`, `wireguard`. The full list of
settings is in the API reference (`GET /api/openapi.json`, or `docs/openapi.json`).

## 7. Users

```bash
scripts/api.sh POST /api/users '{"name":"Alice","quota":107374182400,"reset_day":1}'
```

- `quota` is bytes per month (100 GB = 107374182400; 0 = unlimited); `expires_at` is a Unix time;
  `ip_limit` only raises alerts; `count` creates several at once.
- The answer is a list; each user comes back **once** with `username` and `password` - give them to
  the human, together with the address where users sign in (`scripts/api.sh GET /api/meta`,
  field `user_url`) and each user's `link` for their apps.
- Nothing ever pauses a user by itself - limits only raise alerts. Do not promise otherwise.

## 8. Verify and hand over

```bash
scripts/check.sh      # every line must start with ok; FAIL / ATTN lines say what to do
```

Then tell the human, in one message:

1. The panel's address, and that they must sign in, change the password and turn on two-factor
   sign-in (Settings › Security).
2. What you set up: servers (name, IP, protocols and ports), users (names, quotas), settings.
3. Anything they still have to do (DNS, cloud firewalls you could not change).
4. That your API token expires within a day (or that they can revoke it in Settings › API & MCP).

Delete `server.json` files and any other files holding secrets.

## Later

**Upgrade the panel** (servers keep running; agents reconnect within seconds):

```bash
sudo bash scripts/install-panel.sh --upgrade              # or --upgrade --version X.Y.Z
curl -sS https://panel.example.com/healthz                # ok
```

**Upgrade a server's agent** (disconnects nobody):

```bash
scripts/api.sh POST /api/servers/ID/actions '{"kind":"upgrade_agent"}'
scripts/wait-server.sh ID
```

`upgrade_xray` and `restart_xray` disconnect Xray users for a moment - only after the human says yes.

**Back up the panel** (one file holds everything, including every secret - keep it private):

```bash
sudo -u meridian meridian backup /var/lib/meridian/backup-$(date +%F).db
```

**Restore**: `sudo systemctl stop meridian`, then
`sudo install -m 0600 -o meridian -g meridian backup.db /var/lib/meridian/meridian.db`, then
`sudo systemctl start meridian`.

**Move the panel to another host**: back up; install on the new host (steps 1-2); stop it; restore
the backup there; start it; point the domain at the new host. Servers reconnect by themselves.

**Change the domain or listen address**: run `scripts/install-panel.sh` again with the new
`--domain` (or `--listen`). It rewrites `/etc/meridian/meridian.env` and restarts the panel;
servers keep running. Then `scripts/api.sh PUT /api/settings '{"public_url":"https://new.example.com"}'`
if the address users and servers use has changed, and reinstall agents whose servers can no longer
reach the old address.

**Remove a server**: `scripts/api.sh DELETE /api/servers/ID` - its agent removes everything Meridian
set up (everyone on it is disconnected: ask first).

**Settings file** `/etc/meridian/meridian.env` (restart with `sudo systemctl restart meridian`
after a change - servers keep running): `MERIDIAN_DOMAIN`, `MERIDIAN_EMAIL`, `MERIDIAN_LISTEN`,
`MERIDIAN_TRUSTED_PROXIES` (comma-separated CIDRs of reverse proxies, e.g. Cloudflare's ranges),
`MERIDIAN_NO_GEO_DOWNLOAD`. Nothing else belongs there.

## Troubleshooting

| You see | Cause | Do this |
| --- | --- | --- |
| `curl .../healthz` times out | firewall or cloud security group | open TCP 80/443 (step 1) |
| certificate errors on the first visit | the certificate is still being issued, or DNS points elsewhere | wait a minute; recheck step 1 |
| `api.sh` answers `401` | token wrong or expired | `sudo -u meridian meridian token ...` again |
| `api.sh` answers `400` with a message | invalid value | do what the message says |
| `wait-server.sh`: agent has not connected | the server cannot reach the panel, or the install failed | run the install command again and read its output; `curl https://panel.example.com/healthz` on the server |
| `wait-server.sh`: could not apply its configuration | a protocol setting does not work on that server (e.g. a port in use) | read the message; change the protocol through the API |
| `clock skew` in `journalctl -u meridian-agent` (Alpine: `grep meridian-agent /var/log/messages`) | the server's clock is wrong | `timedatectl set-ntp true` (Alpine: `apk add chrony && rc-update add chronyd && rc-service chronyd start`) on the server |
| a request answered `400` with "needs nftables" | the server (often Alpine) has no nftables | `apk add nftables iproute2` on it, or leave that feature out - never edit firewall files yourself |
| clients cannot connect, server READY | the server's firewall or cloud security group blocks the ports | step 5d with `ports.sh` |
| `run this as the panel's user` | you ran a `meridian` command as root | prefix it with `sudo -u meridian` |

More: `docs/operations.md` (backups, reverse proxies, lost access, logs) and `docs/api.md`.
