# Meridian - working on this repository

Go panel + Go agent + Preact UI for running proxy/VPN servers (Xray: VLESS, VMess, Trojan,
Shadowsocks, SOCKS5, HTTP over every transport with REALITY/TLS; Hysteria2; WireGuard; nftables/realm
forwards). One supervisor signs in to the panel; users get links and their own page (`/me`); a public
status page and country rules are built in. Read `docs/architecture.md` first; the `meridian-dev`
skill has step-by-step procedures.

## Product rules (do not break these)

- **Nothing pauses on its own.** Quotas, expiry and IP limits raise alerts; only a person pauses.
- **No needless restarts.** Apply changes through live APIs (Xray HandlerService, Hysteria HTTP auth,
  wgctrl, atomic nftables). Anything that would restart a core becomes "pending restart" and waits
  for an explicit click. Agent restarts and upgrades must never touch traffic.
- **English only** in UI, messages, subscription group names and docs. Plain words, no jargon in
  user-facing text; error messages say what to do.
- **Disruptive actions ask first**: UI confirms with what will happen; MCP tools need `confirm=true`.
- **Security bar is "zero known vulnerabilities"**: validate input where it enters (panel) and again
  where it is used (agent: nft, paths, systemd); never log secrets; every route checks ownership
  (`ownServer`/`ownSub`/`ownNode`); user sessions reach only `/api/portal/*`; the dashboard data
  (`/api/status`) is the supervisor's and never contains users, addresses, ports or protocols; no
  subscription format ever turns certificate checks off; keep `make check` clean.
- **Only working protocol combinations**: `xraySettings.check` refuses anything that would not work
  on the server or in the listed apps, and app support is computed by the subscription renderers.
- **External services: reputable providers over HTTPS only** (GitHub, Let's Encrypt, DB-IP,
  Cloudflare). Never Chinese services, mirrors or CDNs, and never plain-HTTP endpoints.

## Commands

```bash
make                 # UI + panel + agents
make test            # go test -race ./... + UI typecheck   (MERIDIAN_NO_GEO_DOWNLOAD=1 is set)
make check           # + vet, staticcheck, govulncheck (Go + Linux), npm audit
make docs            # regenerate docs/openapi.json after API changes
make release         # dist/release tarballs + desktop CLI builds + SHA256SUMS(.sig - needs the release key)
```

UI only: `cd web && npm run dev` (proxies /api to 127.0.0.1:18080).

## Conventions

- Handlers: `func (p *Panel) apiX(w, r, a *Account) error`, registered in `routes.go` through
  `authed` / `sessionOnly` (browser only: passwords, tokens, the site rule); users' handlers are
  `func (p *Panel) apiPortalX(w, r, s *Sub) error` through `portal`. Every new route must be added
  to `apiOps` in `apidoc.go` (a test fails otherwise) with request/response types that carry
  `doc:"..."` tags.
- Load objects with `ownServer` / `ownSub` / `ownNode`. In code a user is a `Sub` (table `subs`);
  in the API, UI and docs it is a "user".
- Protocols: `internal/panel/protocols.go` (settings, `check`, rendering for Xray, credentials);
  `protocols_test.go` proves every accepted combination renders and works in at least one app.
- Clean names with `cleanName`, notes with `cleanNote`, hosts with `normHost`; see `validate.go`.
- The panel <-> agent contract is `internal/proto`. Any incompatible change bumps `proto.Version`.
- Agent code must build and vet with `GOOS=linux`.
- Tests: `internal/panel/api_test.go` has an httptest harness (`newHarness`, `browser()`, `bearer()`).
- Generated files: `docs/openapi.json` (`make docs`), `web/dist` (`make web`). Do not edit them.
- The UI has two pages: the panel (`web/index.html`, `web/src/main.tsx`) and the status page / users'
  page (`web/status/index.html`, `web/src/status/`, plain DOM, and `web/public/status/globe.js`).
- The logo is the operator's (`internal/panel/brand.go`, `web/src/mark.ts`): draw it with `LogoMark`
  (panel) or `markEl` (status page) and animate it with `animClass` / `openInto` - never hard-code the
  umbrella. Loading screens keep their mark between `<!--logo-->` markers (the panel swaps it).
- Deployment is documented for AI agents in `skills/meridian-deploy/` (and `AGENTS.md`): when the
  installer, CLI or API change, update that skill and its scripts too.

## Local end-to-end testing

`test/e2e/README.md`: a Lima VM runs the agent; the panel runs on the Mac; a network namespace in the
VM runs sing-box clients for every protocol. Never point tests at production servers.
