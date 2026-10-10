# The Telegram bot

Rosélune's Telegram bot sends what needs you to one chat - a conversation with you, or a group or
channel - and, if you want, answers questions there, sends a daily report and lets you decide about
health risks or pause a user with a button.

## Set it up

1. In Telegram, open **@BotFather**, send `/newbot` and copy the token it gives you. Give Rosélune a
   bot of its own: Telegram lets only one program read a bot's messages.
2. Send your new bot a message - or add it to a group (or to a channel, as an administrator).
3. In the panel, **Settings › Notifications**: paste the token, press **Find chats**, pick the chat
   and **Save**. **Send a test message** checks it.

The token is kept apart from the other settings and never shown again in full. **Remove** deletes
the token and the chat; the bot then stops answering and the daily report stops.

## Notifications

What is sent is chosen under **What is sent**: servers (offline, back online, restarted, a
configuration refused, a crashed core), users (data used up - the servers stop serving them until
it starts over -, access ended or ending, too many devices), certificates (expiring), sign-ins and
security, and **health risks** (high and critical - see [health checks](health.md)). They arrive as
they happen, in order; turning notifications on never sends the past. They only tell: nothing is
paused or changed by a message.

## Commands

Turn on **Answer commands in the chat**. The bot registers its commands with Telegram, so typing `/`
in the chat lists them:

| Command | Answer |
| --- | --- |
| `/status` | servers online, users and who is connected, throughput now, users' traffic today, and what needs you |
| `/servers` | every server: online or for how long it has been offline, users connected, throughput, processor |
| `/server tokyo` | one server: load, memory, disk, throughput, uptime, traffic today and this month, its plan, paid period, availability, open health risks |
| `/traffic` or `/traffic month` | traffic per server, today or this month, and the users' total |
| `/users` | users by data used this cycle, who is online or paused, when access ends |
| `/user alice` | one user: data used of their limit, access end, devices online now and from where, flags |
| `/online` | who is connected now, on how many devices and which servers |
| `/top` | the users with the most traffic today and this month |
| `/events` | the latest events |
| `/risks` | open health risks, most serious first |
| `/expiring` | paid periods and users' access ending within 7 days, and who used 80% of their data |
| `/ping` | the ping monitors: each server's last round trip and the last hour's loss |
| `/report` | the daily report, now |
| `/help` | this list |

A name may be written in part (`/user ali`) as long as only one matches. In a group, commands
written for another bot (`/status@otherbot`) are left alone.

## The daily report

Turn on **Send a daily report** and pick its hour (in the panel's time zone, Settings › Panel). It
holds the traffic today and this month per server and the top users, availability over 24 hours,
servers offline, paid periods and users' access ending within 7 days, users and servers running out
of data, and the open health risks. **Send it now** sends one at once. A panel that was down at the
hour sends it when it is back, the same day; turned on after the hour, the first one comes the next
day.

## Changes from Telegram

With **Allow changes from Telegram** on (it is off until you turn it on):

- each high or critical health risk arrives as a message of its own with **Acknowledge** and
  **Expected…** buttons, and `/risks` shows the same buttons under each risk;
- `/pause alice` and `/resume alice` pause and resume users.

Every change asks first: the bot answers with what will happen and a confirmation button, and only
the person who asked can press it. **Expected…** asks whether it is expected on that server or on
every server, and every server is confirmed once more. Unanswered questions expire after 10 minutes.
Who decided is kept, as "Telegram: name (@username)".

This setting can only be turned on in the panel in a browser - never with an API token or an AI
assistant - and it turns itself off when the bot token or the chat changes, so another chat never
inherits it. Changes are made only by the Telegram users listed under **Only these Telegram users**
(ask **@userinfobot** for your id) or by your own linked Telegram account (below): with nobody listed,
everyone in the chat may read, nobody may change.

## Users and Telegram

Turn on **Users can link their Telegram accounts** (Settings › Notifications › Telegram accounts).
A user then links up to two Telegram accounts of theirs:

- in the bot's **Mini App** - the button next to the message box of a private chat with the bot, or
  **Sign in** in its reply - by signing in once with the username and password they were given. The
  password goes to the panel over HTTPS, never through the chat. Next time the app opens their page
  directly. The Mini App needs the panel's public address on https (Settings › Panel) and opens in
  Telegram's apps on phones and computers;
- or with a code: on their page, **Telegram › Link a Telegram account** shows a code that works once,
  for ten minutes - they send `/link CODE` to the bot (or tap **Open the bot in Telegram**).

In a private chat the bot then answers them: `/usage` (data used and left this cycle, when it starts
over, today, until when their access runs, devices now, and what is left of limits on single
protocols), `/devices` (where their devices connect from - never their addresses), `/open` (their
page in Telegram) and `/unlink`. A user can unlink one Telegram account once a month (30 days);
the panel lists every link and lets you unlink any at any time without using up theirs. One Telegram
account belongs to one person.

Anyone else who writes to the bot privately learns only how to link an account - nothing else.

## The panel in Telegram

Turn on **Open the panel from Telegram** (in a browser only; it asks first). Up to two Telegram
accounts of yours can then be linked - signing in once in the Mini App with your password (and
two-factor code), or with **Link one of my Telegram accounts** in the same place. A linked account
opens the panel inside Telegram and uses every command above in a private chat with the bot - with
changes allowed, it may make them without being listed. Keep Telegram's own two-step verification
on: whoever has that Telegram account has your panel. A sign-in made in Telegram cannot change
passwords, two-factor, API tokens or sessions, open the console, install plugins or change the site
rule - those need your password in a browser.

Sign-ins in the Mini App are guarded like the sign-in page: an address that keeps failing is shut out
for longer each time, a username that many addresses fail at slows down, and a Telegram account gets
five tries an hour (five wrong link codes an hour, too). The app's launch data is signed by Telegram
with the bot's token and is good for an hour.

## What the bot never does

- It answers only in the chat you chose, and - when you list user ids - only those people, plus the
  Telegram accounts linked to users or to you in private chats. Everyone else gets at most how to
  link an account.
- It never sends subscription links, passwords, keys, tokens or the servers' addresses.
- It answers at most 20 commands a minute.
- It ignores commands older than 10 minutes (sent while the panel was down).
- It reads Telegram over HTTPS only (`api.telegram.org`), one long poll at a time.

## When it does not answer

**Settings › Notifications › Telegram bot** shows what the bot does: *listening as @yourbot*, or
why it cannot read messages:

- *the bot token was refused by Telegram* - copy the token again from @BotFather;
- *another program reads this bot's messages (a webhook or a second panel)* - a bot can be read by
  one program only: give Rosélune a bot of its own, or remove the other program's webhook;
- *cannot reach Telegram* - the panel's server cannot reach `api.telegram.org`; it tries again,
  waiting longer each time (up to 5 minutes).

It also answers only commands in the chosen chat: in a group, check that the bot can read messages
(@BotFather › Bot Settings › Group Privacy, or make it an administrator).
