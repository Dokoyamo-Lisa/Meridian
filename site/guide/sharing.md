# Share a server with another panel

Two people, each with their own Rosélune panel, can use one server: its owner shares it, and the
other panel runs its own protocols and users there - on ports and networks the owner's do not use.
Neither sees the other's users. A server can be shared with two other panels at most.

What stays the owner's: the **Console**, upgrades of the agent and the cores, the server's **Country
rule** and relaying.

## 1. The other panel asks for a share code

In the panel that will **use** the server:

1. **Servers** › **Add server**, and choose **Shared with me**.
2. A **Name** for it (and, if you know it, the **Address users connect to**).
3. Press **Add and show the share code**.

**You should see** **Waiting for its owner** and a share code (*meridian-share:...*). Press **Copy
share code** and send it to the server's owner.

> **Careful:** The code holds the key this panel uses for the server - send it only to its owner. A
> new one (**⋯** › **New share code…**) makes the old one useless.

## 2. The owner shares the server

In the panel that **owns** the server:

1. Open the server's page and find **Sharing**.
2. Press **Share with another panel**, paste the code, and press **Share**.

**You should see** *Tokyo is shared - the other panel sees it within a minute*, and the other panel in
the list with a green dot and **connected**.

## 3. The other panel sets it up

Back in the panel that uses it: the server shows **Online** with the mark **shared with you**. Add
protocols and users as on your own servers. A port the owner's panel already uses is refused as *in
use by another program on the server* - leave **Port** empty and a free one is picked.

## Stop sharing

- **The owner**: on the server's page, under **Sharing**, **Stop sharing** - everything the other
  panel runs there (its protocols, users and forwards) is removed, and its users on that server are
  disconnected.
- **The other panel**: **⋯** › **Leave server…** (type its name to confirm) - the same, from
  its side. To use it again, the owner must share it again.

## If something goes wrong

| What you see | What to do |
|---|---|
| **Share with another panel** is greyed out: *Upgrade the agent to 1.0 first* | **⋯** › **Upgrade agent** on the server, then try again. |
| ... *Two panels at most* | It is already shared twice: stop one of them first. |
| The other panel stays **not reached yet** | The server cannot reach that panel's address: check that panel's **Public URL** (**Settings › Panel**) is reachable from the server. |
| The code is refused | It was replaced by a new one, or copied only in part: ask for the code again. |
