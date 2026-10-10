# Operations

## The database

A new panel keeps its data in PostgreSQL: the installer installs it (apt or dnf), creates the role
and the database `meridian`, and writes `/var/lib/meridian/database.url`
(`postgres://meridian@/meridian?host=/var/run/postgresql` - the panel's own system user signs in over
the local socket, so no password is stored). A panel installed before 1.0, or with
`--database sqlite`, keeps everything in SQLite, `/var/lib/meridian/meridian.db`. Settings › Updates
and `meridian db status` say which.

Moving the data - with the panel stopped; the counts are checked, and what it moved from is kept:

```bash
sudo systemctl stop meridian
# to PostgreSQL: an empty database the panel's user may use (for example the installer's)
sudo -u meridian meridian db to-postgres --data /var/lib/meridian 'postgres://meridian@/meridian?host=/var/run/postgresql'
# or back to SQLite
sudo -u meridian meridian db to-sqlite --data /var/lib/meridian
sudo systemctl start meridian
```

To create the role and database by hand: `sudo -u postgres createuser meridian` and
`sudo -u postgres createdb -O meridian meridian`. A database on another host works too
(`postgres://user:password@host/db?sslmode=verify-full`); `MERIDIAN_DATABASE_URL` in
`/etc/meridian/meridian.env` overrides the file.

## Backups

