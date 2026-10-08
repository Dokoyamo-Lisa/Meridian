# MCP: using the panel from Claude and other AI assistants

The panel is a [Model Context Protocol](https://modelcontextprotocol.io) server at `/mcp`
(Streamable HTTP). An assistant connected to it can answer questions like *"who is sharing their
link?"*, *"where does alice's traffic go?"* or *"which servers need attention?"*, and - with a
full-access token - add users and servers, set up protocols, country rules and the status page, or
block an IP.

## Connect

Create a token in **Settings › API & MCP**. Start with **Read only**; give an assistant **Full
access** only when you want it to make changes.

**Claude Code**

```bash
claude mcp add --transport http meridian https://panel.example.com/mcp \
  --header "Authorization: Bearer mrd_..."
```

**Clients that take a JSON configuration** (Cursor, VS Code, Claude Code's `.mcp.json`, …)

```json
{
  "mcpServers": {
    "meridian": {
      "type": "http",
      "url": "https://panel.example.com/mcp",
      "headers": { "Authorization": "Bearer mrd_..." }
    }
  }
}
```

**Clients that only start local programs** use the bridge built into the `meridian` binary - each
release has `meridian-cli-<version>-linux-<arch>` builds (Meridian runs on Linux only); save one as,
say, `/usr/local/bin/meridian`. On other systems, connect the client over HTTP as above. The bridge
reads the token from the environment, never from the command line:

```json
{
  "mcpServers": {
    "meridian": {
      "command": "/usr/local/bin/meridian",
      "args": ["mcp", "--url", "https://panel.example.com"],
      "env": { "MERIDIAN_TOKEN": "mrd_..." }
    }
  }
}
```

The bridge refuses plain `http://` panel addresses outside localhost, so the token never travels
unencrypted.

For the best results also install the **meridian-admin** skill (`skills/meridian-admin` in this
repository): it teaches the assistant the workflows and the safety rules below.

## Tools

Read (any token):

| Tool | What it returns |
| --- | --- |
| `overview` | servers online, people online, traffic today, alerts |
| `list_servers`, `get_server` | servers with protocols, forwards, load, bandwidth |
| `list_users`, `get_user` | limits, usage, flags, sign-in, who is connected now, the link |
| `online_now` | every open connection with IP, place, network, server, protocol |
| `ip_history` | IPs per user or server over a period, with places and networks |
| `destinations` | where traffic went (domains/IPs, connections, bytes) |
| `user_traffic` | daily traffic and per-protocol totals |
| `events` | the activity timeline |
| `find_sharing` | users whose link looks shared: over their IP limit, many IPs, countries or networks |
| `check_protocol` | whether a protocol draft can be saved, what it becomes, which apps can use it; with `protocol_id`, a change to that protocol (as saving would apply it) |
| `get_scan` | what a scan found on a server (existing Xray, V2Ray, x-ui, 3x-ui, sing-box, Hysteria2) |
| `get_access` | the country rules, devices connected now by country, refused packets |
| `get_notifications` | where notifications go (masked) and what is sent |
| `get_status_page` | how the status page is set up, and where users sign in |
| `list_certificates` | shared certificates, the protocols using each and whether each server serves it yet (no private keys) |
| `list_blocked_ips`, `action_status` | |
| `check_updates` | this panel's version, the newest release and its notes, whether updates install by themselves, servers with an older agent |

Change (full-access token):

| Tool | Notes |
| --- | --- |
| `create_user`, `update_user`, `new_user_password`, `resume_user`, `reset_user_usage`, `sign_out_user` | generated passwords come back once; access by whole servers (`server_ids`) and single protocols (`protocol_ids`) |
| `pause_user`, `rotate_user_link`, `reset_user_credentials`, `delete_user` | disconnect people - need `confirm=true` |
| `add_server`, `get_install_command` | return the install command (it contains the server's secret) |
| `update_server`, `set_server_ports` | name, address (an IP also sets the location from DB-IP), IP version, ports from the provider, Xray configuration code - Xray takes them live; an IP version change restarts Hysteria2 protocols once, so it needs `confirm=true` |
| `add_protocol`, `add_forward`, `unblock_ip`, `scan_server` | applied live; a scan changes nothing; a protocol can have its own server address (`bind_ip`) or serve only proxy passes (`pass_only`) |
| `update_protocol` | changes a protocol in place (only the given fields); needs `confirm=true` when its devices must refresh their subscription (transport, security, domain, certificate, port, address...) or a Hysteria2 protocol restarts |
| `set_protocol_code` | a protocol's own settings as code: Xray JSON (its inbound, outbounds, rules for its traffic only) or Hysteria2 YAML (it restarts, so that needs `confirm=true`) |
| `replace_certificate` | replaces a shared certificate once for every server that uses it (Hysteria2 protocols using it restart: then it needs `confirm=true`) |
| `set_status_page`, `set_server_on_status_page` | where the status page is, whether visitors see the servers (`public`) and their IP addresses (`show_ips`), and how each server appears on its globe |
| `set_notifications`, `test_notifications` | send problems to Telegram and/or an HTTPS webhook; a test message per channel |
| `set_branding` | the panel's name, its logo (SVG markup or a base64 image; checked like an upload) and how the logo moves |
| `import_protocols`, `set_country_rule`, `set_protocol_enabled`, `remove_protocol`, `remove_forward`, `block_ip`, `server_action` | can disconnect people or stop services - need `confirm=true` |
| `update_panel`, `upgrade_all_agents` | install the newest release (the panel restarts; proxies keep running) and upgrade every older agent (nobody is disconnected) - need `confirm=true` |

## Safety model

- The token decides what the assistant can do; a read-only token never even sees the change tools.
- Every tool runs through the same REST handlers as the web UI: the same validation, the same
  checks. Tools never see private keys.
- Tools that disconnect people, stop services or cannot be undone are marked destructive and fail
  unless called with `confirm=true`. The server's instructions tell the assistant to describe the
  effect and get your explicit confirmation first. Your MCP client will usually ask you to approve
  each call too.
- The rule for who may open the site cannot be changed through MCP (a wrong rule could lock you
  out); use the panel.
- Revoke the token in **Settings › API & MCP** and the assistant loses access immediately.

## Examples

- "Which links look shared this week?" → `find_sharing {days: 7}`, then `ip_history` for the top
  suspect.
- "Make 5 users for the design team, 50 GB each, only on the Tokyo servers." → `list_servers`, then
  `create_user {name: "design", count: 5, quota_gb: 50, server_ids: [...]}`, and pass on the
  usernames and passwords it returns.
- "Put the status page on the front page and show the panel in Hong Kong." →
  `set_status_page {mode: "home", panel_city: "Hong Kong"}`.
- "Don't show our server addresses on the status page." → `set_status_page {show_ips: false}`.
- "The globe shows Seattle-1 in Kansas." → `set_server_on_status_page {server_id: 4, city: "Seattle"}`.
- "Block 203.0.113.7 for a day, it's scanning us." → the assistant asks you to confirm, then
  `block_ip {ip: "203.0.113.7", hours: 24, confirm: true}`.
