# Routing: external nodes and traffic splitting

The **Routing** page decides where your users' traffic leaves the internet, beyond "from the server
they connect to":

- **External nodes** are proxies elsewhere - a provider's, a friend's - that your servers can use as
  exits. A protocol can pass through one, and traffic rules can send traffic there.
- **Traffic rules** send matching traffic - some sites, domains, countries, addresses, ports,
  BitTorrent, or everything - directly, through a proxy pass, to a load balancer, or nowhere.
- **Load balancers** share the traffic a rule sends them among several exits.

Everything here also works through the API (`/api/external-nodes`, `/api/routing`, see
[api.md](api.md)) and the MCP tools (see [mcp.md](mcp.md)).

## External nodes

### What can be imported

**Routing › External nodes › Import nodes** takes any of these:

| Kind | Notes |
| --- | --- |
| VLESS | TCP, WebSocket, gRPC, HTTPUpgrade or XHTTP, with TLS or REALITY; the Vision flow |
| VMess | AEAD (alterId 0), the same transports, with TLS or without (VMess encrypts by itself) |
| Trojan | the same transports, always with TLS |
| Shadowsocks | AEAD ciphers (AES-GCM, ChaCha20) and Shadowsocks 2022, also with a server and a user key |
| Hysteria2 | with or without Salamander obfuscation; a self-signed certificate with its SHA-256 (`pinSHA256`); a port range uses its first port |
| WireGuard | a private key, the server's public key, the tunnel address |
| HTTPS proxy | `https://user:password@host:port` |

…written as:

- **share links**, one per line (`vless://`, `vmess://`, `trojan://`, `ss://`, `hysteria2://` or
  `hy2://`, `wireguard://`, `https://`);
- **a subscription's content** - the base64 list providers hand out;
- **a Clash / mihomo file** with a `proxies:` list;
- or **a subscription address**: the panel fetches it once, over HTTPS only and only from a public
  address (checked when it connects, so a name that points into your own network gets nowhere).
  Its nodes are copied: later changes at the provider are not followed - import again to add new
  ones.

One import takes up to 4 MB of text and 2000 nodes; an account holds up to 2000 external nodes.
Nodes you have already imported are skipped, and a name that is taken gets a number
(`HK`, `HK 2`). The answer lists every entry that was left out, with the reason.

### What is refused, and why

