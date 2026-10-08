---
name: meridian-dev
description: Step-by-step procedures for changing the Meridian codebase - adding API endpoints, MCP tools, UI pages, protocols or agent features, changing the panel/agent contract, testing end to end in the local VM, and cutting a release. Use when implementing or reviewing changes in this repository.
---

# Developing Meridian

Read `CLAUDE.md` (rules) and `docs/architecture.md` (how it fits together) before changing code.

## Add or change an API endpoint

1. Write the handler in the matching `internal/panel/api_*.go`:
   `func (p *Panel) apiThing(w http.ResponseWriter, r *http.Request, a *Account) error`.
   - Load objects with `ownServer` / `ownSub` / `ownNode` (a user is a `Sub` in code).
   - Validate input with the helpers in `validate.go` (`cleanName`, `cleanNote`, `normHost`,
     `forwardTarget`, `realityTarget`, `normalizeBlock`). Return `errStatus(400, "plain English")`.
   - Record an event for anything a person would want in the timeline (`p.event(...)`).
   - Call `p.touchServers(ids...)` / `p.touchAccount(id)` when servers must get a new state.
2. Register it in `routes.go` with `authed`, or `sessionOnly` for anything a token must not do
   (passwords, tokens, the site's country rule). Endpoints for a signed-in user go through `portal`
   under `/api/portal/`; public ones (status page) check the settings themselves.
3. Document it in `apiOps` (`apidoc.go`). Use named request/response structs with `doc:` tags;
   add a friendly schema name to `schemaNames` if the Go name is lower-case.
4. Add a test - at least the happy path, bad input, and that a user session and a read-only token
   cannot reach it (`api_test.go` has the harness; `status_test.go` shows a stand-in web build).
5. `make docs` to refresh `docs/openapi.json`.
6. If an assistant would use it, add an MCP tool in `mcp.go` (`mcpTools`): read tools need no
   flags; `Write: true` for changes; `Destructive: true` for anything that disconnects people or
   cannot be undone (the framework then demands `confirm=true`).
7. UI: types in `web/src/api.ts`, page code in `web/src/pages/`. Confirm disruptive actions with
   `ask({...})` and say exactly who gets disconnected. The status page and users' page live in
   `web/src/status/` (plain DOM, all text through `textContent`).

## Change what servers run

1. Desired state is built in `compile.go` / `nodes.go` from the database.
2. If the shape of `internal/proto` changes incompatibly, bump `proto.Version`; agents that are too
   old then accept only an upgrade.
3. In the agent engine, apply the change live. If it truly needs a restart, report it in
   `Applied.Pending` instead of restarting; the panel shows "restart needed" and waits for a click.
4. Validate everything from the state again on the agent before it reaches nftables, file paths,
   systemd units or command arguments.
5. `GOOS=linux go vet ./...` and the e2e kit below.

## Add a protocol or a protocol option

1. `internal/panel/protocols.go`: `kindList`, the settings struct and defaults, `check` (refuse every
   combination that would not work - with a reason that says what to do), the Xray rendering
   (`xrayInbound`) and `endpoint` for subscriptions; Hysteria2 / WireGuard live in `nodes.go`.
2. `internal/subgen`: every format that can express it; return a reason from the format's
   support check where it cannot - the app list in the UI comes from there.
3. `protocols_test.go` (`TestEveryAcceptedProtocolWorks`) must keep passing: every accepted
   combination renders a server config and works in at least one app.
4. Import: map it in `import.go` if other panels can produce it.
5. UI: the protocol form in `web/src/pages/Protocols.tsx`; docs: `README.md`, `docs/getting-started.md`.

## Test end to end (local VM only)

See `test/e2e/README.md`. Short version:

```bash
make agents && go build -o /tmp/meridian ./cmd/meridian
# run the panel on the Mac, install the agent in the Lima VM with the command from the server page
limactl copy -r test/e2e <vm>:/tmp/e2e
limactl shell <vm> -- sudo bash /tmp/e2e/setup-client.sh     # once per VM boot
limactl shell <vm> -- sudo bash /tmp/e2e/quick.sh label      # 204 per protocol = working
```

Check that live changes keep the Xray PID unchanged (`systemctl show -p MainPID meridian-xray`).
Never test against production servers, users or links.

## Before calling it done

```bash
make check     # race tests, vet (darwin + linux), staticcheck, govulncheck, npm audit
```

All must be clean. Then update `docs/STATUS.md` and, for user-visible changes, `CHANGELOG.md`.

## Release

1. Bump `VERSION`, add a `CHANGELOG.md` entry.
2. `make check && make release` - produces `dist/release/*.tar.gz`, desktop CLI builds,
   `SHA256SUMS` and `SHA256SUMS.sig`. The signature needs the release key
   (`~/.config/meridian/release-signing.key`, or `MERIDIAN_SIGNING_KEY`): panels update themselves
   only to releases signed with it (`internal/update/keys.go` holds its public half). Upload all of
   these files to the GitHub release, `SHA256SUMS.sig` included.
3. Upgrade path to verify: `install-panel.sh --upgrade` on a test panel, then **Upgrade agent** on a
   test server; traffic must keep flowing throughout.
