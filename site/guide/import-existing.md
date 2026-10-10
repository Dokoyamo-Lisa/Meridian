# Bring an existing setup

Already running **Xray**, **V2Ray**, **x-ui**, **3x-ui**, **sing-box** or **Hysteria2** on a
server? Rosélune can take over its protocols with the same keys and passwords, so people's devices
keep working - nobody needs a new link from you to connect the first time.

You need: the server added to the panel and **Online** (see [Add a server](add-a-server.md)).

## 1. Look at what is there

1. On the server's page, press **Import existing setup**.
2. Press **Scan the server**. The agent reads the other software's configuration - it changes
   nothing.

**You should see** each piece of software it found, whether it runs, and its protocols, each with
its port and how many people use it.

## 2. Pick what to bring over

Tick the protocols you want. Each one keeps its keys, and each of its people keeps their ID or
password: the panel matches them to your users by name, or creates them.

Now choose:

- **Take over: stop the old service and use the same ports** - ticked: the old service is stopped
  and disabled, and Rosélune serves the **same ports**: devices keep working with what they have,
  without a new link. The panel asks first, because everything else that service ran stops too.
- Not ticked: nothing is stopped; the imported protocols get **free ports**, so people need their
  new link from Rosélune.

Press **Import** - it shows how many you ticked, for example **Import 3** (and, with take-over,
confirm with **Stop it and take over**).

**You should see** *Imported 3 protocols, 12 user(s) matched by name*, and the protocols on the
server's page. Notes may follow, for example a port that was in use and was changed - those people
need their new link.

If the import created new users, a window (*5 users are ready*) lists each one's sign-in and
subscription link. Press **Copy everything** before **Done**: their passwords are shown only now.

## 3. Give people their new link

Everything you imported is in **Users**. Their old setups keep working (with take-over, on the same
ports), but from now on their links come from Rosélune: send each person their link from their page
(**Copy link** or the QR code), so new devices and changes reach them.

## Good to know

- **What cannot come over is said**, protocol by protocol, with the reason - for example a
  combination no app could use. Those stay as they were (or stop, with take-over: the panel says so).
- **A certificate that cannot be read** is replaced by a new self-signed one: those devices must
  update their subscription (the import says which).
- **One key for everyone** (single-user Shadowsocks 2022) becomes one key per person: those devices
  must update their subscription too.
- After the import, those protocols behave like any other: limits, usage per person, cutting off.

## If something goes wrong

| What you see | What to do |
|---|---|
| *No other proxy software was found* | It is not one Rosélune reads (Xray, V2Ray, x-ui, 3x-ui, sing-box, Hysteria2), or its configuration is somewhere unusual. Add the protocols by hand and give people new links. |
| **Import existing setup** is greyed out | The server is not online - wait until it shows **Online**. |
| A port was in use and changed | Another program uses it. Send those people their new link, or free the port and edit the protocol back to it. |
| Devices stopped working after a take-over | Look at the notes the import showed: those protocols changed (a key or a certificate). Send the new links. |
