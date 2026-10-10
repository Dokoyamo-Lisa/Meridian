# Everyday tasks

The things you do once the service runs. Each one takes a minute.

## See how everything is doing

**Overview** shows the servers online, the devices connected now, the traffic right now and today,
and a list called **Needs attention** when something does - a user who used up their data, an access
that ended, a server that stopped reporting.

## Limits and alerts

A user's **quota**, **end date** and **device limit** never cut anyone off by themselves. When one is
reached, the panel tells you:

- the user is marked in **Users** (for example *Over quota*, *Expired*, *Near quota*);
- **Overview › Needs attention** lists it;
- if you set it up, a message arrives on [Telegram](telegram-alerts.md).

You then decide: give more (**Edit**), start a new period, or pause them.

## Pause and resume someone

1. Open **Users** and click the person.
2. Press **Pause**.

   ![A user's page with the Pause button](../img/guide/user-actions.webp)

3. The panel says exactly what will happen. Press **Pause now**.

   ![Pause Sam? Every device using this user's link is disconnected now](../img/guide/pause-confirm.webp)

**You should see** *Paused* next to their name, and *Paused just now. Devices cannot connect until
you resume it.* To let them back in, press **Resume** - it works at once, no questions asked.

## Give more data, or more time

Open the person and press **Edit**: change **Quota per cycle**, **Valid until** or **Devices online
at once**, then save. The servers follow within seconds; nobody is disconnected.

To start over on a plan (a new month, say): **⋯ › New period on a plan…**. To set this cycle's
usage back to zero: **⋯ › Reset usage…**.

## Plans: the same limits for many people

**Users › Plans** keeps templates: a quota, how long it lasts, a device limit, a price for your own
records. Pick a plan in the **New user** form, and the person gets all of it at once.

## A new password or a new link for someone

| | |
|---|---|
| They forgot their password | Their page in the panel › **New password** (shown once - send it to them). |
| Their link reached someone else | **⋯ › Issue a new link…** - the old one stops working; they add the new one from their page. |
| A device was stolen | **⋯ › Reset credentials…** - the stolen device stops working; the other devices keep the same link and only need to update their profile in the app. |

## The status page

**Settings › Panel › Status page** decides who sees how your servers are doing:

| Where it is | What it means |
|---|---|
| **Only at /me** | Only your users, after signing in to their page (the default). |
| **Front page** | Everyone who opens your panel's address sees the globe and the servers; the panel moves to `/overview`. |
| **At /status** | The same, at `https://panel.example.com/status`. |
| **Its own domain** | On a name of its own, such as `status.example.com`; the panel never opens there. |

![The status page settings](../img/guide/settings-status-page.webp)

> **Good to know:** The status page never shows your users, prices, protocols or keys, and shows IP
> addresses only if you turn that on.