| Refused | Why |
| --- | --- |
| Nodes that turn certificate checks off (`allowInsecure`, `insecure`, `skip-cert-verify`) | Anyone on the way could read and change the traffic. A Hysteria2 node with the certificate's SHA-256 is fine: the pin is checked instead |
| Unencrypted nodes: VLESS or Trojan without TLS or REALITY, SOCKS5, plain HTTP proxies, Shadowsocks `none` | The traffic would cross the internet readable between your server and the node |
| TUIC, AnyTLS, ShadowsocksR, Snell, NaiveProxy, Mieru, Juicity, Hysteria (version 1), ShadowTLS, SSH, Brook | Xray on your servers cannot connect to them |
| The HTTP/2, QUIC and mKCP transports, TCP with an HTTP header disguise, VMess with alterId above 0, VLESS post-quantum encryption, Shadowsocks plugins | Xray no longer supports them, or not as an exit |
| Shadowsocks with an old stream cipher (`aes-256-cfb`, `rc4-md5`, ...) | Those ciphers are broken, and Xray no longer has them: use an AEAD or 2022 method |
| A loopback address (the server itself), a link-local address (the cloud's metadata service, `169.254.169.254`), a multicast address, or a name that only means something inside a network (`.local`, `.internal` - like `metadata.google.internal` -, `.lan`, `.home.arpa` ...) | Your servers must never be pointed at themselves or at their provider's internal services |
| A TLS node on a bare IP address without its server name (`sni`) | The certificate could not be checked |

### Credentials are never shown again

Once imported, a node's passwords and keys stay with the panel: the lists, the API and the MCP
tools show only its name, kind, address and port. The servers that use it get them in their
configuration (a server's **What the server gets**, which needs full access, shows that), and
backups contain them like every other key. When a node's address or credentials change, give it a
**new link** in its **Edit** dialog - the servers using it switch at once.

### Using a node as a proxy pass

In a protocol's settings, **Proxy pass** lists your external nodes below the protocols on other
servers. Choose one, and that protocol's traffic leaves the internet at the node - its users appear
there. Only Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP) can pass through an
exit. A protocol that passes through an external node can itself be the exit of a proxy pass from
another server (a relay): the chain then has its two passes.

### Turning a node off, or removing it

Turned off, a node takes no traffic, and **everything that uses it blocks that traffic** - the
protocols passing through it and the rules sending traffic there. It never leaves from your own
servers instead: people who chose to appear at the node must never show up somewhere else without
anyone deciding that. Turn it on again and they continue. Load balancers simply leave it out while
it is off.

Removed, a node is gone for good: protocols and rules that used it keep blocking that traffic until
you choose another exit for them (or turn their pass off), and load balancers lose it as a member.
Its number is never given to another node, so nothing ever starts using a new node by accident. The
panel asks before turning off or removing a node that is in use, and says what will be blocked.

### Checking a node from a server

**Check** asks one of your servers to reach the node: it opens a connection to it - for a node behind
TLS or REALITY also a TLS handshake, checked against the node's server name - and says whether that
worked and how long it took. Nothing is sent through the node. Hysteria2 and WireGuard nodes speak
UDP and cannot be checked this way; the check needs agent 1.0 or later on that server.

## Traffic rules

### What they apply to

Rules split the traffic of the **Xray protocols** - VLESS, VMess, Trojan, Shadowsocks, SOCKS5 and
HTTP. Hysteria2 and WireGuard traffic does not go through Xray and is never split.

Each rule applies to **every server**, **some servers** or **some protocols**. On each server it
covers, it takes the traffic of the users connected there - also on protocols that pass through
another server: a rule can send some of their sites out directly instead.

Traffic that passes through a server from other servers - other servers' proxy passes and their
rules' traffic - **never follows that server's rules**: a chain stays the way the server it started
from chose it.

### What a rule matches

| Condition | What to write |
| --- | --- |
| **Sites** | Xray's site lists by name: `openai`, `anthropic`, `google`, `youtube`, `netflix`, `disney`, `spotify`, `telegram`, `twitter`, `facebook`, `instagram`, `tiktok`, `github`, `apple`, `microsoft`, `steam`, `cn`, `geolocation-!cn`, `category-ads-all`, ... - every list in the [domain list community's data folder](https://github.com/v2fly/domain-list-community/tree/master/data). `name@attribute` (e.g. `google@cn`) picks part of a list |
| **Domains** | `example.com` - that domain and its subdomains; `full:www.example.com` - exactly that name; `keyword:example` - every name containing it; `regexp:^ad\.` - a regular expression (Go syntax) |
| **Countries** | Two-letter codes, e.g. `CN`, `IR` |
| **Addresses** | IPs or ranges: `1.1.1.1`, `91.108.4.0/22`, `2001:db8::/32` |
| **Ports** | `443`, or a list with ranges: `80,443,8000-9000` |
| **Network** | TCP, UDP, or both |
| **BitTorrent** | BitTorrent connections, recognized by their first bytes |
| **Everything** | All traffic of the protocols the rule applies to - for a last rule, say |

A connection matches a rule when its **name** is among the sites and domains **or** its **address**
is among the countries and addresses - and, when given, its port, network and BitTorrent match too.

**Countries and addresses match connections made to an address.** Most apps send the server the
name of the site they want (`chatgpt.com`, not an IP), and then only sites and domains can match:
the server never looks the name up for the rule. When an app sends only an address, Meridian's
protocols still read the name from the connection itself (the TLS server name, the HTTP host), so
site rules match those connections too. So for a country, **pair it with its site list**: `cn` as a
site and `CN` as a country catch Chinese sites whichever way an app asks for them. (On a server
without nftables Xray looks names up before its rules, so there countries and addresses match
names as well.)

Site lists come with Xray on each server. A name its list does not have (a typo) makes Xray refuse
the whole configuration: the server keeps running its previous one, and the Routing page shows which
name it refused - correct the rule and it applies.

Private addresses (the servers' own networks) are always blocked before any rule, so a rule cannot
open them (`private` is not accepted as a country). A rule holds up to 2000 sites, domains,
countries and addresses, all rules together 10000: every server gets them all - use site lists for
long ones.

### Where it sends the traffic

| Send it | What happens |
| --- | --- |
| **Directly** | It leaves from the server itself - from a protocol's own address, when it has one - also for protocols that pass through another server |
| **Proxy pass** | Through a protocol on one of your servers (the traffic leaves there, or wherever that protocol passes on to) or through an external node |
| **Load balancer** | One of the load balancer's members takes it (see below) |
| **Block** | The connection is refused |

A server reaches a protocol on another server with its own credential there (it appears at the
exit as `r<its server id>`, never as one of the exit's users). **On the exit protocol's own server**,
the traffic simply leaves the way that protocol's own traffic does - directly, from its own address,
or through its own proxy pass - so a rule "AI sites through Tokyo" covering every server sends Tokyo's
own users' AI traffic out from Tokyo too. WireGuard protocols cannot be exits.

If an exit cannot be used on a server - turned off, removed, a chain longer than two passes, one
that would come back to the server itself - that rule's traffic is **blocked there, never sent out
directly instead**, and the page says so (see *What does not work as written* below).

### Order

The **first rule that matches** decides. Move rules with the arrows; put narrow rules (one site to
block) before broad ones (everything through an exit). What no rule takes leaves as it always did:
directly, or through its protocol's own proxy pass. [Architecture](architecture.md#traffic-rules-on-a-server)
lists where traffic rules sit among a server's other Xray rules.

### Applied live

Adding, changing, reordering or removing a rule reaches every server it covers within seconds,
through Xray's API: nothing restarts and nobody is disconnected. The one exception is the latency
checks of a fastest-first load balancer (below).

### What does not work as written

The Routing page lists, per rule and server, what cannot be used as written and what happens
instead - an exit that is turned off or gone (that traffic is blocked), a load balancer with no
member it can use (blocked, or the fallback of one that picks the fastest), a rule whose servers or
protocols were all removed (it does nothing - choose where it applies), a rule that reaches no Xray
protocol, and a site list or country Xray refused. A server's own page shows the same for that
server, and a protocol's **Turn off** and **Remove** say which rules send traffic through it.

When a server or protocol is removed, rules drop it from where they apply; a rule left with none
applies nowhere (never everywhere) until you choose again. Load balancers lose it as a member. Rules
that sent traffic to a removed exit keep it as their exit and block that traffic until you choose
another.

## Load balancers

A load balancer shares the traffic of the rules that send traffic to it among its **members** -
protocols on your servers, external nodes, and **the server itself** (directly):

| Picks | How |
| --- | --- |
| **At random** | Each connection goes to a member at random |
| **Taking turns** | Each connection goes to the next member |
| **The fastest** | Xray checks every member once a minute (a request to Google's `generate_204` page through it) and uses the fastest that answers |

The fastest needs Xray's latency checks, which start only with an Xray restart. So the first time a
server uses such a load balancer, its page shows **restart needed** - nothing restarts until you
click **Restart now** - and until then it picks at random. When no load balancer needs the checks
any more, they stop with the next restart, whenever that comes - no restart is asked for that.

On a member protocol's own server, that member leaves the way the protocol's own traffic does. A
member turned off or removed is left out. When no member can be used on a server, the traffic sent
to the load balancer there is **blocked** - it never leaves directly instead - and the page says
so. The one exception is a load balancer that picks the fastest: it has a **fallback**, where
traffic goes when no member answers its latency checks - blocked (the default) or directly - and
the same fallback applies when none of its members can be used. (Xray knows that members are down
only from those checks, so the other two kinds have no fallback.)

A load balancer cannot be removed while a rule sends traffic to it. Load balancers apply live with
agent 1.0 or later; an older agent applies them with one Xray restart (upgrading the agent
disconnects nobody).

## Examples

**AI sites through Tokyo.** Rule *AI sites*: sites `openai, anthropic`, every server, **Proxy pass**
through *Tokyo · REALITY*. Users on every server reach these sites from Tokyo's address; on Tokyo they
leave directly.

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"AI sites","match":{"sites":["openai","anthropic"]},"target":"node:12"}' \
  https://panel.example.com/api/routing/rules
```

**Chinese sites directly.** Your users in China connect to a Hong Kong server whose protocols pass
through Tokyo. Rule *China directly*: site `cn` and country `CN`, **Directly**, first in the list -
Chinese sites then leave from Hong Kong, everything else still through Tokyo.

**Ads blocked.** Rule *Ads*: site `category-ads-all`, **Block**.

**Streaming spread over three exits.** Load balancer *Streaming*: members *Frankfurt · REALITY*,
*External · Provider Tokyo* and *the server itself*, picking the fastest, fallback block. Rule
*Streaming*: sites `netflix, disney, youtube`, **Load balancer** *Streaming*. Each server shows
*restart needed* once for the latency checks; until you restart it, it takes the members at random.

## What Meridian cannot count at an external node

Usage is counted where users connect - on your servers - per user, protocol and day, exactly as
always, wherever the traffic then leaves; destinations are recorded there too. Traffic sent to an
exit also counts toward the server's own bandwidth, and on one of your servers used as an exit it
counts toward that server's bandwidth, never as anyone's usage (users are counted once, where they
connect).

At an external node Meridian sees nothing: not its bandwidth or quota, its load, whether it is up
(beyond **Check**), or who else uses it. Its operator sees your users' destinations and the address
of your server - choose providers you trust with that.