The panel keeps everything in its database (plus the plugins' files). Backups carry it as a SQLite
file whichever database the panel uses, and restore into either. **Settings › Backups**:

- **Download** a backup: a ZIP of the database and the plugins' files. It holds every key, token and
  credential, so the panel asks for your password first; give a passphrase and the file is encrypted
  (with [age](https://age-encryption.org) - `age -d -o backup.zip backup.zip.age` opens it too).
- **Off this server**, automatically: a **WebDAV** folder (Nextcloud, ownCloud, a NAS, Koofr...) or an
  **S3** bucket (AWS, Cloudflare R2, Backblaze B2, Wasabi, MinIO...), HTTPS only. Every backup sent
  there is encrypted with your passphrase - keep the passphrase somewhere safe: without it nobody,
  you included, can open them. Choose every day or every week at an hour, and how many to keep (older
  ones are removed). **Save and check** writes a small file there, lists the folder and removes the
  file. Each backup and each failure is in the timeline and the *servers* notifications.
- **Restore** a backup from the list at the destination, or from a file. The panel checks it (a sound
  database from this version or an older one, nothing in the ZIP that does not belong there), keeps
  it ready and restarts with it; what it replaces is kept in `before-restore-<time>` in the data
  directory (delete it once all is well - it holds keys too). The servers keep running throughout and
  get the restored configuration when they reconnect - which may change what they run, if the backup
  differs. The panel's service starts it again by itself (the installer sets that up).

On the panel's host the same works from the command line - with the panel running:

```bash
sudo -u meridian meridian backup --data /var/lib/meridian /var/lib/meridian/backup-$(date +%F).db
```

and to restore (a `.db` from that command, or a `.zip` / `.zip.age` made in the panel - the passphrase
is asked, or taken from `MERIDIAN_BACKUP_PASSPHRASE`):

```bash
sudo systemctl stop meridian
sudo meridian restore --data /var/lib/meridian backup.zip.age
sudo systemctl start meridian
```

`restore` refuses to run while the panel is running, checks the backup, and keeps what it replaces
next to the data (on PostgreSQL, as a SQLite file `meridian.db.before-restore-<time>`). Do not copy a
database over `meridian.db` by hand: SQLite would lay the old database's write-ahead log
(`meridian.db-wal`) over it on the next start.

Agents reconnect by themselves; nothing on the servers changes unless the restored data differs.

## Upgrades

**Panel** - **Settings › Updates** shows the newest release; **Update now** installs it, and
**Install new releases by themselves** does the same at night (03:00-05:00, panel time). The panel
downloads the release from GitHub, checks that its `SHA256SUMS` carries Meridian's release signature
(the public key is built into the panel - a release without a valid signature is never installed)
and that the archive matches it, and backs up its database (`/var/lib/meridian/backups`, the last
three). It then leaves the release in `/var/lib/meridian/update`, where the updater service
(`meridian-update.path` / `.service`, root) picks it up, checks the signature and checksum again
with the installed binary's key, refuses anything not newer than what runs, and installs it with the
release's own `install-panel.sh --upgrade`. If the new panel does not stay up, the updater puts the
previous version back (kept in `/usr/local/lib/meridian/previous`). Servers keep running throughout;
the panel restarts once; the timeline says how it went.

The updater is installed by `install-panel.sh` (from 0.7.0). A panel installed before that is
upgraded the manual way once - unpack the new release and run:

```bash
sudo ./install-panel.sh --upgrade
```

Servers keep running while the panel restarts; agents reconnect within seconds. Database
migrations run at start-up.

**Agents** - **Settings › Updates › Upgrade all agents** (or the button on Servers) upgrades every
server whose agent is older than the panel; offline servers upgrade as soon as they connect. One
server at a time: its page, **More actions › Upgrade agent**. Nobody is disconnected: the cores run
in their own systemd units. The panel sends the new binary's checksum inside the signed state; the
agent refuses anything else. When a new panel version changes the agent contract (the changelog says
so), each server shows the alert "the panel needs a newer agent" until you upgrade its agent; the old
agent keeps serving its last configuration meanwhile. A new agent that writes a core's configuration
differently never restarts it by itself: the server page says what waits, and **Restart now**
restarts exactly that. An upgrade whose report never came back (an agent before 1.0 could lose it
as it restarted) is settled as soon as the agent says it runs the new version; **Upgrade all
agents** replaces an upgrade that still waits with binaries the panel no longer has, and says which
servers it left out and why (an upgrade already under way, or waiting for an offline server).

**Cores (Xray, Hysteria, realm)** - set the versions in **Settings › Cores**. Running servers keep
their version until you choose **More actions › Upgrade Xray**, **Upgrade Hysteria** or **Upgrade
realm** on a server (that core restarts once; Xray's configuration is tested with the new version
first, and Xray is rolled back if it does not stay up). New servers install the configured versions.
A core whose download fails holds back only its own changes; everything else still applies.

## Notifications

Settings › Notifications sends what needs you to a Telegram chat and/or an HTTPS webhook. Events are
sent in order from the activity timeline; the panel remembers how far it got, so restarting it
neither repeats nor loses anything, and turning notifications on never sends the past. While every
channel fails, the events wait (retried with a growing pause, up to 15 minutes) and are dropped after
a day - the timeline in the panel keeps them. The bot token and webhook address are stored apart from
the other settings and never shown again in full (not even to read-only API tokens).

Webhooks receive `{"text", "content", "site", "events": [{"time", "level", "kind", "message"}]}`:
Slack and Mattermost read `text`, Discord reads `content`. Only `https://` addresses are accepted, none
on the panel's own machine.

The Telegram bot can also answer commands in its chat, send a daily report and - once you allow it -
take decisions about health risks and pause users, each confirmed with a button: see
[the Telegram bot](telegram.md). What the servers' health checks find is described in
[health checks](health.md).

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

### Turnstile keeps everyone out

If Cloudflare's Turnstile cannot be reached from the panel (or from your browser), sign-ins fail with
a message saying so. Turn it off on the panel's host: add `MERIDIAN_NO_TURNSTILE=1` to
`/etc/meridian/meridian.env` and `sudo systemctl restart meridian` (proxies keep running). Sign in,
fix or turn off Turnstile in **Settings › Security**, then remove the line and restart again.

### Too many failed sign-ins

Sign-ins are held back in three ways, and the message says which and for how long:

- **"too many attempts - wait a few minutes"**: three tries from one address, or five at one username
  from addresses it does not know, within 15 minutes. A sign-in that works resets the count.
- **"too many failed sign-ins from your address"**: three wrong passwords in 15 minutes shut the
  address out - for 15 minutes, then an hour, four hours and a day if it comes back.
- **"too many failed sign-ins from your network"**: ten wrong passwords from one network within an
  hour - its IPv4 /24, or its IPv6 /24 - keep the whole network out for a day.

Addresses that signed in to the account in the last 30 days are never slowed down at its username or
held back by their network, so failing on purpose cannot lock you out. **A passkey always signs in**:
it cannot be guessed, so none of these limits apply to it. Restarting the panel
(`sudo systemctl restart meridian` - proxies keep running) clears them all.

### A plugin keeps the panel from working

Turn it off on the panel's host - a running panel follows within a few seconds:

```bash
sudo -u meridian meridian plugins list --data /var/lib/meridian
sudo -u meridian meridian plugins disable ID --data /var/lib/meridian
```

Or start the panel without any plugin: add `MERIDIAN_NO_PLUGINS=1` to `/etc/meridian/meridian.env`
and `sudo systemctl restart meridian`; remove the line (and restart) to bring the plugins back.
Details: [plugins](plugins.md#safety-and-recovery).

## The status page on its own domain

1. Point the domain (for example `status.example.com`) at the panel's host, like the panel's own
   domain.
2. Enter it in **Settings › Panel › Status page › Its own domain**.
3. With `--domain` (automatic HTTPS) the panel obtains a certificate for it on the first visit.
   Behind your own reverse proxy, add the domain there too (see below).

That domain serves only the status page, the users' sign-in and pages, and their subscription links;
the panel and its API answer `404` there. Visitors see every server there (unless **Show the servers
to everyone** is off); signed in, you also see what needs you - the panel itself still opens only at
its own address. Users are then told to sign in at `https://status.example.com/me`.

## Moving the panel

1. Back up the database and install the panel on the new host.
2. Stop the new panel, restore the backup there (`meridian restore`, above), start it.
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

## A server that keeps losing the panel

Some servers have a poor route to the panel - the server in one country, the panel in another,
across a congested or filtered border. Their agent drops off again and again although the server
and its users are fine. The panel notices: a server **keeps losing the panel** when, in the last 24
hours, it went offline 3 times or more, or was offline for more than 1% of them (about 15 minutes)
and came back in between. Drops that came with a reboot or an agent restart do not count, nor do
drops nearly every server had at once (that is the panel's own network). The overview then lists
the server, its page says why, and the timeline - and so notifications, group *servers* - says so
once a day.

Such a server can reach the panel **through another of your servers** with a good route to both (a
relay): on its page, **Connection to the panel › Reach the panel through**, choose the relay and
save. The agent switches within a minute. Nothing restarts and nothing changes for its users.

- The relay only passes the connection on: TLS still ends at the panel, checked for the panel's own
  name, and every request is signed and its body sealed as always - the relay sees neither content
  nor keys, and logs nothing of it.
- The relay listens on a TCP port the panel picks, away from its protocols and forwards (on a server
  whose provider decides its ports, one the provider forwards); the relay's page shows it. **If a
  firewall at the relay's provider filters what comes in, let that port in.** Only the addresses of
  the servers it relays get through - anything else is closed at once - and at most 64 connections
  at a time. The country rule and IP blocks never apply to that port, so a relayed server in a
  blocked country still gets through.
- When the relay cannot be reached, the agent goes to the panel directly by itself and tries the
  relay again a minute later (then less often while it keeps failing). The server's page says so -
  *The relay through Sweden failed (no answer from the relay server) - connected directly* - and so
  does the overview.
- One hop only: a relay reaches the panel directly, and a server that relays others is never relayed
  itself. Both need agent 1.0 or later (**More actions › Upgrade agent** first).
- The agent keeps its relay on disk: after a restart or a reboot it goes through the relay again
  before the panel has answered.
- When a relayed server's address changes, the relay does not know the new one yet: the agent
  reaches the panel directly once, and the relay learns the address from that report. (Should the
  direct way fail too, the server is out of touch with the panel until it gets through - its users
  notice nothing.)

**Automatically**: **Settings › Panel › Monitoring › When a server keeps losing the panel, relay it
through** names a server that takes such servers by itself. The panel switches a server there once,
records it in the timeline and notifies you; switch it back on the server's page at any time - for a
week after a person chose a server's way, the automatic relay leaves it alone.

Through the API: `PATCH /api/servers/{id}` with `{"panel_relay": <the relay's server id>}` (`0` =
directly) and `auto_relay` in `PUT /api/settings`; AI assistants use the `set_panel_relay` tool.

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

## The console

**Console** on a server's page (or the terminal button in the server list) opens a root shell on that
server in the panel - for a quick look or a fix without SSH. Several servers can be open at once, each
in a tab; **–** hides the window while the shells keep running.

- Only you, signed in to the panel in your browser, can open one - never an API token, MCP, a plugin
  or a user - and the panel asks your password again when you have not confirmed it in the last 30
  minutes.
- The server's agent starts the shell and connects it to the panel the same way it sends everything
  else (signed, TLS checked, through its relay if it uses one); nothing typed or shown is kept or
  logged. Opening and closing are in the timeline and the *security* notifications.
- Under systemd the shell runs in a scope of its own: what you start from it keeps running when the
  agent is upgraded or restarted. A console with no key pressed for 30 minutes closes; at most four
  are open on a server (three from the panel at a time).
- To turn it off on a server, create `/etc/meridian-agent/no-console` there (or set
  `MERIDIAN_NO_CONSOLE=1` in the agent's environment); the panel then hides the button and says why.

## Maintenance mode

**Settings › Security › Maintenance mode**: while you work on the panel (an upgrade of your own, a
restore, moving it), only you can sign in. Users are signed out of their own pages and see
*Maintenance in progress* - with the line you add, e.g. *back at 18:00 UTC* - there and on the
status page. Their connections, subscriptions and the servers keep working the whole time. Turn it
off when you are done.

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
| `MERIDIAN_NO_TURNSTILE` | `1` = sign-ins do not need Cloudflare Turnstile, whatever Settings say (the way back in when Cloudflare cannot be reached) |
| `MERIDIAN_NO_PLUGINS` | `1` = start without any plugin |

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
| "keeps losing the panel" | The server's route to the panel is poor. Let it reach the panel through another server: its page, **Connection to the panel** (see [A server that keeps losing the panel](#a-server-that-keeps-losing-the-panel)). |
| "The relay through … failed" | `refused`: the relay's agent is not running, or a firewall at its provider closes its port (its page shows the port). `closed the connection`: the relay does not know this server's address yet (it learns it within a minute) or cannot reach the panel itself. `no answer`: a firewall drops the port. The server reaches the panel directly meanwhile. |
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
