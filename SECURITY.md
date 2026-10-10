# Security

Meridian holds the keys to every server it manages and a record of who connected from where. This
document describes what it protects, how, and what remains the operator's job.

## Reporting a problem

Report vulnerabilities privately - through the repository's private security advisory form
("Report a vulnerability" under the Security tab) - not in a public issue. Include the version
(`meridian version`, `meridian-agent version`), what you did and what happened.

## Threat model

| Asset | Threat | Main protections |
| --- | --- | --- |
| The supervisor account | password guessing, phishing, session theft | passkeys (WebAuthn, user verification required), bcrypt (cost 12), rate limits and network bans, TOTP two-factor with replay protection, HttpOnly + SameSite cookies, CSRF header, sign-out everywhere |
| Users' sign-ins | guessing, a user reaching the panel or another user's data | separate cookie and session table, SameSite=Strict, rate limits, the panel's API refuses user sessions and the user API refuses everything else; covered by tests |
| Servers (root via the agent) | a forged or replayed panel message, tampered downloads | per-server 256-bit secret, signed requests, sealed replies bound to each request, checksum-pinned installer, agent and cores |
| Subscription credentials | link sharing, copied configs | 144-bit link tokens, new link / new credentials actions, per-IP online limits and alerts |
| Connection logs (IPs, destinations) | disclosure | panel DB 0600 under a dedicated user, root-only log files on servers, configurable retention |
| The status page | revealing users, keys or more than you chose to show | the server dashboard is built from a fixed set of fields that never includes users, ports, protocols, keys or prices; visitors get it only while the page shows the servers to everyone, otherwise only a sign-in; IP addresses only while the page is set to show them (off by default), and then to everyone alike; users only their own page; its own domain never serves the panel |
| Client certificate checks | a man in the middle on TLS / Hysteria2 | real certificates are verified by name; self-signed ones are pinned (SHA-256 / the certificate itself) and left out of formats that cannot pin - no format ever turns checks off (a test renders every format to make sure) |
| Server hosts | the panel turning a server into an open proxy to its own network | REALITY targets must be public (or your own site on loopback, with sensitive ports refused), forwards and external nodes cannot reach loopback / link-local / cloud metadata |
| External nodes and traffic rules | a provider's subscription with nodes that weaken encryption or point servers at internal services; the nodes' credentials leaking; traffic leaving somewhere nobody chose | nodes that turn certificate checks off or travel unencrypted are refused; subscriptions are fetched over HTTPS only, from public addresses checked when connecting; credentials are never shown again; each server has its own identity at an exit; traffic whose exit cannot be used is blocked, never sent out directly |
| Access by country | being locked out; abuse from some regions | the panel refuses site rules that would refuse their author, `meridian reset-site-access` to recover; server rules never touch SSH or the agent's own connection |
| AI assistants (MCP) | an assistant taking disruptive action unasked | token scopes, `confirm=true` on every disruptive tool, origin check, no MCP access to the site rule |
| Plugins | code from someone else running with the panel's rights, in your browser or in visitors' | installed, updated and turned on only from a signed-in browser (or the panel's host), off until you agree to exactly what each asks for, warnings that say what that lets it do, `meridian plugins disable` and `MERIDIAN_NO_PLUGINS=1` to recover - see below |

## How it is protected

### Panel and browser

- Serve the panel over HTTPS: `--domain` gets a Let's Encrypt certificate automatically (also for
  the status page's own domain, if you set one). With HTTPS the panel sends HSTS and marks session
  cookies `Secure`. The UI warns while it is on plain HTTP.
- Passwords: 10-72 bytes, bcrypt cost 12. Failed sign-ins take the same time whether or not the
  name exists, and are limited four ways: three tries per address and five per name from addresses
  it does not know (15 minutes, reset by a sign-in that works); an address that fails 3 times in 15
  minutes is shut out of signing in for 15 minutes, then an hour, four hours and a day if it comes
  back; a name that fails 5 times from addresses it does not know takes one try a minute from those;
  a network that fails 10 times within an hour - its IPv4 /24 or IPv6 /24 - cannot sign in for a day.
  An address that signed in to the account in the last 30 days is never slowed down at its name or
  held back by its network, so failing on purpose cannot lock the supervisor out. Every failure, and
  every network ban, is in the timeline and the *security* notifications.
- Passkeys (WebAuthn) for the panel's accounts: discoverable credentials with user verification
  required, no attestation kept; the relying party is the panel's own name (never an IP address) and
  only its origin is accepted, so a look-alike site cannot use them. Challenges are random, used once
  and expire in five minutes; a passkey whose signature counter goes back (perhaps copied) is refused
  and reported. A passkey counts as both factors, so it signs in without a TOTP code, and the
  guessing limits above never hold it back. Each account has a random 32-byte WebAuthn user handle,
  never its id; adding and removing passkeys needs a browser session and is notified.
- Cloudflare Turnstile (optional, Settings › Security): every sign-in to the panel and to users'
  pages must carry a token Cloudflare accepts for this site's hostname; the panel checks it with
  Cloudflare (HTTPS, no redirects followed) before it looks at the password. The secret is never
  returned. Turnstile is turned on only after Cloudflare accepted a token made on the settings page
  with the same keys, so wrong keys cannot lock the sign-in; `MERIDIAN_NO_TURNSTILE=1` on the host
  turns it off. Cloudflare's script is allowed by the page's Content-Security-Policy only while
  Turnstile is on (and for the supervisor's own session, to set it up).
- Maintenance mode signs every user out and keeps them out (503) until it ends; the supervisor's
  sessions and every proxy, subscription and agent keep working.
- The console (a root shell on a server in the browser) opens only from the supervisor's signed-in
  browser session, after the password is confirmed (again after 30 minutes); never with an API
  token, MCP, plugins or a user's session. Its WebSocket must come from the panel's own page
  (Origin) and carry a one-time ticket bound to that browser session; the agent dials back signed
  with its server's key for that session only; the panel passes on only typing and window sizes.
  Nothing typed or shown is stored; opening is notified. A server can refuse consoles altogether
  (`/etc/meridian-agent/no-console`).
- Two-factor sign-in for the supervisor (TOTP, RFC 6238); each code is accepted once.
- Sessions: random 256-bit tokens stored as SHA-256, 30 days sliding. Changing a password signs out
  every other session; Settings › Security lists sessions and signs others out. The supervisor can
  sign a user out everywhere, and setting a user's password ends their sessions.
- Every state-changing request with a session cookie needs the `X-Meridian: 1` header, which other
  sites cannot send (CSRF). Responses carry a strict Content-Security-Policy, `X-Frame-Options: DENY`,
  `nosniff`, `Referrer-Policy`, `Cross-Origin-Opener-Policy` and `Permissions-Policy`.
- The first-run password exists only in a note readable by the panel's user (shown by the
  installer) - never in the service log - and the note is deleted on the supervisor's first sign-in.
- Generated user passwords are shown once and stored only as bcrypt hashes.

### The status page and the users' pages

- The server dashboard's data (`/api/status`) is built from an explicit list of fields - name, city,
  public IP addresses, up/down, availability, throughput, load, memory, disk, connections, system,
  bandwidth and traffic, the day the paid period ends, outages - and never contains users, ports,
  protocols, keys or prices (a test checks this).
- Visitors get it only while **Show the servers to everyone** is on (the default); otherwise they
  see a sign-in form and nothing else. The supervisor (session or API token) always gets it.
- The addresses are in it only while **Show IP addresses** is on (off by default) - then for
  everyone who opens the page, otherwise for nobody, the supervisor included (a test checks both
  ways). The panel itself always shows them to the supervisor.
- A user's page shows only their own link, usage, devices and servers.
- On the status page's own domain the panel and its API answer `404`; a supervisor session made
  there opens nothing else.

### Certificates in subscriptions

- Domain certificates are verified by name. Self-signed certificates are pinned: by SHA-256 in
  Clash/mihomo, Surge and Quantumult X, by the certificate itself in sing-box. Share links cannot
  make apps pin, so self-signed TLS and Hysteria2 protocols are left out of share-link formats.
- No format ever contains `insecure`, `allowInsecure` or `skip-cert-verify`; a test renders every
  format with self-signed protocols and fails if one does.

### Access by country

- The site rule is checked on every request by the panel itself, for the areas it covers (panel,
  users' pages, status page, subscription links). The panel refuses to save a rule that would refuse
  the person saving it; `meridian reset-site-access` turns it off from the host if you are locked out
  later.
- The servers' rule is enforced by nftables on the service ports only, in both directions, so open
  connections stop at once; SSH, private networks and listed exceptions are never filtered. The
  country lists reach agents signed and sealed, identified by their SHA-256.
- Client addresses come from the TCP connection, or from forwarding headers only when the
  connection comes from a configured trusted proxy.

### API tokens and MCP

- Tokens are shown once and stored as SHA-256. They expire (or not, if you choose), can be read-only,
  and can never change passwords, two-factor settings, sessions, tokens or the site rule. Besides the
  browser, only the panel's host can create one (`meridian token`, run as the panel's user - whoever
  can do that can read the database anyway); host commands refuse to run as root on the panel's
  database, so they never leave files the panel cannot open. Failed token
  attempts are rate limited. A request with a token is never also authenticated by a cookie.
- `GET /api/servers/{id}` hides the agent install command (which contains the server's secret) from
  read-only tokens.
- The MCP endpoint requires a token, refuses foreign browser origins (DNS rebinding), offers
  read-only tokens only read tools, and every tool that disconnects people demands `confirm=true`.

### External nodes and traffic rules

- Importing proxies from elsewhere (share links, a subscription's content, Clash files) refuses every
  node that turns certificate checks off (`allowInsecure`, `insecure`, `skip-cert-verify`) or sends
  traffic unencrypted (VLESS or Trojan without TLS or REALITY, SOCKS5, plain HTTP proxies,
  Shadowsocks `none` or its broken stream ciphers), with the reason. A Hysteria2 node with a
  self-signed certificate gets in only with that certificate's SHA-256, which the servers then pin.
  Nothing in a server's configuration ever turns a certificate check off.
- A node's address cannot be a loopback, link-local (cloud metadata) or multicast address, or a name
  that only means something inside a network (`.local`, `.internal`, `.lan`, ...). Private addresses
  are allowed, as for port forwards: a node can sit in the provider's private network.
- A subscription address is fetched once, over HTTPS only (a redirect to plain HTTP is refused, two
  redirects at most), without any proxy from the environment, and only from public addresses: the
  panel resolves the name itself and checks every address it connects to, so a name that points into
  the panel's own network - or changes its answer between two lookups - gets nowhere. The answer is
  capped at 4 MB (pasted text too), an import at 2000 nodes, an account at 2000 nodes; the reasons an
  answer lists are capped as well.
- A node's passwords and keys are stored like the panel's other secrets and never returned: the API,
  the UI and MCP show its name, kind, address and port only. The servers that use it get them in
  their configuration (and `GET /api/servers/{id}/config` needs a full-access token).
- A server reaches a protocol its traffic rules send traffic to with its own identity there
  (`r<server id>`), derived from its secret with HMAC - never as one of the exit's users - and it
  changes when the server's token is rotated, once the agent has the new token.
- Traffic whose exit cannot be used - a node or protocol turned off or removed, a chain that would be
  too long or come back - is blocked, never sent out directly instead, so nobody shows up somewhere
  they did not choose; the panel says where and why.
- Rules are checked where they enter: site list names, domains, regular expressions (compiled, at
  most 512 characters), country codes, addresses and ports; at most 2000 entries per rule and 10000
  in all, 500 rules, 100 load balancers of 100 members. Private addresses stay blocked before every
  rule.
- **Check** on a node: the agent checks the address again, refuses loopback, link-local and multicast
  addresses on the address it actually connects to, verifies the certificate of a TLS or REALITY
  node against its server name with the system's authorities (never skipping the check), and sends
  nothing through the node.

### Plugins

A plugin that is on is trusted with what it asks for - a server plugin's program runs on the panel's
host as the panel's user and can read the database. What Meridian guarantees is that nothing runs
without the supervisor's informed yes, and that a plugin cannot stall the panel or grant itself more:

- Installing, updating, removing and turning plugins on or off need a signed-in browser session with
  the CSRF header, or the panel's host (`meridian plugins`); API tokens, MCP and plugins' own API
  calls are refused. A new plugin is off; turning it on records exactly what it asks for, and a new
  version that asks for more is turned off until the supervisor agrees again.
- Uploads are checked before anything is written: size and file-count limits (also while
  unpacking), no absolute paths, `..`, backslashes, links or special files, a strict manifest; files
  are written through `os.Root` into a fresh folder, readable only by the panel's user.
- A program gets a minimal environment without the panel's secrets, its own process group, time
  limits on every call and bounded queues; its calls to the API pass the same checks as a token of
  its scope and never reach the plugins' management or what needs a browser. The contract, agent
  settings, cores and actions of a server's configuration stay the panel's whatever a filter
  answers, and agents check every configuration as always.
- Only the style sheets and scripts a manifest names are served; the panel script only to the
  signed-in supervisor. Answers of a plugin's API are sent sandboxed (they never run as a page of the
  panel), its public pages run sandboxed in an origin of their own, and requests reach plugins
  without cookies or credentials.

### Panel <-> agent

- Each server has its own 256-bit secret. Signing and encryption keys are derived from it with HKDF.
- Every agent request is signed with HMAC-SHA256 over method, path, time, a random nonce and the body
  hash; the panel refuses requests older than five minutes and any nonce it has seen.
- Every reply is encrypted with AES-256-GCM and bound to the path and the request's nonce, so a
  recorded reply cannot be replayed to a later request. This holds over plain HTTP.
- Agents verify what they run:
  - the install command checks the installer's SHA-256, and the installer checks the agent binary's;
  - agent upgrades carry the binary's SHA-256 inside the signed state;
  - Xray, Hysteria and realm downloads are checked against SHA-256 digests the panel took from
    GitHub over HTTPS and delivered in the signed state (or that the agent fetched from GitHub itself).
    A checksum is never taken from the download mirror, and the mirror verifies files before caching them.
- The agent validates what the panel sends before it reaches nftables, file paths or the init
  system (port ranges, addresses, interface names, domain names, version numbers).

### Servers

- Shared certificates' private keys are stored like the panel's other secrets and are never
  returned by the API (only the certificate chain is). Configuration code may hold keys too: read-only
  API tokens see neither it nor the merged configuration (`GET /api/servers/{id}/config` needs a
  full-access token). The agent checks that an inbound refers only to the shared certificate it names.
- The agent runs as root because it manages nftables, WireGuard and the services. Under systemd,
  cores run in their own units with a capability bounding set, `NoNewPrivileges`, `ProtectSystem`,
  `ProtectHome`; realm runs as an unprivileged dynamic user.
- Under OpenRC (Alpine Linux) there is no equivalent sandbox: Xray and Hysteria2 run as root with
  `no_new_privs`, as most panels run them everywhere; realm runs as `nobody` holding only
  `CAP_NET_BIND_SERVICE`. Without nftables (optional on Alpine) the loopback guard below is absent:
  the Xray API and the Hysteria2 hooks still listen on 127.0.0.1 only, so only users logged in on
  the server itself could reach them - install nftables on servers other people can log in to.
- The Xray API and the Hysteria auth and stats endpoints listen on loopback only, and an nftables
  owner match lets only root connect to them.
- The WireGuard DNS resolver answers only WireGuard clients.
- Connection logs (client IPs, destinations) are readable by root only and rotated.
- IP blocks apply only to Meridian's own ports, so a mistake can never lock you out of SSH.
- Let's Encrypt: the agent opens port 80 only while a certificate is being issued, and certificate
  names are checked as plain domain names before they reach the file system.
- Behind NAT the agent learns its public IPv4 address from DB-IP (`api.db-ip.com`) or Cloudflare
  (`1.1.1.1/cdn-cgi/trace`), over HTTPS only and with redirects to plain HTTP refused: the address
  ends up in users' links, so a forged answer must not be possible. Only a public address is
  accepted.
- Importing: a scan only reads configuration files and the process list and reports what it found;
  a service is stopped only by an explicit **Take over** (confirmed in the panel, `confirm=true` over
  MCP).

### Input

Names lose control characters; addresses must be valid domains or IPs; REALITY camouflage targets
must be public sites (or a loopback port of your own site, sensitive ports refused); port forwards
cannot target loopback, link-local or cloud-metadata addresses; blocks cannot be wider than /8
(IPv4) or /32 (IPv6); versions must be plain numbers; the public URL must be a bare
`http(s)://host[:port]`; protocol settings must form a combination that works (anything else is
refused). Subscription generators escape every line-based format (Surge, Quantumult X, WireGuard
files, YAML comments) on top of that. The status page and the users' pages insert server data only
as text, never as HTML. An uploaded logo is identified by its content, not its claimed type: PNG and
JPEG must decode (at most 2048 x 2048), WebP must be WebP, and an SVG is rebuilt from an allowlist of
drawing elements and attributes - anything active or reaching outside (scripts, event handlers,
foreign content, links, outside images, fonts or styles, `javascript:`/`data:` URLs, DOCTYPEs) is
refused with the reason. Logos are always served with `nosniff` and a sandboxing
Content-Security-Policy, so even opening one directly cannot run anything in the panel's origin. What agents report as connected is parsed before it is stored or shown:
addresses must be valid IPs (IPv4-mapped forms are unmapped), and users the panel does not have are
ignored.

### Supply chain and code

- Few dependencies; `make check` runs the race-enabled tests, `go vet`, staticcheck,
  govulncheck (Go and Linux builds) and `npm audit`. At the time of writing they report nothing that
  affects Meridian. (govulncheck lists GO-2026-5932, a module-level note that the deprecated
  `golang.org/x/crypto/openpgp` package is unmaintained; Meridian never imports it, and there is no
  fixed version.)
- Release archives come with `SHA256SUMS`.

## What stays your job

- **Use HTTPS for the panel.** Over plain HTTP the agent channel is still safe, but browser sessions,
  users' passwords and the install commands you copy are not.
- **Protect the supervisor account** with a passkey, or a long password and two-factor sign-in.
  Whoever controls the panel controls every server - by design.
- **Treat backups as secrets.** The database holds every key and credential; `meridian backup`
  writes files only the owner can read.
- **Keep cores current.** Meridian pins Xray, Hysteria and realm versions; set newer ones in
  Settings › Cores and upgrade servers from their pages.
- **Mind the law on connection logs.** Client IPs and destinations are personal data in many places.
  Set a retention period you can justify, or turn logging off in Settings.
- **Keep the panel host patched** and expose only ports 80/443 (or your proxy's).
- **Install only plugins you trust**, and read what turning one on lets it do: a plugin is code you
  let into the panel, your browser and possibly your visitors' browsers.

## Accepted limitations

- Rate limits and the agent replay cache are in memory: a panel restart resets them. The five-minute
  time window still bounds replays.
- TOTP secrets are stored unencrypted in the database, which is the trust anchor anyway.
- The panel trusts its agents' reports for numbers about their own server; a compromised server can
  misreport its own traffic, but cannot touch other servers.
- Country lists are as accurate as DB-IP's free database; addresses move between countries, so
  country rules reduce abuse rather than guarantee where someone is.
