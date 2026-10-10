# The Telegram bot and webhooks

Once your bot sends alerts ([Alerts on Telegram](telegram-alerts.md)), it can do more: answer your
questions, send a daily report, let you acknowledge risks and pause people with a button, open the
panel inside Telegram, and tell your users what they have left. Alerts can also go to Slack, Discord,
Mattermost or any web address.

Everything here is in **Settings › Notifications**.

## Ask the bot

Under **Telegram bot**, tick **Answer commands in the chat** and save. In your chat, send `/help`:

| Command | Answers |
|---|---|
| `/status` | A summary: servers online, devices, traffic today. |
| `/servers`, `/server` *name* | Each server's state, or one in detail. |
| `/traffic`, `/top` | Traffic today and this month; who used most. |
| `/users`, `/user` *name*, `/online` | People, one person, who is connected. |
| `/events`, `/risks`, `/expiring` | Latest events, open health risks, access and data running out. |
| `/report` | The daily report, now. |

The bot answers only in your chat. It never sends links, passwords, keys or your servers'
addresses.

## A daily report

Tick **Send a daily report**, choose the hour (the panel's time zone) and save. It holds traffic
today and this month per server, the top users, availability, servers offline, what is ending soon,
data running out and open health risks. **Send it now** shows you one at once.

## Changes from Telegram

Tick **Allow changes from Telegram** (it asks first). Then:

- each high or critical health risk arrives with **Acknowledge** and **Expected…** buttons;
- `/pause` and `/resume` work for people.

Every change waits for a confirmation button that only the person who asked can press. Under **Only
these Telegram users**, list who may make changes (Telegram user ids - **@userinfobot** tells you
yours); others in the chat can only read.

> **Good to know:** Protective steps on servers (stopping a process, removing a key...) are never
> possible from Telegram - only in the panel, in your browser.

## The panel inside Telegram

Turn on **Open the panel from Telegram** and link up to two Telegram accounts of yours (**Link one of
my Telegram accounts**). The bot then opens the panel inside Telegram.

> **Careful:** Whoever has that Telegram account has your panel. Keep Telegram's own two-step
> verification on. (Passwords, two-factor, tokens, the console and plugins still need a browser.)

## Your users and the bot

Turn on **Users can link their Telegram accounts** (under **Telegram accounts**). Each person can
link up to two accounts - by signing in once in the bot's app, or with a code from their own page.
They then send the bot `/usage` (data used and left, until when their access runs) and `/devices`, and
open their page in Telegram. You see every linked account there and can **Unlink** any.

## A webhook (Slack, Discord, Mattermost...)

Under **Webhook**, paste the **Address** of an incoming webhook (HTTPS only) and save. Slack, Discord
and Mattermost webhooks work as they are; anything else receives JSON with the text and each
event's time, level, kind and message. **What is sent** applies to it too.

## If something goes wrong

| What you see | What to do |
|---|---|
| The bot does not answer commands | **Answer commands in the chat** is off, or you wrote in another chat than the one chosen. |
| Buttons under risks do nothing | **Allow changes from Telegram** is off, or you are not under **Only these Telegram users**. |
| A user's `/usage` says how to link an account | They have not linked yet: their page › **Telegram** › **Link a Telegram account**. |
| The webhook gets nothing | The address must start with *https://*; check it was saved (it shows *Saved: ...*). |
