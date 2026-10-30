# Watch who connects, and from where

**Monitor** in the top menu: who is connected now and how fast, from where and as whom, where they
went, everything that happened, and the way from your servers to the rest of the internet.

What the panel records is up to you: **Settings › Panel** › **Monitoring** - **Record connecting IPs**
(needed for device limits and to spot shared links), **Record destinations** (the sites people
reach), and **Keep logs for (days)**.

## The tabs

| Tab | What it shows |
|---|---|
| **Live** | Everyone connected right now, by user, fastest first. Each user's row has their **speed now** - download and upload over all their servers and protocols - and under it each connection: the address, where it is (country, city, network), the server and protocol, and that address's speed on that server. At the top: users and addresses online, and all users' download and upload. Updates every 5 seconds; people over their device limit are counted at the top. |
| **History** | What was seen before, three views: **Addresses** - every address that connected in a period (**24 h** to **90 days**), with the users it connected as; **Destinations** - the sites people reached, by **visits** (connections), per user and server; **Blocked** - addresses you shut out, until when and why. |
| **Events** | The timeline: everything that happened, by whom - **Everything** or only **Warnings**, for one server or all. |
| **Health** | Signs of trouble on your servers - see [Keep your servers safe](keep-servers-safe.md). |
| **Ping** | How long the way from each server to an address takes (below). |

**Speed now, counted once.** A user going through a proxy pass (a protocol that sends its traffic
on through another server) is counted on the server they connect to, once - not again on the exit.
The line under the figures also shows what all servers' network carries; there a proxy pass counts
on both servers, so it can be about twice the users' speed. A user's speed is what their protocols
carried; an address's is everything that crossed the network, with the protocols' own overhead, so
it reads a little higher. Servers whose agent is older than 1.4.3 do not measure speed: the tab
names them - upgrade their agent (nobody is disconnected).

**Destinations count visits.** Every protocol counts how often a site was reached; how many bytes
went to each is not measured, so none is shown. Destinations need **Record destinations**; for
WireGuard also **Log the names devices look up** on the protocol.

## Shut an address out

From **Live** (or a person's page): **Block** next to the address. Or **History** › **Blocked** › **Block
an IP**:

1. **IP address or range** - *203.0.113.7*, or a range such as *203.0.113.0/24*.
2. **For**: **1 hour**, **1 day**, **7 days** or **Until removed**.
3. A **Reason**, for your records.
4. Press **Block**.

**You should see** it under **History** › **Blocked**. The address can no longer reach any protocol on your
servers and its open connections are dropped; SSH and other services on the servers are not
affected. **Unblock** lets it in again at once.

> **Good to know:** Blocking an address does not pause anyone: the person behind it can still
> connect from elsewhere. To stop a person, **Pause** them on their page.

## Ping monitors

Measure the way from your servers to a site, a DNS resolver or another server:

1. **Monitor** › **Ping** › **Add a monitor**.
2. **Address** - a name or an IP on the internet, such as *1.1.1.1*; a **Name** for the charts.
3. **How**: **ICMP** (like ping) or **TCP** (the time a connection takes - works where ICMP is
   filtered; give the **Port**, such as *443*).
4. **Every** - how often; a round is three probes. **Measured from**: **All servers** or some.
5. Optional: **Show on the status page** - visitors see its name and the round trips in a server's
   details, never the address.
6. Press **Add**.

**You should see** each server's round trip and the rounds lost, as they come in.

## If something goes wrong

| What you see | What to do |
|---|---|
| **History** › **Addresses** is empty | **Record connecting IPs** is off in **Settings › Panel**. |
| *No destinations recorded* | **Record destinations** is off - or for WireGuard, turn on **Log the names devices look up** on the protocol. |
| A ping monitor loses every round over **ICMP** | The address does not answer ICMP: use **TCP** with a port it serves (such as 443). |
| A blocked address still shows online | It disappears within a few seconds, at the server's next report. |
