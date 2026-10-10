# When something goes wrong

Find what you see in the tables below; each row says what to do. The messages are quoted exactly as
Meridian shows them, so you can search this page for a few words of yours (press `/`).

## Signing in

| What you see | What it means | What to do |
|---|---|---|
| `wrong username or password` | One of the two is wrong. | The supervisor's username is `admin` unless you changed it. Check the password letter by letter. |
| `too many attempts - wait a few minutes` | Three tries from your address, or five at this username from new addresses, within 15 minutes. | Wait 15 minutes. A sign-in that works resets the count. |
| `too many failed sign-ins from your address - try again in 15 minutes` | Three wrong passwords in a row from your address. | Wait the time it says. If it happens again, the wait grows: an hour, four hours, a day. |
| `too many failed sign-ins from your network (203.0.113.0/24) - try again in 24 hours` | Ten wrong passwords from addresses near yours within an hour - perhaps someone guessing. | Sign in with a **passkey** (never held back), or from an address you signed in from before. Or restart the panel on its server: `sudo systemctl restart meridian`. |
| `this username had many failed sign-ins - from an address it does not know, it takes one try a minute: wait a minute` | Others failed at your username. | Wait a minute between tries. Your usual address is never slowed down. |
| `wrong two-factor code - check the time on your phone` | The code does not match. | Set your phone to automatic date and time, then type a fresh code. |
| `this code was already used - wait for the next one (at most 30 seconds)` | Each code works once. | Wait for the app to show the next code. |
| `Passkeys work only at the panel’s own address (its domain, over HTTPS).` | The panel was opened at an IP address, or at another name. | Open `https://panel.example.com`. |
| `not available in your region` | The panel's country rule does not let your country in. | On the panel's server: `sudo -u meridian meridian reset-site-access --data /var/lib/meridian`, then `sudo systemctl restart meridian`. |
| `Maintenance in progress - ...` | Maintenance mode is on: only the supervisor can sign in. | Sign in as the supervisor and turn it off in **Settings › Security**. |
| The supervisor's password is lost | | On the panel's server: `sudo -u meridian meridian reset-password admin`. It prints a new password and turns two-factor sign-in off. |

## A server

| What you see | What to do |
|---|---|
| The server shows **Offline** (and the event *stopped reporting*) | Check in your provider's control panel that the server runs. Then on the server: `systemctl status meridian-agent --no-pager`. If the agent is stopped: `systemctl start meridian-agent`. |
| `clock skew` in the agent's log | `timedatectl set-ntp true` on the server. |
| A button says a **restart is needed** | A change waits for a core restart, which disconnects people for a moment. Press it when it suits you. Nothing restarts without your click. |
| **Health** lists a risk | Open it: it says what changed on the server. If you did it yourself, mark it as expected; if not, take it seriously. |
| You want to set up the agent again | Paste the server's install command again (the server's page › **More actions** shows it). It is safe and keeps everything. |

## People cannot connect

Go through the list on [Check that it works](check-it-works.md) - in most cases it is the port at
the provider's firewall, a paused user, or an app that still has an old link.

## The panel itself

| What you see | What to do |
|---|---|
| The panel's page does not load | On its server: `systemctl status meridian --no-pager`. Not running? `sudo systemctl restart meridian` - the servers and your users' connections keep running. |
| It still does not start | `journalctl -u meridian -n 50 --no-pager` shows why. |
| A plugin broke something | `sudo -u meridian meridian plugins list --data /var/lib/meridian`, then `sudo -u meridian meridian plugins disable --data /var/lib/meridian ID` with the plugin's ID. |

## Logs

The logs say what happened, in plain words. On the panel's server:

```bash
journalctl -u meridian -n 100 --no-pager
```

On a server with an agent (`rc-service meridian-agent status` and `/var/log/messages` on Alpine):

```bash
journalctl -u meridian-agent -n 100 --no-pager
```

Add `-f` instead of `-n 100 --no-pager` to watch new lines as they come; press `Ctrl` + `C` to stop.

## Ask for help

Open an issue in the [repository](https://github.com/Dokoyamo-Lisa/Meridian/issues) and include:

1. The version (**Settings › Updates**, or `meridian version` on the server).
2. What you did, what you expected, and what happened - with the exact message.
3. The last lines of the log above.

> **Never:** Do not post passwords, API tokens, users' links, a server's install command (it holds
> the server's secret) or backups. Replace your domain and addresses with `example.com` and
> `203.0.113.10` if you like.

A security problem? Report it privately instead, through the repository's security advisory form
(see [SECURITY.md](../../SECURITY.md)).
