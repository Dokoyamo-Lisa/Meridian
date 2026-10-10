# Health checks

Every few minutes each server's agent looks for signs that the server was broken into or is being
abused: a crypto-miner, a program running from a temporary folder, a port nobody opened, a new
account or SSH key, a changed scheduled task or service, SSH sign-ins, traffic Rosélune does not
account for, Rosélune's own programs changed. What it finds becomes a **risk** in the panel, and you
decide about each one.

Health checks only tell. Nothing is ever stopped, blocked or paused because of a risk - if a server
runs a miner, you stop it.

Agents 1.0 and later run the checks; an older agent shows "Health checks need agent 1.0 or later" on
its server page (**More actions › Upgrade agent** - nobody is disconnected).

## How it works

- **The first check records what is normal.** A minute after the agent starts, the first check
  notes the server's accounts, SSH keys, scheduled tasks, services, kernel modules, listening ports
  and Rosélune's own programs - the *baseline* - and keeps it on the server
  (`/var/lib/meridian-agent/health.json`, readable by root only). From then on, every check (every
  five minutes) reports what changed since.
- **Some things are bad in themselves** and are reported even by the first check: a crypto-miner, a
  connection to a mining pool, `/etc/ld.so.preload`, a program running from `/tmp`, `/var/tmp` or
  `/dev/shm` or whose file was deleted, a program posing as a kernel thread, a second account with
  full rights (uid 0).
- **It is light.** It reads `/proc` and a few files, at the lowest processor and disk priority, and
  hashes Rosélune's programs only when they change (and once a day). It runs no shell; the only
  program it starts is `journalctl`, to read the SSH server's messages.
- **It sends little.** Each finding is a title and a short detail. Command lines, file contents and
  password hashes never leave the server: a changed file is described by how many lines were added
  and removed; a password change is noticed from a digest of the hash.
- **Nothing is lost.** Findings wait on the server until the panel has them, so a panel that was
  unreachable gets them later, and none is counted twice.

## What it looks for

| Finding | Severity |
| --- | --- |
| A known crypto-miner (xmrig, kinsing, kdevtmpfsi, minerd, cpuminer, nanominer and others), or a program whose command line is a miner's | critical |
| A connection from a program that is not Rosélune's to a port mining pools use (3333, 4444, 5555, 14444, ...) | high (critical for a miner) |
| A program running from `/tmp`, `/var/tmp` or `/dev/shm`, one whose file was deleted, or one posing as a kernel thread | high |
| A program - not Rosélune's (agent, Xray, Hysteria, realm) and not a common system service - that kept a processor busy for 10 minutes | warning |
| A port that opened after the baseline and is not Rosélune's (protocols, WireGuard, forwards, the agent) or a system service's (sshd, resolvers, time servers ...) | warning |
| A second account with uid 0 | critical |
| A new account | high if it can sign in, warning if it cannot |
| An account that gained a login shell, or a password where it had none | high |
| A password changed | warning (high for root) |
| Administrator rights changed: `/etc/sudoers`, `/etc/sudoers.d/` | high |
| A new SSH key in `authorized_keys` of root or any account that can sign in - one finding per key | high |
| SSH keys removed | info |
| `/etc/ld.so.preload` with anything in it (every program then loads that library - how rootkits hide) | critical |
| Scheduled tasks changed: `/etc/crontab`, `/etc/cron.d/` and `cron.hourly` ... `cron.monthly`, `/etc/anacrontab` | warning |
| A person's own scheduled tasks changed (`crontab -e`: `/var/spool/cron`) | high |
| A service set to start at boot (systemd or OpenRC), a service's settings changed (`/etc/systemd/system/*.d/`), `/etc/rc.local` changed | warning |
| The SSH server's settings changed (`/etc/ssh/sshd_config`, `sshd_config.d/`) | warning |
| A kernel module loaded - except the many loaded on demand (firewall, tunnels, file systems, encryption, drivers) | warning |
| Someone signed in over SSH with a key | info |
| Someone signed in over SSH with a password | warning (high for root) |
| Many failed SSH sign-ins (on average 60 or more in 5 minutes) | warning |
| The server sent far more than Rosélune's protocols and forwards carried and than it received - over 1 GB more, and three times as much, in 10 minutes (a DDoS bot) | high |
| A load average above twice the processor cores over 15 minutes | warning |
| The agent's program file replaced while it runs (not by its own upgrade) | high |
| The agent's program is not this panel's build of its version | critical |
| A core's program file changed after the agent installed it; a Hysteria program that is not the published release | high; critical |

