# Operations

## Backups

The whole panel is one SQLite database. Take a consistent copy while it runs:

```bash
sudo -u meridian meridian backup --data /var/lib/meridian /var/lib/meridian/backup-$(date +%F).db
```

The copy contains every key, token and credential: it is created readable by its owner only - keep
it that way wherever you store it. A daily cron job plus copying the file off the host is enough.

**Restore**: stop the panel, put the copy in place, start it.

```bash
sudo systemctl stop meridian
sudo install -m 0600 -o meridian -g meridian backup.db /var/lib/meridian/meridian.db
sudo systemctl start meridian
```

Agents reconnect by themselves; nothing on the servers changes unless the restored data differs.

## Upgrades

**Panel** - unpack the new release and run:

```bash
sudo ./install-panel.sh --upgrade
```

Servers keep running while the panel restarts; agents reconnect within seconds. Database
migrations run at start-up.

**Agents** - on each server page, **More actions › Upgrade agent**. Nobody is disconnected: the cores
run in their own systemd units. The panel sends the new binary's checksum inside the signed state;
the agent refuses anything else. When a new panel version changes the agent contract (the
changelog says so), each server shows the alert "the panel needs a newer agent" until you upgrade
its agent; the old agent keeps serving its last configuration meanwhile.

**Cores (Xray, Hysteria, realm)** - set the versions in **Settings › Cores**. Running servers keep
their version until you choose **More actions › Upgrade Xray** on a server (Xray restarts once; the
agent tests the configuration with the new version first and rolls back if it does not stay up).
New servers install the configured versions.

## API tokens without a browser

Scripts and AI agents working on the panel's host can create a token there (shown once; it appears
in **Settings › API & MCP**, where you can revoke it):

```bash
TOKEN=$(sudo -u meridian meridian token --name deploy --scope full --days 1)
```

`--scope read` makes a read-only token; `--days 0` one that never expires. Prefer short lifetimes for
one-off jobs.

## Lost access

**The supervisor's password**:

```bash
sudo -u meridian meridian reset-password --data /var/lib/meridian admin
```

It prints a new password, turns off two-factor sign-in for the account and ends its sessions.

**A user's password**: open the user in the panel and choose **New password** under *Their own
page* (or set one with **Edit**). The user's sessions end; their proxy connections are not
affected.

### Locked out by the site country rule

**Access › Who may open this site** refuses a rule that would lock out the person saving it, but
your address can change later (travel, a new provider). If the panel now shows "not available in
your region", run on the panel's host:

```bash
sudo -u meridian meridian reset-site-access --data /var/lib/meridian
sudo systemctl restart meridian
```

It turns the site rule off; the servers' country rule stays as it is. Proxies keep running while the
panel restarts.

## The status page on its own domain

1. Point the domain (for example `status.example.com`) at the panel's host, like the panel's own
   domain.
2. Enter it in **Settings › Panel › Status page › Its own domain**.
3. With `--domain` (automatic HTTPS) the panel obtains a certificate for it on the first visit.
   Behind your own reverse proxy, add the domain there too (see below).

