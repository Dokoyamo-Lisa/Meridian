# API

Everything the web panel does is available as JSON over HTTP. The complete, always-current
reference is generated from the code:

- in the panel: **Settings › API & MCP › REST API** (searchable, every endpoint with its fields);
- as OpenAPI 3.1: `GET /api/openapi.json` on your panel, or [openapi.json](openapi.json) here
  (`make docs` regenerates it; a test fails if a route is undocumented).

## Authentication

**API tokens** are for scripts and assistants. Create one in **Settings › API & MCP** - or on the
panel's host with `sudo -u meridian meridian token --name my-script --scope full --days 30` - and
send it as a bearer token:

```bash
curl -H "Authorization: Bearer mrd_..." https://panel.example.com/api/servers
```

- **Read only** tokens may only `GET`. **Full access** tokens may do everything the supervisor can,
  except change the password, two-factor settings, sessions, tokens or the site's country rule -
  those need a signed-in browser.
- A token is shown once. Revoke it in the same place; expired or revoked tokens answer `401`.
- On a machine other people use, keep the token off the command line (any user can list running
  commands): pass the header through a file descriptor, as `skills/meridian-deploy/scripts/api.sh`
  does - `curl -K <(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN") ...`. The examples
  below use `-H` for brevity.

**Browser sessions** (the web UI) use an HttpOnly cookie. Every non-`GET` request with a session must
carry `X-Meridian: 1`, which protects against cross-site requests. Sign in with
`POST /api/login {"username", "password", "code"}`; the answer says `"kind": "admin"` for the
supervisor or `"kind": "user"` for a user, or `"totp_required": true` when a two-factor code is
needed.

**Users** sign in with the same `POST /api/login` and get their own cookie, which only works for
`/api/portal/*` (their page) and `/api/status*`. A user session can never reach the panel's API,
and the supervisor's session is not a user session.

## Conventions

- Bodies are JSON (`Content-Type: application/json`). Times are Unix seconds. Traffic is in bytes,
  rates in bytes per second.
- `PATCH` changes only the fields you send.
- Errors are `{"error": "plain English"}` with `400` (invalid input - the message says what to do),
  `401` (not signed in / bad token), `403` (not allowed, or not from your country), `404` (not
  found), `409` (conflict, e.g. a port in use), `429` (slow down).
- Changes to servers, protocols, users, country rules and blocks reach the servers within seconds.
  Nothing restarts a core unless the endpoint says so, and nothing pauses a user unless you ask.

## Examples

Create ten users with a 100 GB monthly quota, valid until the end of the year. Each comes back
once with a generated password:

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"team","count":10,"quota":107374182400,"reset_day":1,"expires_at":1798761599}' \
  https://panel.example.com/api/users
```

Who is online right now:

```bash
curl -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/live
```

IPs that used user 12's link in the last 30 days:

```bash
curl -H "Authorization: Bearer $TOKEN" "https://panel.example.com/api/users/12/ips?days=30"
```

Pause them (disconnects every device using the link now):

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/users/12/pause
```

Add a server with VLESS · REALITY and Hysteria2, then print the install command:

```bash
curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"Tokyo 2","protocols":["vless","hysteria2"]}' \
  https://panel.example.com/api/servers | jq -r .install
```

Check a protocol before adding it - whether it can be saved, and which apps can use it:

```bash
curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"kind":"trojan","settings":{"transport":"ws","security":"tls","cert_mode":"acme","sni":"tj.example.com"}}' \
  https://panel.example.com/api/protocols/check
```

Add it to server 3:

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"kind":"trojan","settings":{"transport":"ws","security":"tls","cert_mode":"acme","sni":"tj.example.com","path":"/ws"}}' \
  https://panel.example.com/api/servers/3/nodes
```

Block two countries on every server (open connections from them stop at once):

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"mode":"block","countries":["KP","IR"],"exceptions":[]}' \
  https://panel.example.com/api/access/servers
```

Make the status page the site's front page (visitors see every server and sign in from the top right; users then get their own page). Add `| .status_ips = true` to show the servers' IP addresses there too (to everyone), or `| .status_public = false` to give visitors only the sign-in:

```bash
curl -s -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/settings \
  | jq '.status_page = "home"' \
  | curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d @- \
      https://panel.example.com/api/settings
```

Name the panel, upload its logo and choose how the logo moves. `PUT /api/settings` changes only the
fields you send:

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"site_title":"Acme Net","logo_animation":"rise"}' https://panel.example.com/api/settings
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: image/svg+xml" \
  --data-binary @logo.svg https://panel.example.com/api/settings/logo
```

Add a port forward:

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"target":"203.0.113.7:443","listen_port":30443,"network":"tcp+udp"}' \
  https://panel.example.com/api/servers/3/forwards
```

## Endpoint overview

| Area | Endpoints |
| --- | --- |
| Session | `POST /api/login`, `POST /api/logout`, `GET /api/me`, `GET /api/meta`, password, two-factor, sessions |
| API tokens | `GET/POST /api/tokens`, `DELETE /api/tokens/{id}` (browser session only) |
| Monitoring | `GET /api/overview`, `/api/live`, `/api/ips`, `/api/dests`, `/api/events` |
| Servers | `GET/POST /api/servers`, `GET/PATCH/DELETE /api/servers/{id}`, `rotate-token`, `actions`, `metrics`, `GET /api/actions/{id}` |
| Protocols | `GET /api/protocols` (what can be built, the apps), `POST /api/protocols/check`, `POST /api/servers/{id}/nodes`, `PATCH/DELETE /api/nodes/{id}`, `regenerate`, `test-target` |
| Import | `POST/GET /api/servers/{id}/scan`, `POST /api/servers/{id}/import` |
| Forwards | `POST /api/servers/{id}/forwards`, `PATCH/DELETE /api/forwards/{id}` |
| Users | `GET/POST /api/users`, `GET/PATCH/DELETE /api/users/{id}`, `POST /api/users/{id}/{pause,resume,rotate-link,reset-keys,reset-usage,sign-out,new-password}`, `ips`, `dests`, `traffic`, `preview` |
| A user's own page | `GET /api/portal/me`, `POST /api/portal/password`, `POST /api/portal/logout` (user session) |
| Status page | `GET /api/status`, `GET /api/status/live` (the live dashboard of every server - public while the status page shows the servers to everyone, otherwise the supervisor's), `GET /api/places` |
| Country rules | `GET /api/access`, `PUT /api/access/servers`, `PUT /api/access/site` (browser session only) |
| IP blocks | `GET/POST /api/blocks`, `DELETE /api/blocks/{id}` |
| Settings | `GET/PUT /api/settings` (including the status page), `GET /api/openapi.json` |
| Subscription links | `GET /s/{token}` (`?client=clash|stash|singbox|shadowrocket|surge|quanx|base64|hiddify|loon|uri|html`), `GET /s/{token}/wg/{node}` |
| MCP | `POST /mcp` - see [mcp.md](mcp.md) |

The agent endpoints under `/agent/v1/` are internal and signed per server; see
[architecture.md](architecture.md#panel--agent-channel).
