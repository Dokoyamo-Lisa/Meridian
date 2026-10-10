# Rosélune's demo

A panel anyone may look around in and nobody can change. Its eight servers and sixteen people do not
exist. Every server, client and device address comes from the ranges kept for documentation
(192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24 and 2001:db8::/32), and the panel is drawn in
Amsterdam on the status page's globe. None of it points at a real machine or a real place.

It has three parts:

- **The panel**: an ordinary Rosélune release with a database of its own. Nothing in Rosélune knows it
  is a demo.
- **`meridian-demo`** (this folder): `setup` fills a new panel through its API with the demo's servers,
  protocols, users, plans, ping monitors, traffic rules and a forward, and writes the month before
  today. `run` then plays every server's agent through the agents' own protocol. Every ten seconds it
  sends the hosts' load and who is online, every minute the traffic, devices, destinations and ping
  rounds. Each hour it moves the demo's dates along, so renewals stay ahead and the story holds.
- **The reverse proxy in front**:
  - It lets every visitor in with a **read-only API token** (no sign-in; the panel refuses changes
    from such a token).
  - It turns away every request that is not `GET` or `HEAD` with a plain message, and keeps agents,
    the MCP endpoint and the console out.
  - It gives the panel the same documentation address for every visitor, so no visitor's IP address
    is ever recorded or shown to the next one.

## Run it

```bash
make && go build -o meridian-demo ./demo

# the panel: its own user, data and port; no plugins, no update checks, no IP database downloads
sudo useradd --system --home /var/lib/meridian-demo --shell /usr/sbin/nologin meridian-demo
sudo install -d -o meridian-demo -m 700 /var/lib/meridian-demo
sudo -u meridian-demo env MERIDIAN_ADMIN_USER=demo MERIDIAN_NO_PLUGINS=1 MERIDIAN_NO_UPDATE_CHECK=1 \
  MERIDIAN_NO_GEO_DOWNLOAD=1 MERIDIAN_TRUSTED_PROXIES=127.0.0.1/32 \
  meridian serve --listen 127.0.0.1:8090 --data /var/lib/meridian-demo &

# fill it (once, on the new panel), then play the agents
sudo -u meridian-demo sh -c 'MERIDIAN_TOKEN=$(meridian token --data /var/lib/meridian-demo --name demo-setup) \
  ./meridian-demo setup -panel http://127.0.0.1:8090 -data /var/lib/meridian-demo \
  -url https://demo.example.com -state /var/lib/meridian-demo/agents.json'
sudo -u meridian-demo sh -c 'MERIDIAN_TOKEN=$(meridian token --data /var/lib/meridian-demo --name demo-agents --scope read --days 0) \
  ./meridian-demo run -panel http://127.0.0.1:8090 -state /var/lib/meridian-demo/agents.json -data /var/lib/meridian-demo'
```

A visitors' token: `sudo -u meridian-demo meridian token --data /var/lib/meridian-demo --name visitors
--scope read --days 0`, for the proxy below. Run the panel and `run` as services: see
[`systemd/`](systemd). Nobody signs in to the demo panel, so its first password
(`/var/lib/meridian-demo/initial-admin.txt`) can go.

## The proxy (Caddy)

```caddy
demo.example.com {
	encode zstd gzip
	route {
		# agents, assistants and the console stay out
		@closed path /agent/* /mcp /mcp/* /api/servers/*/console/ws
		respond @closed 404
		# nothing changes: only GET and HEAD reach the panel
		@change not method GET HEAD
		header @change Content-Type "application/json; charset=utf-8"
		respond @change `{"error":"This is a demo: look around as much as you like - nothing here can be changed."}` 403
		reverse_proxy 127.0.0.1:8090 {
			# every visitor is let in, read-only, and is the same documentation address to the panel
			header_up Authorization "Bearer VISITORS_READ_ONLY_TOKEN"
			header_up X-Forwarded-For 192.0.2.1
			header_up -X-Real-IP
			header_up -Forwarded
			header_up -CF-Connecting-IP
			header_up -True-Client-IP
			header_up -Cookie
		}
	}
}
```

Behind Cloudflare's proxy, set the zone's SSL/TLS mode to **Full (strict)**. Caddy then gets its
certificate as usual, and the server's address stays out of the DNS.

The panel also refuses every change from a read-only token, so a request that got past the proxy's
rule would change nothing either. Two places in Settings, the API tokens and the signed-in browsers,
say they need a signed-in browser. That is expected: visitors never sign in.
