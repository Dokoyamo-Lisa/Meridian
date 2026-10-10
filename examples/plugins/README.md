# Example plugins

Two plugins to start from. [docs/plugins.md](../../docs/plugins.md) describes everything a plugin
can do: the manifest, the permissions and what each lets a plugin do, the protocol a server plugin
speaks, and the JavaScript APIs of the panel and the status page.

| Folder | What it shows |
| --- | --- |
| [`ember-theme`](ember-theme) | Styles only: a warm accent colour for the panel, the status page and users' pages. It runs nothing. |
| [`hello`](hello) | Every other part: a program written in Go that counts the panel's events, puts a star before the entries of users' subscriptions, answers its own API route, offers an MCP tool and runs a timer that reads the panel's API - plus a page in the panel and a touch on the status page. |

A plugin is a zip file with `plugin.json` at its top. Install it in the panel under
**Settings › Plugins › Upload a plugin**. It arrives turned off; **Turn on** lists what it may do
and asks you to agree.

## Ember (a theme)

Zip the folder's files and upload the zip:

```bash
cd examples/plugins/ember-theme
zip ../ember-theme.zip plugin.json panel.css status.css
```

Expected: `adding: plugin.json`, `adding: panel.css`, `adding: status.css`, and
`examples/plugins/ember-theme.zip`. Upload it, turn it on, and reload the panel: links, buttons and
charts turn orange. Turn it off (or remove it) and reload to get the panel's own colours back.

To make your own theme, copy the folder, change `id`, `name` and the colours. The panel and the
status page draw everything from the CSS variables at the top of `web/src/app.css` and
`web/src/status/status.css`; a plugin's style sheet loads after them, so its rules win. Images and
fonts in a style sheet must be `data:` URLs: the pages load nothing from other sites, and nothing but
the style sheets and scripts `plugin.json` names is served from a plugin.

## Hello (a server plugin)

The program is a Go `main` package that uses only the standard library. Build it for the panel's
machine - Linux, `amd64` on most servers, `arm64` on ARM ones (`uname -m` on the panel's host says
`x86_64` or `aarch64`) - and zip it with the manifest and the two scripts:

```bash
cd examples/plugins/hello
mkdir -p /tmp/hello-plugin/bin
cp plugin.json panel.js status.js /tmp/hello-plugin/
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/hello-plugin/bin/hello .
(cd /tmp/hello-plugin && zip -r ../hello.zip .)
```

Expected: no output from `go build`, then `adding: ...` lines for `plugin.json`, `panel.js`,
`status.js` and `bin/hello`, and the file `/tmp/hello.zip` (about 2-3 MB). If `go build` fails with
"go: command not found", install Go 1.26 or newer from https://go.dev/dl/ (as for building
Rosélune).

Upload `/tmp/hello.zip` and turn it on. Within a few seconds its row on the Plugins page says
**running** and lists what it added:

- the API route `GET /api/plugins/hello/stats` (call it with an API token, or open the panel's new
  **Hello** page in the top bar, which shows the same numbers),
- the MCP tool `hello__summary` (ask an assistant connected to the panel's MCP for "the Hello
  summary"),
- the timer `count-servers` (every 5 minutes it counts the servers through the panel's API),
- its hooks: `event` (it counts every event the panel records) and `filter.subscription` (it puts
  "★ " before every entry of users' subscriptions in the share-link, Clash, Stash and sing-box
  formats).

On the status page's **Servers** view every server's name gets a star too.

If the row says **restarting** or **off** with a problem, open **Log**: it shows what the program
wrote and why the panel stopped it. The most common causes:

| The log says | Do this |
| --- | --- |
| `exec format error` | the program was built for another system or processor: build it again with the right `GOOS=linux GOARCH=...` |
| `did not register within 10 seconds` | the program must call `register` as soon as it starts |
| `register was refused: ...` | it asked for something `plugin.json` does not allow: add the permission, or leave the hook out |

Removing the plugin also deletes what it kept in its data folder (its counts).

## Your own server plugin

Copy `hello`, change `id` and `name` in `plugin.json`, keep the parts of `main.go` you need and
list only the permissions you use - every permission is one more thing the supervisor must agree
to. Any language works if it reads and writes lines of JSON on standard input and output; the
program runs on the panel's host as the panel's user, under the panel's service restrictions
(docs/plugins.md, "Where a server plugin runs").
