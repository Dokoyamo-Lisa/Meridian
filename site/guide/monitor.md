# Watch who connects, and from where

**Monitor** in the top menu: who is connected now, from where and as whom, where the traffic goes,
everything that happened, and the way from your servers to the rest of the internet.

What the panel records is up to you: **Settings › Panel** › **Monitoring** - **Record connecting IPs**
(needed for device limits and to spot shared links), **Record destinations** (the sites people
reach), and **Keep logs for (days)**.

## The tabs

| Tab | What it shows |
|---|---|
| **Online now** | Every device connected right now: its IP, where it is (country, city, network), the user, the server and protocol. Refreshes every 10 seconds; people over their device limit are counted at the top. |
| **IP history** | Every address that connected in a period (**24 h** to **90 days**), with the users it connected as. Search by IP, network, city or country code. |
| **Destinations** | The sites and addresses people reached - per user and server. Needs **Record destinations**; for WireGuard also **Log the names devices look up** on the protocol. |
| **Events** | The timeline: everything that happened, by whom - **Everything** or only **Warnings**, for one server or all. |
| **Blocked IPs** | Addresses you shut out, until when and why. |
| **Health** | Signs of trouble on your servers - see [Keep your servers safe](keep-servers-safe.md). |
| **Ping** | How long the way from each server to an address takes (below). |

## Shut an address out

From **Online now** (or a person's page): **Block** next to the address. Or **Blocked IPs** › **Block
an IP**:

1. **IP address or range** - *203.0.113.7*, or a range such as *203.0.113.0/24*.
2. **For**: **1 hour**, **1 day**, **7 days** or **Until removed**.
3. A **Reason**, for your records.
4. Press **Block**.

**You should see** it under **Blocked IPs**. The address can no longer reach any protocol on your
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
| **IP history** is empty | **Record connecting IPs** is off in **Settings › Panel**. |
| *No destinations recorded* | **Record destinations** is off - or for WireGuard, turn on **Log the names devices look up** on the protocol. |
| A ping monitor loses every round over **ICMP** | The address does not answer ICMP: use **TCP** with a port it serves (such as 443). |
| A blocked address still shows online | It disappears within a few seconds, at the server's next report. |
