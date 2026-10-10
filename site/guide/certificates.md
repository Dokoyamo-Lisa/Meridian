# Certificates

Protocols with TLS - **Trojan · TLS**, **VMess · WebSocket · TLS**, VLESS with TLS, **Hysteria2**,
**AnyTLS** and the HTTPS proxy - show apps a certificate. You choose which, under **Certificate** in
the protocol's form. (REALITY needs none: it borrows a well-known site.)

| Choice | When | What apps do |
|---|---|---|
| **Self-signed (no domain)** | You have no domain, or want it quick | The apps that can pin a certificate check exactly this one; apps that cannot are left out of their subscription. Links never turn checks off. |
| **Let's Encrypt (free, automatic)** | You have a domain pointing at the server | Every app trusts it. The agent gets it and renews it by itself. |
| **My own certificate** | You bought one, or another system makes them | Every app trusts it if it comes from a public authority. |
| **Shared certificate** | One certificate (a wildcard such as *\*.example.com*) for many protocols and servers | Kept once in **Settings › Certificates**; replaced once for all of them. |

## Let's Encrypt

1. At your DNS provider, point the domain (an **A** record, and **AAAA** if the server has IPv6) at
   the server. If it is at Cloudflare, keep it **DNS only** (grey cloud).
2. In the protocol's form: **Certificate** › **Let's Encrypt (free, automatic)**, and type the name
   under **Domain**, for example *tokyo.example.com*.
3. Make sure TCP port **80** is free on the server and open at your provider: Let's Encrypt checks the
   domain there. (A server whose provider decides the ports: the panel says which port is used
   instead.)
4. Press **Add protocol** (or **Save**).

**You should see** the protocol working within a minute or two. Until the certificate arrives, the
server's page says *waiting for the Let's Encrypt certificate for tokyo.example.com* - with the
reason, if Let's Encrypt cannot reach the server.

Renewal is automatic, about a month before it expires, without disconnecting anyone.

## Your own certificate

1. **Certificate** › **My own certificate**.
2. **Domain**: the name the certificate is for.
3. Paste the whole chain under **Certificate chain (PEM)** (the server's certificate first, then the
   intermediate ones - *fullchain.pem*) and the key under **Private key (PEM)**.
4. Save.

The panel refuses a pair that does not belong together, an expired certificate, or one that is not
valid for the domain - and says which. It tells you about two weeks before it expires (an alert, and a
notification if you turned them on).

## A shared certificate

For a wildcard certificate used by many protocols, or one your own system renews:

1. **Settings › Certificates** › **Add certificate**: a **Name**, the **Certificate chain (PEM)** and
   the **Private key (PEM)**. Press **Save**.
2. In each protocol's form: **Certificate** › **Shared certificate**, pick it, and type a **Domain** it
   covers (for *\*.example.com*: *tokyo.example.com*).

**You should see**, under **Settings › Certificates**, the certificate with *1 of 1 serving it* once
the server uses it.

To replace it (renewed): **Replace or rename**, paste the new chain and key, **Save**. Every server
that uses it gets it: Xray loads it within ten minutes and AnyTLS at once, without disconnecting
anyone; Hysteria2 restarts briefly.

> **Tip:** Under **Renewing automatically**, the same page shows a ready-made command for your ACME
> client (acme.sh, certbot, Caddy...) to replace it after each renewal - it needs an API token with
> full access (**Settings › API & MCP**).

## If something goes wrong

| What you see | What to do |
|---|---|
| *waiting for the Let's Encrypt certificate* stays, with *connection refused* or *timeout* | TCP port 80 is closed at your provider or used by another program on the server (a web server). Free it; the agent tries again by itself. |
| ... with *DNS* or *NXDOMAIN* | The domain does not point at this server yet, or it is proxied at Cloudflare. Fix the record and wait a few minutes. |
| *this certificate is self-signed* when pasting your own | Choose **Self-signed (no domain)** instead: the panel makes one and apps pin it. A self-signed certificate of your own would work in no app. |
| *Apps will refuse this certificate* on a shared certificate | It is not from a public authority, or the chain is missing: paste the full chain (*fullchain.pem*). |
| Some apps are missing from a protocol's **Works** box | With a self-signed certificate, apps that cannot pin one are left out: use Let's Encrypt for them. |
