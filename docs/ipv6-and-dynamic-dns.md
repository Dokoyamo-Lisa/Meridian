# IPv6-only servers and dynamic DNS

Rosélune links servers to each other - proxy passes, traffic rules that send traffic to a protocol on
another server, relays, port forwards - and gives users links to them. Normally it uses the public
address each agent reports. Two kinds of servers need more:

- a server whose **IP address changes** (a home connection, a provider that hands out new
  addresses): it is reached by a **dynamic DNS name**;
- a server with **IPv6 only**: servers and devices without IPv6 cannot reach it at all.

## Addresses are followed everywhere

Each agent checks its public addresses every 2 minutes and tells the panel at once when they change
(behind NAT, where it has to ask - Cloudflare first, then DB-IP, over HTTPS - at most every 5
minutes). The panel then records the change (*Public IPv4 of Tokyo changed from … to …*, and the
same for IPv6) and updates every server whose configuration holds that address: proxy passes and
traffic rules that lead there, port forwards that point at it, relays it uses or provides, and the
exceptions servers' country rules make for each other. Nothing restarts; devices on other servers
keep their connections.

Where a server's address is a domain name, links use the name and need no update - which is why a
server whose IP changes should be reached by name.

## A server whose IP address changes

1. Give it a dynamic DNS name: your router, your provider or an updater program keeps a name such as
   `home.example.com` pointing at it - or let the panel do it with Cloudflare (below).
2. When adding the server (or with **Edit** on its page), write the name as its **address** and tick
   **Its IP address changes (dynamic DNS)**.

From then on every link to the server uses the name: users' links, other servers' proxy passes and
traffic rules. The panel resolves the name when you save and every 5 minutes, and compares it with
the addresses the agent reports. The server's page (and the overview's list of what needs attention)
says when they differ, for example:

> home.example.com points at 198.51.100.7 but the server's address is now 203.0.113.9 - links go to
> the old address until your dynamic DNS updates the name

or when the name does not resolve at all. A name that does not answer for a moment is not reported -
only an answer that says something is wrong.

Only servers marked this way are checked: a server whose address is a CDN's name never gets a false
warning.

### Let the panel keep the name up to date with Cloudflare

If the name's domain is in Cloudflare and nothing else updates it:

1. In Cloudflare: **My Profile › API Tokens › Create Token**, template **Edit zone DNS**, for the zone
   (or zones) of your servers' names.
2. In the panel: **Settings › Dynamic DNS**, paste the token, **Test** (it says which zone each name is
   in and what records it has now - nothing is changed by a test), **Save token**. The token is never
   shown again, never logged and can only be set from a signed-in browser.
3. On the server: tick **Keep the name pointing at this server with Cloudflare**. The panel asks you
   to confirm, because from then on it owns that name's A and AAAA records:
   - it sets them to the server's addresses whenever they change, on save, and checks them every 10
     minutes;
   - records are **DNS only** (Cloudflare's proxy would break the protocols) with a 60-second TTL;
   - when the server has no address of one kind (IPv4 or IPv6), the record of that kind is removed;
   - a name that is an alias (CNAME) is left alone, with a message;
   - **no other name** in the zone is ever touched.

Every update and every failure is recorded in the timeline and sent with the *servers*
notifications. A failure says what to do, e.g. *Cloudflare refused the token - check it in Settings ›
Dynamic DNS: it needs the permission Zone - DNS - Edit for the name's zone*.

## A server with IPv6 only

Set **IP version** to **IPv6 only** on the server (Edit). Then:

- **Users' links** carry its IPv6 address (in brackets where an app needs them) or its name. Devices
  on networks without IPv6 cannot reach it - give such users other servers too.
- **Links between servers** must be possible: a server without IPv6 cannot pass traffic to a protocol
  that is reached at an IPv6 address only, and a server without IPv4 cannot reach an IPv4 address.
  The panel refuses such a proxy pass or traffic rule when you save it, with what to do, and shows it
  on the server's page if it becomes true later (an address went away). A server with both IPv4 and
  IPv6 is reached at whichever address the other side can use. A name helps when the exit has both
  kinds and its address was set to just one of them.
- **On a server set to one IP version**, Xray resolves the names of its exits to that version only.

### The panel needs IPv6 too

An IPv6-only server installs its agent from the panel and reports to it, so the panel's address must
have IPv6 (an AAAA record for the panel's domain). The **Add a server** dialog says so when the panel's
address has none. If you cannot give the panel IPv6, the server can reach it through another server
that has both - a relay (see [Operations](operations.md#a-server-that-keeps-losing-the-panel)).

Xray, Hysteria and realm are downloaded from the panel's own mirror first, so GitHub having no IPv6
does not matter.

### Sites that have only IPv4

An IPv6-only server cannot open connections to sites that have IPv4 only, unless:

- its provider offers **DNS64 and NAT64** (many IPv6-only offers do): the agent notices it, names of
  IPv4-only sites get an IPv6 address from the provider's DNS, and the provider carries those
  connections on to IPv4. Nothing to set up in Rosélune; or
- a **traffic rule** sends that traffic - everything, or chosen sites - through an exit that has IPv4:
  a protocol on a server with both, or an imported **external node** such as a WARP WireGuard
  configuration (see [Routing](routing.md)).
