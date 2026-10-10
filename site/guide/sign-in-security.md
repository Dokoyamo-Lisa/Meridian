# Sign-in and the panel's safety

Whoever signs in to your panel controls every server. This page makes sure that is only you: a
passkey or two-factor sign-in, the browsers you are signed in on, a check that a person is signing
in, who may open the site at all, and maintenance mode.

Everything here is in **Settings › Security**, except the site rule (**Access**).

## Passkeys - the best way in

A passkey signs you in with your phone's or computer's own lock (Face ID, a fingerprint, Windows
Hello, a security key). It cannot be guessed and works only on your panel, so a fake sign-in page
gets nothing.

1. **Settings › Security** › **Passkeys**.
2. A **Name** - which device or password manager it is on.
3. **Add a passkey**, and follow your device.

**You should see** *Passkey added - you can sign in with it now*, and it in the list. Add one on a
second device too, in case you lose the first. (More in [Sign in the first time](first-sign-in.md).)

> **Good to know:** Passkeys need the panel at its own name over HTTPS, such as
> *https://panel.example.com* - not at an IP address.

## Two-factor sign-in

Without a passkey, protect your password with a code from an authenticator app:

1. **Two-factor sign-in** › **Set up**.
2. Scan the QR code with your app (or **Copy key** and type it in).
3. Type the **Code from the app** and press **Turn on**.

**You should see** *Two-factor sign-in is on*. Signing in now needs your password and a code.

## Your password

**Password**: the **Current password**, then the **New password** twice - at least 10 characters; a
few words make a good one. Every other browser is signed out.

## Browsers you are signed in on

**Signed-in browsers** lists each one - the browser, its address and when it was last active
(**this browser** is marked). If you see one you do not recognise, press **Sign out others** (every
browser but this one is signed out; API tokens keep working) and change your password.

## A check that a person signs in (Cloudflare Turnstile)

Every sign-in - to the panel and to people's own pages - can be made to pass Cloudflare's check
first, which stops password-guessing programs:

1. At Cloudflare: **Turnstile** › **Add widget**, with the panel's domain (choose **Invisible** and it
   never shows). Copy its two keys.
2. **Settings › Security** › **Sign-in check (Cloudflare Turnstile)**: paste the **Site key** and the
   **Secret key**.
3. **Check and turn on**.

**You should see** **On**. The panel turns it on only after Cloudflare accepted a check made with
those keys - wrong keys cannot lock you out.

Even without it, failed sign-ins are held back: three wrong passwords from one address in 15
minutes shut that address out for a while. Addresses you signed in from lately are never held back,
and a passkey always gets in.

## Who may open the site

**Access** › **Who may open this site**: keep countries away from the panel, people's pages, the
status page or the subscription links (each can be ticked).

1. **Block these countries** or **Allow only these countries**, and pick them.
2. Under **Applies to**, tick what it covers: **This panel and its API**, **Users' own pages**, **The
   status page**, **Subscription links**.
3. **Exceptions**: addresses that always get in (your own, for example).
4. **Save**.

**You should see** what was refused in the last 24 hours below it. Servers always reach the panel,
whatever the rule.

> **Careful:** The panel refuses a rule that would lock you out right now (it says *This rule would
> lock you out*) - but your address may change while travelling. Add your home address under
> **Exceptions**.

## Maintenance mode

While you work on the panel - an upgrade, a restore - only you can sign in:

1. **Settings › Security** › **Maintenance mode**, an optional **A line for users** (*back at 18:00
   UTC*).
2. Switch it on and confirm with **Start maintenance**.

People are signed out of their own pages and see *Maintenance in progress* there and on the status
page. **Their connections keep working.** Switch it off when you are done.

## If something goes wrong

| What you see | What to do |
|---|---|
| You forgot your password | On the panel's host: `sudo -u meridian meridian reset-password --data /var/lib/meridian admin` - it prints a new one and turns two-factor off. |
| *not available in your region* | The site rule shuts you out. On the panel's host: `sudo -u meridian meridian reset-site-access --data /var/lib/meridian`, then `sudo systemctl restart meridian` (proxies keep running). |
| *too many failed sign-ins from your address* | Wait - the message says how long - or sign in with a passkey. Restarting the panel clears it. |
| Sign-ins fail because Turnstile cannot be reached | Add `MERIDIAN_NO_TURNSTILE=1` to `/etc/meridian/meridian.env`, `sudo systemctl restart meridian`, sign in and fix it, then remove the line. |
| *Add a passkey* is missing | The panel is opened at an IP address or over plain HTTP: open it at its name over HTTPS. |