That domain serves only the status page and the users' sign-in; the panel and its API answer `404`
there, and the supervisor cannot sign in on it (use the panel's own address to see the servers). Users are then told to sign in at
`https://status.example.com/me`.

## Moving the panel

1. Back up the database and install the panel on the new host.
2. Restore the backup there.
3. Point the domain to the new host (or change **Settings › Public URL** and reinstall agents with
   fresh commands if the address changes).

Agents keep working through the move: they retry until the panel answers again.

## Behind a reverse proxy

Run the panel with `--listen 127.0.0.1:8080` (`MERIDIAN_LISTEN` in `/etc/meridian/meridian.env`)
and let the proxy terminate TLS. Requirements:

- pass `Host` and `X-Forwarded-Proto`, and append the client to `X-Forwarded-For`;
- allow long requests: agents hold `GET /agent/v1/state` open for up to 50 seconds;
- allow request bodies up to 32 MB (agent reports);
- if the proxy is not on the same host, list it in `MERIDIAN_TRUSTED_PROXIES` (loopback is trusted
  by default). Only trusted proxies' forwarding headers are believed - the country rules and the IP
  logs depend on the real client address. Behind Cloudflare, add Cloudflare's published ranges
  (<https://www.cloudflare.com/ips/>) to `MERIDIAN_TRUSTED_PROXIES`.

Caddy:

```
panel.example.com, status.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

nginx:

```nginx
server {
    listen 443 ssl http2;
    server_name panel.example.com status.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;
    client_max_body_size 32m;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 90s;
    }
}
```

Then set **Settings › Public URL** to `https://panel.example.com`.

## Logs

| What | Where |
| --- | --- |
| Panel | `journalctl -u meridian -f` |
| Agent | `journalctl -u meridian-agent -f` (on the server); on Alpine `grep meridian-agent /var/log/messages` |
| Xray | `journalctl -u meridian-xray`; on Alpine `grep meridian-xray /var/log/messages` |
| Hysteria2 | `journalctl -u 'meridian-hy2@*'`; its request log is `/run/meridian-agent/hy2-N.log` |
| realm forwards | `journalctl -u 'meridian-realm@*'`; on Alpine `grep meridian-realm /var/log/messages` |
| Services on Alpine | `rc-status` lists them (`meridian-agent`, `meridian-xray`, `meridian-hy2.N`, `meridian-realm.N`) |
| What happened, in plain words | **Monitor › Events** |

## Settings reference (`/etc/meridian/meridian.env`)

| Variable | Meaning |
| --- | --- |
| `MERIDIAN_DOMAIN` | serve HTTPS on :443 with a Let's Encrypt certificate (and :80 for the certificate check) |
| `MERIDIAN_EMAIL` | Let's Encrypt contact (optional) |
| `MERIDIAN_LISTEN` | plain HTTP address when there is no domain, e.g. `:8080` or `127.0.0.1:8080` |
| `MERIDIAN_DATA` | data directory (`/var/lib/meridian`) |
| `MERIDIAN_AGENT_DIR` | where the agent binaries for upgrades and installs are (`/usr/local/lib/meridian/agent`) |
| `MERIDIAN_TRUSTED_PROXIES` | comma-separated CIDRs of reverse proxies |
| `MERIDIAN_NO_GEO_DOWNLOAD` | `1` = never download the IP location database (country rules then stay unavailable) |
| `MERIDIAN_ADMIN_USER`, `MERIDIAN_ADMIN_PASSWORD` | the supervisor's name and password on the very first start only (default `admin` and a generated password) |

Everything else lives in **Settings** in the panel.

## Removing Meridian

- A server: delete it in the panel. The agent removes protocols, forwards, firewall rules and
  itself. Or on the server: `sudo meridian-agent uninstall`. Services that were stopped by
  **Take over** during an import are not started again - start them yourself if you want them back.
- The panel: `sudo ./install-panel.sh --uninstall`. The data in `/var/lib/meridian` and
  `/etc/meridian` stays until you delete it.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Server stays "Waiting for agent" | Run the install command again and read its output. The server must reach the panel's public URL. The server page's guide lists the checks. |
| Install says `meridian-install.sh: FAILED` | The panel was updated since you copied the command. Copy a fresh one from the server page. |
| `clock skew` in the agent log | The server's time is off by more than 5 minutes: `timedatectl set-ntp true`. |
| Server offline | The agent reports every few seconds. Check `systemctl status meridian-agent` (Alpine: `rc-service meridian-agent status`) and that the server can reach the panel. Protocols keep working while the agent is down. |
| A shared certificate is not live on a server | *Settings › Certificates* shows each server: **installed** means Xray loads it within ten minutes (nobody is disconnected); **pending** means the server has not taken it (offline, or the protocol is off); **needs agent 0.6** means upgrade that agent. |
| "the configuration was rejected by Xray" after saving configuration code | Xray refused the merged result: nothing changed on the server. The message names the problem; **What the server gets** shows the merged configuration. |
| WireGuard devices lost IPv6 | Since 0.6 tunnels carry IPv4 unless **IPv6 through the tunnel** is on (it never routed IPv6 before - `::/0` only swallowed it). Turn it on where the server has IPv6. |
| A protocol on a NAT server or container does not connect | Its port must be one the provider forwards: set **Ports from the provider** in the server's **Edit** dialog exactly as the provider lists them (`20000-20019`, or `40001-40010:10001-10010` when the numbers differ). The server's page then lists protocols outside them; links carry the provider's numbers. |
| "nftables is not installed" on a server's page | That server (often Alpine) has no nftables: WireGuard, kernel port forwards, country rules and IP blocks don't work there; the proxies do. `apk add nftables iproute2` (Alpine) or `apt install nftables` - the panel notices within seconds. |
| "needs an Xray restart" alert | A change (for example to the Xray core version) needs one restart. Nothing restarts until you click **Restart now**. |
| A protocol cannot be saved | The form says why and what to do instead - for example Trojan needs TLS, and a Let's Encrypt certificate needs a domain pointing at the server. |
| REALITY connects slowly or not at all | **Test from server** on the protocol. Pick a site that answers quickly from the server, or front your own site. |
| Let's Encrypt certificate not issued | The domain must point at the server and port 80 must be free and reachable from the internet. The protocol card shows the agent's last attempt. |
| Hysteria2 never connects | UDP to its port is blocked somewhere. Try another port, or turn on obfuscation. |
| WireGuard missing on a server | Its kernel has no WireGuard module (old kernels, some containers). |
| Destinations show IPs, not names, for WireGuard | Turn on "Log the names devices look up" for that WireGuard protocol. |
| Forwards do nothing | The kernel engine needs `nftables`; install it, or use the realm engine. |
| Country rules unavailable | The panel downloads the DB-IP country database on its first start (needs access to `download.db-ip.com`); it takes a few minutes. |
| A user cannot sign in | Check they use the address shown on their page in the panel (`/me`) and that they have a sign-in (**Their own page**). Too many failed attempts lock the name for 15 minutes. |
| The globe puts a server in the wrong city | On the server page, **On the status page › Edit**, set the location by hand. |
