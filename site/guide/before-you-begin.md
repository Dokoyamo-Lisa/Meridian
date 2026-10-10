# Before you begin

Ten minutes of checking now saves an hour later. You need four things: a server, a way to log in to it
as root, a domain name that points at it, and two free ports. Each one has a check below. Do the
check, compare what you see with **You should see**, and only then go on.

> **Note:** In this guide, `203.0.113.10` stands for your server's IP address and `panel.example.com`
> for your panel's name. Type your own instead.

## 1. A server for the panel

The panel runs on a small Linux server that you rent from a hosting provider (a "VPS"). Any of these
works:

| | |
|---|---|
| **System** | Ubuntu 22.04 or 24.04, or Debian 12. Other Linux systems with systemd work too. |
| **Size** | 1 CPU, 1 GB of memory, 10 GB of disk. |
| **Processor** | 64-bit: Intel/AMD (`x86_64`) or ARM (`aarch64`). |

> **Tip:** The panel can run on the same server as your proxies. For a start, one server is enough.

Your provider gives you the server's **IP address** and either a **root password** or an **SSH key**.
Keep them at hand.

## 2. You can log in as root

Open a terminal on your computer (on Windows: PowerShell; on a Mac: Terminal) and log in:

```bash
ssh root@203.0.113.10
```

The first time, it asks `Are you sure you want to continue connecting?`. Type `yes` and press Enter,
then enter the root password if it asks for one.

Now check who you are:

```bash
whoami
```

**You should see:**

```text
root
```

If you see another name (for example `ubuntu` or `debian`), become root first, then check again:

```bash
sudo -i
whoami
```

Check the system and the processor while you are here:

```bash
grep PRETTY_NAME /etc/os-release
uname -m
```

**You should see** something like:

```text
PRETTY_NAME="Ubuntu 24.04.5 LTS"
x86_64
```

`x86_64` or `aarch64` are both fine.

## 3. A domain name that points at the server

The panel needs a name, such as `panel.example.com`, so that it can use HTTPS (the lock in the browser).
Use a domain you own, from any registrar, and add one DNS record:

| Type | Name | Content | Proxy |
|---|---|---|---|
| A | `panel` | your server's IP address | DNS only |

> **Warning:** If your domain is on Cloudflare, the cloud next to the record must be **grey ("DNS
> only")**, not orange. The installer checks that the name leads straight to your server, and the
> HTTPS certificate needs it.

New records can take a few minutes to work. Then check on the server:

```bash
getent ahostsv4 panel.example.com | head -1
curl -4 -s https://1.1.1.1/cdn-cgi/trace | grep ip=
```

**You should see** the same address twice: first what the name points to, then the server's own
address.

```text
203.0.113.10      STREAM panel.example.com
ip=203.0.113.10
```

If the first line is empty, the record is not there yet (or the name has a typo). Wait five minutes
and try again. If the two addresses differ, fix the record, or turn the Cloudflare cloud grey.

## 4. Ports 80 and 443 are free

The panel answers on ports 80 and 443. Check that nothing else uses them:

```bash
ss -ltnp '( sport = :80 or sport = :443 )'
```

**You should see** only the header line:

```text
State Recv-Q Send-Q Local Address:Port Peer Address:PortProcess
```

If there is a line under it, another program uses the port. The name in `users:(("...` tells you
which one: often `nginx`, `apache2` or `caddy`, a website on this server. Use another server, or run
the panel behind that web server instead ([Setup in detail](../../docs/getting-started.md) shows how).

Many providers also have a **firewall in their own control panel** (it may be called a "security
group" or "firewall rules"). Allow incoming **TCP 80** and **TCP 443** there.

## Ready?

| Check | You should see |
|---|---|
| `whoami` | `root` |
| `uname -m` | `x86_64` or `aarch64` |
| The name and the server | the same IP address twice |
| `ss -ltnp ...` | only the header line |

All four fine? Go on to [Install the panel](install.md).
