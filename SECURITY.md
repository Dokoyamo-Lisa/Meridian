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
| The supervisor account | password guessing, phishing, session theft | bcrypt (cost 12), rate limits, TOTP two-factor with replay protection, HttpOnly + SameSite cookies, CSRF header, sign-out everywhere |
| Users' sign-ins | guessing, a user reaching the panel or another user's data | separate cookie and session table, SameSite=Strict, rate limits, the panel's API refuses user sessions and the user API refuses everything else; covered by tests |
| Servers (root via the agent) | a forged or replayed panel message, tampered downloads | per-server 256-bit secret, signed requests, sealed replies bound to each request, checksum-pinned installer, agent and cores |
| Subscription credentials | link sharing, copied configs | 144-bit link tokens, new link / new credentials actions, per-IP online limits and alerts |
| Connection logs (IPs, destinations) | disclosure | panel DB 0600 under a dedicated user, root-only log files on servers, configurable retention |
| The status page | revealing users, keys or more than you chose to show | the server dashboard is built from a fixed set of fields that never includes users, ports, protocols, keys or prices; visitors get it only while the page shows the servers to everyone, otherwise only a sign-in; IP addresses only while the page is set to show them (off by default), and then to everyone alike; users only their own page; its own domain never serves the panel |
| Client certificate checks | a man in the middle on TLS / Hysteria2 | real certificates are verified by name; self-signed ones are pinned (SHA-256 / the certificate itself) and left out of formats that cannot pin - no format ever turns checks off (a test renders every format to make sure) |
| Server hosts | the panel turning a server into an open proxy to its own network | REALITY targets must be public (or your own site on loopback, with sensitive ports refused), forwards cannot reach loopback / link-local / cloud metadata |
| Access by country | being locked out; abuse from some regions | the panel refuses site rules that would refuse their author, `meridian reset-site-access` to recover; server rules never touch SSH or the agent's own connection |
| AI assistants (MCP) | an assistant taking disruptive action unasked | token scopes, `confirm=true` on every disruptive tool, origin check, no MCP access to the site rule |

## How it is protected

### Panel and browser

- Serve the panel over HTTPS: `--domain` gets a Let's Encrypt certificate automatically (also for
  the status page's own domain, if you set one). With HTTPS the panel sends HSTS and marks session
  cookies `Secure`. The UI warns while it is on plain HTTP.
- Passwords: 10-72 bytes, bcrypt cost 12. Failed sign-ins are rate limited per IP and per name and
  take the same time whether or not the name exists.
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
- **Protect the supervisor account** with a long password and two-factor sign-in. Whoever controls
  the panel controls every server - by design.
- **Treat backups as secrets.** The database holds every key and credential; `meridian backup`
  writes files only the owner can read.
- **Keep cores current.** Meridian pins Xray, Hysteria and realm versions; set newer ones in
  Settings › Cores and upgrade servers from their pages.
- **Mind the law on connection logs.** Client IPs and destinations are personal data in many places.
  Set a retention period you can justify, or turn logging off in Settings.
- **Keep the panel host patched** and expose only ports 80/443 (or your proxy's).

## Accepted limitations

- Rate limits and the agent replay cache are in memory: a panel restart resets them. The five-minute
  time window still bounds replays.
- TOTP secrets are stored unencrypted in the database, which is the trust anchor anyway.
- The panel trusts its agents' reports for numbers about their own server; a compromised server can
  misreport its own traffic, but cannot touch other servers.
- Country lists are as accurate as DB-IP's free database; addresses move between countries, so
  country rules reduce abuse rather than guarantee where someone is.
