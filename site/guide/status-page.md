# The status page

A public page that shows how your servers are doing - where they are on a globe, whether they are
up, their load and traffic - like a provider's status page. Your users also see it after signing in
to their own page. It never shows your users, prices, protocols or keys.

Everything is in **Settings › Panel** › **Status page**; each server's own part is on its page.

## 1. Choose where it is

**Where it is**:

| Choice | Who sees it |
|---|---|
| **Only at /me** (the default) | Only your users, after signing in to their page. |
| **Front page** | Everyone who opens the panel's address; the panel itself moves to */overview*. |
| **At /status** | Everyone, at *https://panel.example.com/status*. |
| **Its own domain** | Everyone, at a name of its own, such as *status.example.com* - the panel never opens there. |

For **Its own domain**: first point the name's DNS at the panel's server (as for the panel's own
domain), then type it under **Domain**. The panel gets its certificate on the first visit. Users are
then told to sign in at *https://status.example.com/me*.

## 2. Choose what it shows

- **Show the servers to everyone** - each server's place, whether it is up, its load, bandwidth,
  traffic and the day its paid period ends. Off: visitors see only the sign-in.
- **Show the overview** - the globe, the totals and the latest events.
- **Show the events** - when servers went offline and came back, over 30 days.
- **Charts in a server's details** - which of **CPU**, **Memory**, **Disk**, **Disk activity**,
  **Network**, **Load**, **Connections**, **Temperature** and **Ping** visitors may open.
- **Show IP addresses** - off by default. On, everyone who opens the page sees your servers'
  addresses - and can test or block them.
- **A line on the sign-in page** - who runs the service, how to reach support.

Press **Save changes**.

**You should see**, at the address you chose, the globe and your servers.

> **Careful:** Leave **Show IP addresses** off unless you have a reason: an address on a public page
> is an address anyone can block.

- **Show each server's network profile** - off by default. On, a server's details on the page gain a
  **Network** part: the address with its end hidden (`45.196.21*.***`), its network (ASN), what it is
  used for, an IP score, what public lists say about its /24, and a graph of its BGP routes. Everyone
  who can open the page sees it, you included.

  The first time someone opens it you should see *Looking this server up…* for a minute or two, then
  the profile; after that it is refreshed once a day. If it still says *Not looked up yet*, the
  panel could not reach the services: wait ten minutes and open it again.

  The full address is never in the page or sent anywhere: RIPE is asked about the /24 and PeeringDB
  about the network. What does become public is the network and the first digits, which narrow a
  server down to a range - leave this off if that matters to you. The score is Rosélune's reading of
  public lists, not a verdict from any provider, and IPv6 addresses have none.

## 3. Each server's place on it

On the server's page, **On the status page** › **Edit**:

- **Show this server on the status page** - untick to leave it off.
- **Name users see** - on the status page, on people's own pages and in their apps (empty: the
  server's name).
- **Set the location by hand** - IP databases often place a data centre at the provider's office.
  **Find a city**, or type the **City**, **Country code**, **Latitude** and **Longitude**.

**Save**.

## Ping charts

A server's details can chart how long the way to an address takes: add a ping monitor with **Show on
the status page** ticked (**Monitor** › **Ping** - see [Watch who connects](monitor.md#ping-monitors)),
and tick **Ping** under **Charts in a server's details**. Visitors see its name, never the address.

## If something goes wrong

| What you see | What to do |
|---|---|
| A server sits in the wrong city | **On the status page** › **Edit** › **Set the location by hand**. |
| Its own domain shows a certificate error | Its DNS does not point at the panel's server yet, or (behind your own reverse proxy) the proxy does not know the name. |
| Visitors see only a sign-in | **Show the servers to everyone** is off. |
| The panel opened at its own domain shows *404* | That is on purpose: the status page's domain never serves the panel. Use the panel's own address. |
