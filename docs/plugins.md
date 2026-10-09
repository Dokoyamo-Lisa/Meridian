# Plugins

A plugin changes Meridian the way its operator wants: how the panel, the status page and users'
pages look, what they show and do, and - through a program that runs beside the panel - what
servers run, what users' apps receive, what notifications say, and which API routes, pages, MCP
tools and timers the panel has. Two examples to start from are in
[`examples/plugins`](../examples/plugins): a theme and a server plugin written in Go.

A plugin can do a lot of damage, so Meridian is careful about who installs it and what it may do:

- Plugins are installed, updated, turned on and off and removed **only from a signed-in browser**
  (Settings › Plugins) - never with an API token or through MCP - or on the panel's host with
  `meridian plugins`.
- A new plugin arrives **turned off**. Turning it on lists, in plain words, everything it asks for
  and what that lets it do, and records that you agreed. A new version that asks for more is turned
  off until you agree to that too.
- If a plugin keeps the panel from working, `meridian plugins disable ID` or starting the panel with
  `MERIDIAN_NO_PLUGINS=1` brings it back ([Safety and recovery](#safety-and-recovery)).

Only install plugins from people you trust: a plugin that is on can do whatever it asks for.

## What a plugin can change

| Part | What it changes | Asks for |
| --- | --- | --- |
| Panel styles | how every page of the panel looks, the sign-in page included | `panel.css` |
| Panel script | anything in the panel: its own pages with an entry in the top bar, entries in the account menu, changes to any page; it can call the whole API as you | `panel.js` |
| Status page styles | how the status page and users' own pages (`/me`) look | `status_page.css` |
| Status page script | anything on the status page and users' pages: decorate server cards, show more, react to the data | `status_page.js` |
| A program (server plugin) | runs on the panel's host and, with the permissions below, does the rest | `server` |
| - `events` | hears every event the panel records in its timeline | |
| - `api:read`, `api:write` | reads (or also changes) everything through the panel's API, as the supervisor | |
| - `filter:compile` | changes each server's configuration before the server gets it (Xray, Hysteria2, WireGuard, forwards, blocks...) | |
| - `filter:subscription` | changes what users' apps receive for their subscription link | |
| - `filter:status` | changes the status page's data | |
| - `filter:notify` | changes notifications, or holds them back | |
| - `routes` | its own API under `/api/plugins/<id>/` and, with `"public_pages": true`, public pages under `/p/<id>/` | |
| - `mcp` | its own tools for AI assistants | |
| - `schedule` | is called on timers | |
| - `notify` | sends you notifications through your Telegram chat and webhook | |

## A first plugin

A theme is two files. `plugin.json`:

```json
{
  "id": "night-blue",
  "name": "Night blue",
  "version": "1.0.0",
  "panel": { "css": "theme.css" }
}
```

and `theme.css`:

```css
:root, :root[data-theme] { --accent: #6aa8ff; --accent-soft: rgba(106, 168, 255, .14); }
```

Zip them (`zip night-blue.zip plugin.json theme.css`), upload the zip in **Settings › Plugins**,
turn it on and reload the panel. The panel and the status page draw everything from the CSS
variables at the top of `web/src/app.css` and `web/src/status/status.css`; a plugin's style sheet
loads after them, so its rules win.

## The manifest: plugin.json

`plugin.json` is at the top of the zip (or of the one folder a zipped folder holds). Unknown fields
are refused, so a typo is pointed out instead of ignored.

| Field | |
| --- | --- |
| `id` | 2 to 32 lowercase letters, digits and dashes, starting with a letter: `night-blue`. It names the plugin's folder and its paths (`/api/plugins/<id>/`, `/p/<id>/`, MCP tools `<id>__<tool>`); a new version must keep it. |
| `name` | what people see, up to 64 characters |
| `version` | up to 32 letters, digits, dots, dashes and pluses: `1.2.0`, `2.0.0-beta+3` |
| `description`, `author` | optional texts (up to 1000 and 100 characters) |
| `homepage` | optional, an `https://` address |
| `panel` | `{"css": "...", "js": "..."}`: a style sheet and/or a script for the panel (paths inside the zip, `.css` and `.js`, at most 2 MB each) |
| `status_page` | the same, for the status page and users' pages |
| `server` | `{"command": "bin/my-plugin", "args": ["--flag"], "public_pages": false}`: the program the panel runs (a path inside the zip), up to 32 arguments, and whether it serves public pages |
| `permissions` | what the program may do: `events`, `api:read`, `api:write`, `filter:compile`, `filter:subscription`, `filter:status`, `filter:notify`, `routes`, `mcp`, `schedule`, `notify`. Permissions need a `server` part; `public_pages` needs `routes`. |

A plugin must bring at least one of `panel`, `status_page` and `server`.

The zip may hold other files the plugin needs (its program's data, templates...), but nothing
except the style sheets and scripts named above is ever served to browsers. Limits: 20 MB zipped,
64 MB unpacked, 32 MB per file, 1,000 files. Every path must stay inside the plugin (no absolute
paths, `..` or backslashes); links, devices and other special files are refused; what macOS adds
(`__MACOSX/`, `.DS_Store`) is skipped. Files are unpacked readable only by the panel's user; the
program named in `server.command` is the only one that can run.

## Permissions and their consequences

Turning a plugin on shows these sentences for what it asks for. Read them: they are what can go
wrong.

| Asks for | What it lets the plugin do |
| --- | --- |
| `server` | A server plugin runs as a program on the panel's host with the panel's rights: it can read every key and password Meridian holds, and do anything else the panel's user can. |
| `panel.js` | Its panel JavaScript runs in your browser with your session: it can do anything you can. |
| `status_page.js` | Its status page JavaScript runs in visitors' browsers - and on your users' own pages, where it sees their links and usage. |
| `filter:compile`, `filter:subscription` | Its filters can change what every server runs and what users' apps receive - a mistake can disconnect everyone. |
| `events` | It hears everything the panel records in its activity timeline: sign-ins, users, servers and changes, with names and IP addresses. |
| `api:read` | It can read everything through the panel's API: users and their links, servers, protocols and settings. |
| `api:write` | It can change everything the panel's API can change - users, servers, protocols, settings - and disconnect people. |
| `filter:status` | It can change what the status page shows - including things meant to stay private. |
| `filter:notify` | It can change your notifications, or hold them back. |
| `routes` | It adds its own API under /api/plugins/<id>/, which you and your API tokens can call. |
| `public_pages` | It serves pages that anyone on the internet can open, under /p/<id>/. |
| `mcp` | It adds its own tools for AI assistants. |
| `schedule` | It is called on a timer. |
| `notify` | It can send you notifications through your Telegram chat and webhook. |
| `panel.css` | It changes how the panel looks: it can hide, move or relabel anything on it. |
| `status_page.css` | It changes how the status page and your users' pages look: it can hide, move or relabel anything on them. |

## Installing, updating, turning on and off, removing

**Settings › Plugins** lists every plugin with its version, what it asks for, whether it is
**off**, **on** (styles and scripts only), **starting**, **running**, **crashed - starting again**,
**on, but held** (the panel started without plugins) or **damaged** (its files are missing: upload
it again), its last problem and, while its program runs, the routes, pages, MCP tools, timers and
hooks it added.

- **Upload a plugin** installs a zip. It stays off.
- **Turn on** shows what it asks for and what that lets it do; it turns on once you agree. Its
  styles load at once (reload the panel for its panel script), its program starts.
- **Turn off** stops its program; its styles, scripts, pages, tools and filters are gone at once. If
  its filter changed what servers run, they get the panel's own configuration again right away.
  A panel script already loaded in a browser stays until that page is reloaded.
- **Upload new version** replaces its files (the zip must have the same `id`). What it keeps in its
  data folder stays. A plugin that is on stays on - its program restarts with the new files - unless
  the new version asks for more than you agreed to: then it is turned off until you turn it on again.
- **Remove** stops it and deletes its files and its data folder.
- **Log** shows the last 300 lines its program logged or wrote to standard error, and what the
  panel noted about it.

Each of these is recorded in the timeline (Monitor › Events): `plugin_installed`,
`plugin_updated`, `plugin_enabled` (with what was agreed to), `plugin_disabled`, `plugin_removed`,
and from the panel `plugin_crashed`, `plugin_failed` (turned off after crashing),
`plugin_filter_failed`, `plugin_lagging` (too slow to take events) and `plugin_message` (a
plugin's notification).

The same through the API - from a signed-in browser only (`X-Meridian: 1`, the session cookie);
tokens may only list:

| | |
| --- | --- |
| `GET /api/settings/plugins` | every plugin: `id`, `name`, `version`, `state`, `parts`, `permissions`, `asks`, `warnings`, `script`, `running`, `last_error`, `restarts`... and `disabled` (started without plugins) |
| `POST /api/settings/plugins` | install: the zip as the body (`Content-Type: application/zip`) |
| `PUT /api/settings/plugins/{id}` | a new version |
| `POST /api/settings/plugins/{id}/enable` | `{"asks": [...]}`: exactly the plugin's `asks` list - turning it on agrees to it |
| `POST /api/settings/plugins/{id}/disable` | turn it off |
| `DELETE /api/settings/plugins/{id}` | remove it with its data |
| `GET /api/settings/plugins/{id}/log` | its program's log |

The MCP tool `list_plugins` shows assistants what is installed and what each plugin may do; there
is no tool that installs or switches plugins.

## Styles and scripts

A plugin's style sheets go into the pages' `<head>` after the pages' own, for everyone who opens
them. The status page script loads after the page's own code, for everyone who opens the status
page or a user's page. The panel script loads only once the supervisor has signed in (never on the
sign-in page) and is served to nobody else.

The pages keep their Content-Security-Policy: scripts and style sheets come only from the panel
itself, so a plugin cannot load anything from other sites, run inline `<script>` code or use
`eval`; a style sheet's images and fonts must be `data:` URLs. Write the page's text with
`textContent`, never with `innerHTML`: anything a user can type (names, notes) must not become
markup.

### The panel: window.Meridian

```js
;(function () {
  const M = window.Meridian
  M.addPage({
    path: '/report',            // the page is at /plugin/report
    title: 'Report',            // its heading and its entry in the top bar
    render(el, ctx) {           // draw into el; ctx: {path, query}
      el.textContent = 'Loading…'
      M.api('GET', '/api/overview').then((o) => { el.textContent = o.servers.online + ' of ' + o.servers.total + ' servers online' })
      return () => {}           // optional: called when the supervisor leaves the page
    },
  })
  M.addMenuItem({ label: 'Open the report', run: () => M.navigate('/plugin/report') })
  M.on('page', ({ path, el }) => { /* a page is on the screen */ })
})()
```

| | |
| --- | --- |
| `version` | the panel's version, `apiVersion` this API's (1) |
| `api(method, path, body?)` | calls the panel's API as the signed-in supervisor and resolves to the decoded answer; it rejects with an `Error` whose `status` is the HTTP status and whose message is the panel's |
| `on(name, fn)` | `"route"`: `fn({path, query})` when the address changes; `"page"`: `fn({path, el})` once the page for a new address is on the screen (its data may still be loading - watch `el` for more). Returns a function that stops listening. |
| `addPage({path, title, nav, render})` | a page at `/plugin<path>` (`path` like `/report`: lowercase letters, digits, dots and dashes); `nav: false` leaves it out of the top bar. `render(el, ctx)` draws it into the container the panel provides and may return a function that cleans up. |
| `addMenuItem({label, run})` | an entry in the account menu at the top right |
| `navigate(path)` | goes to a page of the panel (`/servers`, `/plugin/report`) |
| `toast(text, isError?)` | a short message at the bottom |
| `ask({title, text, confirm, danger})` | the panel's confirmation dialog; resolves to `true` or `false`. Ask before anything that disconnects people. |

The panel's own classes style a plugin's page like the rest: `panel`, `h`, `muted`, `faint`,
`mono`, `btn`, `btn primary`, `badge`, `chip`, `callout`, `kv-list`, a `table` with class `t`.

### The status page: window.MeridianStatus

```js
;(function () {
  const S = window.MeridianStatus
  S.decorateCard((card, server) => {
    if (!card.querySelector('.my-note')) {
      const note = document.createElement('small')
      note.className = 'my-note'
      note.textContent = server.online ? 'All good' : 'We are on it'
      card.append(note)
    }
  })
  S.on('data', (d) => console.log(d.servers.length + ' servers'))
})()
```

| | |
| --- | --- |
| `data` | the dashboard's data (`GET /api/status`) while the visitor may see it, otherwise `null` |
| `me` | a signed-in user's own page (`GET /api/portal/me`), otherwise `null` |
| `view` | the page shown: `overview`, `servers`, `events`, `me` or `signin` |
| `on(name, fn)` | `"data"` each time the dashboard's data arrives (every 30 seconds), `"me"` when a user's page data arrives (or `null` after signing out), `"view"` when another page is shown. Returns a function that stops listening. |
| `decorateCard(fn)` | `fn(card, server)` each time a server's card on the Servers page is drawn or brought up to date - every few seconds - so add things once (check first) |
| `toast(text)` | a short message at the bottom |
| `apiVersion` | this API's version (1) |

The status page script runs for visitors too: never put anything there that only the supervisor
may see, and remember a user's page shows their own link.

## Server plugins

The panel runs `server.command` while the plugin is on:

- its working directory is the plugin's folder; its data folder (kept across new versions, deleted
  with the plugin) is `MERIDIAN_PLUGIN_DATA`;
- its environment holds only `PATH`, `HOME` (the data folder), `LANG=C.UTF-8`, `TZ` (when the panel
  has one), `MERIDIAN_PLUGIN_ID`, `MERIDIAN_PLUGIN_DATA` and `MERIDIAN_VERSION` - none of the
  panel's own settings or secrets;
- it gets a process group of its own: stopping it stops whatever it started;
- it speaks JSON-RPC 2.0 with the panel on its standard input and output, one JSON object per line
  (at most 16 MB); what it writes to standard error goes to its log and, at most 60 lines a minute,
  to the panel's log;
- it must call `register` within 10 seconds of starting, or it is ended;
- to stop it, the panel closes its standard input, sends `SIGTERM`, and `SIGKILL` after 5 seconds;
- when it stops on its own, it starts again after 1, 5, 15, 30 and 60 seconds; one more stop in a
  row turns the plugin off with an event (a program that ran for a minute starts counting afresh).

### Where a server plugin runs

On the panel's host (Linux), as the panel's user, under the panel's service restrictions
(`deploy/install-panel.sh`): it can write only in the panel's data directory - its data folder is
there - and its own `/tmp`, it gets no new privileges, and it may not make memory both writable and
executable. Programs in Go, Rust, C or Python work; runtimes that compile code while they run
(Node.js, Java, .NET) do not. Build for the panel's processor: `GOOS=linux GOARCH=amd64` on most
servers, `arm64` on ARM ones.

### The protocol

Every message is a JSON-RPC 2.0 object on one line. A call has an `id` and gets an answer with the
same `id` (`result`, or `error` with `code` and `message`); a message without an `id` is a
notification and gets none. Both sides may call while waiting for answers, so read and write in
parallel and answer each call when it is done.

**From the plugin to the panel**

`register` - first, once: what it adds. Each part needs its permission.

```json
{"jsonrpc":"2.0","id":1,"method":"register","params":{
  "hooks":["event","filter.subscription"],
  "routes":[{"method":"GET","path":"/stats"},{"method":"*","path":"/items/*"}],
  "pages":[{"method":"GET","path":"/"}],
  "tools":[{"name":"summary","title":"Summary","description":"What the plugin counted",
            "input_schema":{"type":"object","properties":{"days":{"type":"integer"}}},
            "write":false,"destructive":false}],
  "schedules":[{"name":"hourly","every":3600}]}}
```
```json
{"jsonrpc":"2.0","id":1,"result":{"id":"hello","version":"1.0.0","data_dir":"/var/lib/meridian/plugin-data/hello",
 "permissions":["events","filter:subscription","mcp","routes","schedule"],"public_url":"https://panel.example.com"}}
```

- `hooks`: `event` (`events`), `filter.compile`, `filter.subscription`, `filter.status`,
  `filter.notify` (each its `filter:` permission).
- `routes` (its API) and `pages` (public pages, with `"public_pages": true`) need `routes`; at most
  50 each. `method` is `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, or `*`/empty for any (`GET` also
  answers `HEAD`). `path` is an exact path, or a folder ending in `/*` - `/items/*` answers `/items`,
  `/items/` and everything under it; `/*` answers every path.
- `tools` need `mcp`; at most 20. `name`: lowercase letters, digits and underscores (assistants see
  `<id>__<name>`). `description` is required - assistants choose tools by it. `input_schema` is a
  JSON schema object with named `properties` (`confirm` is the panel's). `write: true` hides the
  tool from read-only tokens; `destructive: true` (anything that disconnects people or cannot be
  undone) also makes the panel demand `confirm=true`, after the assistant has asked the user.
- `schedules` need `schedule`; at most 10. `name`: lowercase letters, digits, dashes and
  underscores; `every`: seconds, from 10 to 604800 (7 days). The first call comes one interval after
  registering.

`api` - a call to the panel's API, performed inside the panel as the supervisor, within `api:read`
(reading only) or `api:write`. The answer carries the API's status and body, whatever the status.

```json
{"jsonrpc":"2.0","id":2,"method":"api","params":{"method":"GET","path":"/api/servers"}}
{"jsonrpc":"2.0","id":2,"result":{"status":200,"body":[{"id":1,"name":"Tokyo"}]}}
```

`path` is a path of the API (`/api/...`, with a query if needed); `body` any JSON. Plugins cannot
call the plugins' management or APIs (`/api/settings/plugins`, `/api/plugins/`), users' pages
(`/api/portal/`), signing in and out, or what needs a signed-in browser (passwords, tokens, the
site's country rule) - those answer `403`. [docs/api.md](api.md) and `docs/openapi.json` describe
the API. At most 8 `api` and `notify` calls may run at once.

`notify` - a notification (`notify`; notifications must be set up in Settings › Notifications). It
goes out with the next batch, whatever kinds of events are chosen there; at most 30 an hour.

```json
{"jsonrpc":"2.0","id":3,"method":"notify","params":{"text":"Backup finished"}}
{"jsonrpc":"2.0","id":3,"result":{"queued":true}}
```

`log` - a line in its log on the Plugins page (`level`: `info`, `warn` or `error`). As a
notification it needs no answer:

```json
{"jsonrpc":"2.0","method":"log","params":{"level":"warn","message":"the backup server did not answer"}}
```

**From the panel to the plugin**

`event` (notification, with the `event` hook) - each event the panel records, from the moment the
program registered:

```json
{"jsonrpc":"2.0","method":"event","params":{"id":812,"ts":1791532464,"level":"info","kind":"user_created",
 "server_id":0,"user_id":14,"actor_id":1,"message":"User Dana created","data":{}}}
```

Events wait in a queue of 512; a program that does not keep up misses events (the Plugins page
counts them and the timeline says so) rather than holding anything up.

`filter.compile` - a server's configuration before the server gets it; answer the configuration
(changed or not). What keeps servers safe stays the panel's: the contract version, the server id,
the agent's settings, the cores and the actions waiting for the server are taken from the panel
whatever the answer says. The agent checks the result like any configuration and keeps its
running one when it cannot be applied (Monitor and the server's page show why).

```json
{"jsonrpc":"2.0","id":17,"method":"filter.compile","params":{"server_id":3,"state":{"contract":4,"server_id":3,"xray":{...},"blocked_ips":[]}}}
{"jsonrpc":"2.0","id":17,"result":{"contract":4,"server_id":3,"xray":{...},"blocked_ips":["198.51.100.7"]}}
```

The panel compiles a server's configuration whenever something about it changes, when a program
with this hook registers or stops, and on start - where it first waits (up to 10 seconds) for
these programs to register, so a panel restart never sends servers their configuration without the
plugin's changes. Answer the same configuration for the same input: a different answer every time
makes servers apply it again and again.

`filter.subscription` - what an app receives for a user's link (`format` is the app's: `clash`,
`stash`, `singbox`, `shadowrocket`, `base64`, `hiddify`, `loon`, `uri`, `surge`, `quanx`); answer
the body as a string (at most 8 MB). WireGuard configuration downloads are not filtered.

```json
{"jsonrpc":"2.0","id":18,"method":"filter.subscription","params":{"format":"uri","user_id":14,
 "content_type":"text/plain; charset=utf-8","body":"vless://...#Tokyo"}}
{"jsonrpc":"2.0","id":18,"result":"vless://...#%E2%98%85%20Tokyo"}
```

`filter.status` - the status page's data (what `GET /api/status` answers the asker: visitors get
what the status page shows them, the supervisor more); answer the data (a JSON object, at most
4 MB). The same data is filtered once however many people look.

```json
{"jsonrpc":"2.0","id":19,"method":"filter.status","params":{"data":{"title":"Status","servers":[...],"totals":{...}}}}
{"jsonrpc":"2.0","id":19,"result":{"title":"Status · all systems normal","servers":[...],"totals":{...}}}
```

`filter.notify` - a notification before it is sent: the events it reports and its text. Answer the
text to send (at most 8,000 characters), or `""` to hold this one back.

```json
{"jsonrpc":"2.0","id":20,"method":"filter.notify","params":{"events":[{"time":1791532464,"level":"crit",
 "kind":"server_offline","message":"Tokyo is offline"}],"text":"Meridian\nTokyo is offline"}}
{"jsonrpc":"2.0","id":20,"result":"Meridian\nTokyo is offline - the provider knows"}
```

`route` - a request to its API (`/api/plugins/<id>/...`, from the signed-in supervisor or an API
token) or to its public pages (`/p/<id>/...`, from anyone).

```json
{"jsonrpc":"2.0","id":21,"method":"route","params":{"method":"POST","path":"/items/5","query":"full=1",
 "headers":{"Content-Type":"application/json","User-Agent":"..."},"body":"{\"done\":true}",
 "public":false,"caller":{"kind":"token","scope":"full"},"ip":"203.0.113.9"}}
{"jsonrpc":"2.0","id":21,"result":{"status":200,"headers":{"Cache-Control":"no-store"},"body":{"ok":true}}}
```

- `path` is below `/api/plugins/<id>` or `/p/<id>`; `headers` never carry cookies or credentials;
  a body that is not text arrives as `body_base64` instead of `body` (at most 1 MB);
  `caller` (its API only) is `{"kind":"session"}` or `{"kind":"token","scope":"read"|"full"}`.
- The answer: `status` (default 200), `headers`, and `body` - a string is sent as text, any other
  JSON as JSON - or `body_base64` for bytes. The headers it may set are `Content-Type`,
  `Cache-Control`, `Content-Disposition`, `ETag`, `Last-Modified`, `Location` (a path or an
  `https://` address), `Retry-After`, `Content-Language` and `Vary`; public pages may also set the
  `Access-Control-Allow-*` headers.
- Its API needs a signed-in session (with `X-Meridian: 1` on changes) or an API token; read-only
  tokens may only `GET`. Its answers can never run as a page on the panel's site (they are sent
  sandboxed).
- Public pages answer anyone, at most 240 requests a minute from one address. They run sandboxed in
  a separate origin: their scripts and forms work, inline ones too, and they load files from the
  panel's site (their own paths) and `data:` URLs, but they never see the panel's cookies, storage or
  API. A page's script that fetches from its own paths gets the answer only if that answer allows it
  (`Access-Control-Allow-Origin: *`).

`mcp` - a call of one of its MCP tools; `scope` is the caller's token (`read` or `full`). A string
answer reaches the assistant as text, anything else as JSON; an `error` as the tool's failure.

```json
{"jsonrpc":"2.0","id":22,"method":"mcp","params":{"tool":"summary","args":{"days":7},"scope":"read"}}
{"jsonrpc":"2.0","id":22,"result":"Since Monday the panel recorded 212 events."}
```

`tick` - one of its timers; answer when the work is done (any result). A timer whose last call has
not come back skips its turn.

```json
{"jsonrpc":"2.0","id":23,"method":"tick","params":{"name":"hourly"}}
{"jsonrpc":"2.0","id":23,"result":{}}
```

### Time limits and failures

| Call | Time limit | When it fails or takes too long |
| --- | --- | --- |
| `register` | 10 s after starting | the program is ended (and started again, see above) |
| `filter.*` | 2 s | the value goes on unchanged, the timeline says so (at most every 10 minutes), and the plugin is passed over for 30 seconds; servers' configurations are compiled again after that |
| `route` | 15 s | the caller gets `502` or `504` with what happened |
| `mcp` | 30 s | the tool answers with the error |
| `tick` | 1 min | its log says so |
| `event` | - | a full queue drops events, counted on the Plugins page |

So a slow or broken plugin can delay what it filters by 2 seconds at most, and never holds up the
panel, the agents or subscriptions. Filters run one plugin after another, in the order of their
ids, each getting what the one before made of it.

## Safety and recovery

If a plugin breaks the panel's pages, slows it down or makes servers misbehave:

1. **Turn it off on the panel's host** - a running panel follows within a few seconds, without a
   restart:

   ```bash
   sudo -u meridian meridian plugins list --data /var/lib/meridian
   sudo -u meridian meridian plugins disable night-blue --data /var/lib/meridian
   ```

   Expected: a table of the plugins (`ID VERSION ON NAME LAST PROBLEM`), then `The plugin
   night-blue is off.` `meridian plugins remove ID` removes one with its data;
   `meridian plugins enable ID` shows what it asks for and turns it on only with `--yes`.

2. **Or start the panel without any plugin** - nothing of any plugin runs or is served, and the
   Plugins page says so; you can still turn plugins off or remove them there:

   ```bash
   echo MERIDIAN_NO_PLUGINS=1 | sudo tee -a /etc/meridian/meridian.env
   sudo systemctl restart meridian
   ```

   (`meridian serve --no-plugins` does the same.) Delete that line and restart to bring back the
   plugins that are on. Proxies keep running while the panel restarts; servers whose configuration a
   plugin filtered get the panel's own configuration while it runs without plugins.

What protects you:

- installing, updating, turning on and removing need a signed-in browser (and its CSRF header) or
  the panel's host - not API tokens, not MCP, not other plugins;
- a plugin runs nothing and is served nowhere until you agree to exactly what it asks for; a new
  version that asks for more is turned off;
- a program gets none of the panel's secrets in its environment (it can still read the panel's
  files: that is what the warning says), its calls to the API go through the same checks as
  everyone's, and it cannot call the plugins' management;
- every call to a program has a time limit and everything sent to it waits in bounded queues;
- the panel keeps what keeps servers safe (contract, agent settings, cores, actions) whatever a
  compile filter answers, and agents check every configuration before applying it;
- a plugin's API answers are sandboxed and its public pages run in an origin of their own;
- nothing but the style sheets and scripts `plugin.json` names is served from a plugin's folder,
  and the panel script only to the signed-in supervisor.