SSH sign-ins come from the system journal (systemd) or from `/var/log/auth.log`, `/var/log/secure`
or `/var/log/messages`.

## Deciding about a risk

Each risk can be:

- **Acknowledged** - you have seen it. If it happens again, it is flagged again.
- **Expected** - it is yours: it is never flagged again on its server, or - if you choose so - on
  **every server**, including servers you add later. Expected on every server asks first and says
  what it means: a risk may be a real break-in, so only mark expected what you know is yours.
- **Opened again** - undoes either.

Who decided and when is kept with the risk and in the activity timeline.

A risk is recognised by its **key**, shown under each risk, so the same thing found again is the
same risk. Keys are specific, so *expected* covers exactly one thing:

| Key | What expected means |
| --- | --- |
| `port:tcp:8080` | that port may be open on this server |
| `ssh-login:root:SHA256:...` | root signing in with that SSH key, from anywhere |
| `ssh-password:root:203.0.113.5` | root signing in with a password from that address |
| `ssh_keys:root:SHA256:...` | that one key in root's `authorized_keys` (a key added later is still flagged) |
| `account:deploy`, `service:docker.service`, `module:zfs`, `cpu:ffmpeg` | that account, service, module or busy program |
| `file:/etc/crontab` | changes to that file are never flagged again - acknowledge instead to hear about the next one |

Some risks *still hold* (a process runs, a port is open): the panel shows "still so" until the
agent no longer sees it. Others happened once (a sign-in, a changed file).

## Where you see them

- **Monitor › Health**: every server's risks, open ones first, with filters by status, severity and
  server, and the buttons to decide.
- **The server page**: a line under the numbers - "Health · nothing needs you · checked 2m ago" -
  that opens into the server's risks (by itself when something serious is open).
- **The overview**: open high and critical risks are in **Needs you**.
- **Notifications** (Settings › Notifications): the **Health risks** group sends high and critical
  risks as they are found; it is on by default.
- **Telegram**: with the bot answering commands and changes allowed, each high or critical risk
  arrives with **Acknowledge** and **Expected…** buttons, and `/risks` lists the open ones - see
  [the Telegram bot](telegram.md).

## The API and MCP

| | |
| --- | --- |
| `GET /api/risks?status=open&severity=high&server=3` | risks, most serious first (`status`: open - the default -, acknowledged, expected or all) |
| `POST /api/risks/{id}/decide` | `{"decision": "expected" \| "acknowledged" \| "open", "scope": "server" \| "all"}` |
| `GET /api/servers/{id}/health` | when the server's check last ran, and everything it found |
| MCP `list_risks` | the same list, for an assistant |
| MCP `decide_risk` | a decision; expected on every server needs `confirm=true` |

Read-only tokens can list risks but not decide. Details are in the API reference (Settings › API &
MCP).

## If a server was broken into

The check tells you something is wrong; it cannot tell you everything an intruder changed. When a
risk looks real (a miner, a key or account you did not add, `ld.so.preload`):

1. Do not mark it expected. Look at what it names on the server (`ps -fp <pid>`, the file, the key).
2. Stop what runs (`kill`), remove what keeps bringing it back - SSH keys, accounts, scheduled
   tasks, services - and change the passwords of the accounts that can sign in.
3. Turn password sign-ins off (`PasswordAuthentication no` in `/etc/ssh/sshd_config`) and let SSH be
   reached only from your own addresses.
4. If the server's own programs were changed, or you are not sure you found everything, move its
   users to another server and reinstall the system - then add it to the panel again.
