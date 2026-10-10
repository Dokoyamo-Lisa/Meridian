package db

// migrations run in order; each one exactly once. Never edit a released step - append a new one.
var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
  display_name  TEXT    NOT NULL DEFAULT '',
  role          TEXT    NOT NULL CHECK (role IN ('owner','admin')),
  password_hash TEXT    NOT NULL,
  totp_secret   TEXT    NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  max_servers   INTEGER NOT NULL DEFAULT 0,
  max_subs      INTEGER NOT NULL DEFAULT 0,
  note          TEXT    NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  last_login_at INTEGER NOT NULL DEFAULT 0,
  last_login_ip TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE sessions (
  token_hash   TEXT    PRIMARY KEY,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT    NOT NULL DEFAULT '',
  ua           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_account ON sessions(account_id);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE servers (
  id                INTEGER PRIMARY KEY,
  account_id        INTEGER NOT NULL REFERENCES accounts(id),
  name              TEXT    NOT NULL,
  secret            TEXT    NOT NULL,
  address           TEXT    NOT NULL DEFAULT '',
  note              TEXT    NOT NULL DEFAULT '',
  sort              INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  deleted_at        INTEGER NOT NULL DEFAULT 0,
  -- reported by the agent
  instance_id       TEXT    NOT NULL DEFAULT '',
  last_seq          INTEGER NOT NULL DEFAULT 0,
  agent_version     TEXT    NOT NULL DEFAULT '',
  hostname          TEXT    NOT NULL DEFAULT '',
  os                TEXT    NOT NULL DEFAULT '',
  kernel            TEXT    NOT NULL DEFAULT '',
  arch              TEXT    NOT NULL DEFAULT '',
  cpu_model         TEXT    NOT NULL DEFAULT '',
  cpu_cores         INTEGER NOT NULL DEFAULT 0,
  mem_total         INTEGER NOT NULL DEFAULT 0,
  disk_total        INTEGER NOT NULL DEFAULT 0,
  ipv4              TEXT    NOT NULL DEFAULT '',
  ipv6              TEXT    NOT NULL DEFAULT '',
  country           TEXT    NOT NULL DEFAULT '',
  city              TEXT    NOT NULL DEFAULT '',
  lat               REAL,
  lon               REAL,
  caps              TEXT    NOT NULL DEFAULT '{}',
  boot_time         INTEGER NOT NULL DEFAULT 0,
  agent_started_at  INTEGER NOT NULL DEFAULT 0,
  first_seen_at     INTEGER NOT NULL DEFAULT 0,
  last_seen_at      INTEGER NOT NULL DEFAULT 0,
  online            INTEGER NOT NULL DEFAULT 0,
  status_changed_at INTEGER NOT NULL DEFAULT 0,
  applied_rev       TEXT    NOT NULL DEFAULT '',
  apply_errors      TEXT    NOT NULL DEFAULT '',
  pending_restart   TEXT    NOT NULL DEFAULT '',
  xray_version      TEXT    NOT NULL DEFAULT '',
  -- provider bandwidth quota (informational)
  bw_limit          INTEGER NOT NULL DEFAULT 0,
  bw_mode           TEXT    NOT NULL DEFAULT 'both',
  bw_reset_day      INTEGER NOT NULL DEFAULT 1,
  bw_offset         INTEGER NOT NULL DEFAULT 0,
  cycle_rx          INTEGER NOT NULL DEFAULT 0,
  cycle_tx          INTEGER NOT NULL DEFAULT 0,
  cycle_start       INTEGER NOT NULL DEFAULT 0,
  -- billing (informational)
  price             REAL    NOT NULL DEFAULT 0,
  currency          TEXT    NOT NULL DEFAULT 'USD',
  billing_cycle     TEXT    NOT NULL DEFAULT 'month',
  expires_on        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX servers_account ON servers(account_id);

-- A node is one protocol endpoint on a server ("protocols" in the UI).
CREATE TABLE nodes (
  id         INTEGER PRIMARY KEY,
  server_id  INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL,
  name       TEXT    NOT NULL DEFAULT '',
  port       INTEGER NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  settings   TEXT    NOT NULL DEFAULT '{}',
  host       TEXT    NOT NULL DEFAULT '',
  pass_node  INTEGER NOT NULL DEFAULT 0, -- proxy pass: leave the internet through this node
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX nodes_server ON nodes(server_id);

-- Port forwarding (realm) on a server.
CREATE TABLE forwards (
  id             INTEGER PRIMARY KEY,
  server_id      INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name           TEXT    NOT NULL DEFAULT '',
  listen_port    INTEGER NOT NULL,
  network        TEXT    NOT NULL DEFAULT 'tcp+udp',
  target         TEXT    NOT NULL,
  engine         TEXT    NOT NULL DEFAULT 'nft', -- nft (kernel NAT) | realm
  proxy_protocol INTEGER NOT NULL DEFAULT 0,
  enabled        INTEGER NOT NULL DEFAULT 1,
  up_total       INTEGER NOT NULL DEFAULT 0,
  down_total     INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX forwards_server ON forwards(server_id);

-- A subscription is one link: one person, device or team. Limits are soft - they raise flags,
-- they never pause anything. Pausing is a manual action.
CREATE TABLE subs (
  id             INTEGER PRIMARY KEY,
  account_id     INTEGER NOT NULL REFERENCES accounts(id),
  name           TEXT    NOT NULL,
  note           TEXT    NOT NULL DEFAULT '',
  token          TEXT    NOT NULL UNIQUE,
  uuid           TEXT    NOT NULL,
  secret         TEXT    NOT NULL,
  paused         INTEGER NOT NULL DEFAULT 0,
  paused_at      INTEGER NOT NULL DEFAULT 0,
  quota          INTEGER NOT NULL DEFAULT 0,
  reset_day      INTEGER NOT NULL DEFAULT 0,
  expires_at     INTEGER NOT NULL DEFAULT 0,
  ip_limit       INTEGER NOT NULL DEFAULT 0,
  scope          TEXT    NOT NULL DEFAULT '',
  routing        TEXT    NOT NULL DEFAULT '',
  cycle_up       INTEGER NOT NULL DEFAULT 0,
  cycle_down     INTEGER NOT NULL DEFAULT 0,
  cycle_start    INTEGER NOT NULL DEFAULT 0,
  total_up       INTEGER NOT NULL DEFAULT 0,
  total_down     INTEGER NOT NULL DEFAULT 0,
  last_online_at INTEGER NOT NULL DEFAULT 0,
  last_fetch_at  INTEGER NOT NULL DEFAULT 0,
  last_fetch_ip  TEXT    NOT NULL DEFAULT '',
  last_fetch_ua  TEXT    NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX subs_account ON subs(account_id);

CREATE TABLE wg_peers (
  sub_id      INTEGER NOT NULL REFERENCES subs(id) ON DELETE CASCADE,
  node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  private_key TEXT    NOT NULL,
  public_key  TEXT    NOT NULL,
  psk         TEXT    NOT NULL,
  ip4         TEXT    NOT NULL,
  ip6         TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (sub_id, node_id)
);
CREATE INDEX wg_peers_node ON wg_peers(node_id);

CREATE TABLE traffic_daily (
  day     TEXT    NOT NULL,
  sub_id  INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  up      INTEGER NOT NULL DEFAULT 0,
  down    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, sub_id, node_id)
);
CREATE INDEX traffic_daily_sub ON traffic_daily(sub_id, day);

CREATE TABLE server_daily (
  day       TEXT    NOT NULL,
  server_id INTEGER NOT NULL,
  rx        INTEGER NOT NULL DEFAULT 0,
  tx        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, server_id)
);

CREATE TABLE forward_daily (
  day        TEXT    NOT NULL,
  forward_id INTEGER NOT NULL,
  up         INTEGER NOT NULL DEFAULT 0,
  down       INTEGER NOT NULL DEFAULT 0,
  conns      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, forward_id)
);

-- Every IP that connected, per subscription, per server, per day.
CREATE TABLE ip_log (
  day        TEXT    NOT NULL,
  sub_id     INTEGER NOT NULL,
  ip         TEXT    NOT NULL,
  server_id  INTEGER NOT NULL,
  node_id    INTEGER NOT NULL DEFAULT 0,
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  conns      INTEGER NOT NULL DEFAULT 0,
  country    TEXT    NOT NULL DEFAULT '',
  city       TEXT    NOT NULL DEFAULT '',
  asn        INTEGER NOT NULL DEFAULT 0,
  org        TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (day, sub_id, ip, server_id)
);
CREATE INDEX ip_log_ip ON ip_log(ip);
CREATE INDEX ip_log_sub ON ip_log(sub_id, last_seen);
CREATE INDEX ip_log_server ON ip_log(server_id, last_seen);

-- Where traffic went, per subscription, per server, per day.
CREATE TABLE dest_log (
  day       TEXT    NOT NULL,
  sub_id    INTEGER NOT NULL,
  server_id INTEGER NOT NULL,
  host      TEXT    NOT NULL,
  port      INTEGER NOT NULL,
  network   TEXT    NOT NULL DEFAULT 'tcp',
  conns     INTEGER NOT NULL DEFAULT 0,
  bytes     INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL,
  PRIMARY KEY (day, sub_id, server_id, host, port, network)
);
CREATE INDEX dest_log_sub ON dest_log(sub_id, day);
CREATE INDEX dest_log_server ON dest_log(server_id, day);

CREATE TABLE server_metrics (
  server_id INTEGER NOT NULL,
  ts        INTEGER NOT NULL,
  cpu       REAL    NOT NULL DEFAULT 0,
  mem_used  INTEGER NOT NULL DEFAULT 0,
  disk_used INTEGER NOT NULL DEFAULT 0,
  load1     REAL    NOT NULL DEFAULT 0,
  rx_rate   INTEGER NOT NULL DEFAULT 0,
  tx_rate   INTEGER NOT NULL DEFAULT 0,
  tcp       INTEGER NOT NULL DEFAULT 0,
  online    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (server_id, ts)
);

CREATE TABLE events (
  id         INTEGER PRIMARY KEY,
  ts         INTEGER NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  level      TEXT    NOT NULL DEFAULT 'info',
  kind       TEXT    NOT NULL,
  server_id  INTEGER NOT NULL DEFAULT 0,
  sub_id     INTEGER NOT NULL DEFAULT 0,
  actor_id   INTEGER NOT NULL DEFAULT 0,
  message    TEXT    NOT NULL,
  data       TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX events_account ON events(account_id, id);

CREATE TABLE actions (
  id         INTEGER PRIMARY KEY,
  server_id  INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL,
  args       TEXT    NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  created_by INTEGER NOT NULL DEFAULT 0,
  status     TEXT    NOT NULL DEFAULT 'pending',
  output     TEXT    NOT NULL DEFAULT '',
  done_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX actions_server ON actions(server_id, status);

-- IPs an admin blocked on their servers (abuse). Applied with nftables, released by hand or on expiry.
CREATE TABLE ip_blocks (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  ip         TEXT    NOT NULL,
  reason     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  created_by INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE (account_id, ip)
);
`,
	// 2: API tokens (scripts, MCP) and TOTP replay protection
	`
CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name         TEXT    NOT NULL,
  token_hash   TEXT    NOT NULL UNIQUE,
  prefix       TEXT    NOT NULL,
  scope        TEXT    NOT NULL CHECK (scope IN ('read','full')),
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  last_used_ip TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX api_tokens_account ON api_tokens(account_id);
ALTER TABLE accounts ADD COLUMN totp_last INTEGER NOT NULL DEFAULT 0;
`,
	// 3: every Xray protocol (VLESS REALITY becomes VLESS with security reality) and credentials
	// carried over from another panel when its protocols are imported
	`
UPDATE nodes SET kind = 'vless',
  settings = json_set(settings, '$.security', 'reality', '$.transport', 'raw', '$.flow', 'xtls-rprx-vision')
  WHERE kind = 'vless-reality';
UPDATE nodes SET settings = json_set(settings, '$.security', 'none', '$.transport', 'raw') WHERE kind = 'shadowsocks';
UPDATE nodes SET settings = json_set(settings, '$.cert_mode', 'self') WHERE kind = 'hysteria2';
CREATE TABLE node_creds (
  node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  sub_id   INTEGER NOT NULL REFERENCES subs(id) ON DELETE CASCADE,
  id       TEXT    NOT NULL DEFAULT '',
  password TEXT    NOT NULL DEFAULT '',
  username TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (node_id, sub_id)
);
`,
	// 4: one supervisor. Users (subscriptions) can sign in to see their own usage. Everything that
	// belonged to extra admin accounts moves to the supervisor, and those accounts are removed.
	`
CREATE TEMP TABLE m4_owner AS SELECT id FROM accounts WHERE role = 'owner' ORDER BY id LIMIT 1;
UPDATE servers SET account_id = (SELECT id FROM m4_owner)
  WHERE account_id IN (SELECT id FROM accounts WHERE role = 'admin') AND EXISTS (SELECT 1 FROM m4_owner);
UPDATE subs SET account_id = (SELECT id FROM m4_owner)
  WHERE account_id IN (SELECT id FROM accounts WHERE role = 'admin') AND EXISTS (SELECT 1 FROM m4_owner);
UPDATE OR IGNORE ip_blocks SET account_id = (SELECT id FROM m4_owner)
  WHERE account_id IN (SELECT id FROM accounts WHERE role = 'admin') AND EXISTS (SELECT 1 FROM m4_owner);
UPDATE events SET account_id = (SELECT id FROM m4_owner)
  WHERE account_id IN (SELECT id FROM accounts WHERE role = 'admin') AND EXISTS (SELECT 1 FROM m4_owner);
DELETE FROM ip_blocks WHERE account_id IN (SELECT id FROM accounts WHERE role = 'admin') AND EXISTS (SELECT 1 FROM m4_owner);
DELETE FROM accounts WHERE role = 'admin' AND EXISTS (SELECT 1 FROM m4_owner);
DROP TABLE m4_owner;

ALTER TABLE subs ADD COLUMN login TEXT NOT NULL DEFAULT '';
ALTER TABLE subs ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE subs ADD COLUMN last_login_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subs ADD COLUMN last_login_ip TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX subs_login ON subs(login COLLATE NOCASE) WHERE login != '';

CREATE TABLE user_sessions (
  token_hash   TEXT    PRIMARY KEY,
  sub_id       INTEGER NOT NULL REFERENCES subs(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT    NOT NULL DEFAULT '',
  ua           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX user_sessions_sub ON user_sessions(sub_id);

ALTER TABLE traffic_daily ADD COLUMN server_id INTEGER NOT NULL DEFAULT 0;
UPDATE traffic_daily SET server_id = COALESCE((SELECT server_id FROM nodes WHERE nodes.id = traffic_daily.node_id), 0);
CREATE INDEX traffic_daily_server ON traffic_daily(sub_id, server_id, day);
`,
	// 5: country rules - each server follows the global rule ('' ), has none ('off') or its own
	`
ALTER TABLE servers ADD COLUMN country_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN country_list TEXT NOT NULL DEFAULT '[]';
ALTER TABLE server_daily ADD COLUMN geo_drops INTEGER NOT NULL DEFAULT 0;
`,
	// 6: the public status page - availability in 10-minute buckets, and how each server appears
	`
CREATE TABLE server_uptime (
  server_id INTEGER NOT NULL,
  bucket    INTEGER NOT NULL,
  samples   INTEGER NOT NULL DEFAULT 0,
  up        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (server_id, bucket)
);
ALTER TABLE servers ADD COLUMN public_name TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN status_hidden INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN loc_manual INTEGER NOT NULL DEFAULT 0;
`,
	// 7: importing what already runs on a server
	`
CREATE TABLE server_scans (
  server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  at        INTEGER NOT NULL,
  data      TEXT    NOT NULL
);
ALTER TABLE nodes ADD COLUMN imported TEXT NOT NULL DEFAULT '';
`,
	// 8: servers whose provider decides their ports (NAT servers, LXC and Incus containers)
	`
ALTER TABLE servers ADD COLUMN public_ports TEXT NOT NULL DEFAULT '';
`,
	// 9: protocols that serve only proxy passes; a protocol's own address; a server's IP version and
	// the addresses on its interfaces
	`
ALTER TABLE nodes ADD COLUMN pass_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN bind_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN ip_version TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN addrs TEXT NOT NULL DEFAULT '';
-- configuration as code: the operator's own Xray JSON (per server) and Hysteria2 YAML (per protocol),
-- merged on top of what the panel generates
ALTER TABLE servers ADD COLUMN xray_code TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN code TEXT NOT NULL DEFAULT '';

-- shared certificates: kept once, used by protocols on any server, updated once for all of them
CREATE TABLE certs (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  cert_pem   TEXT    NOT NULL,
  key_pem    TEXT    NOT NULL,
  domains    TEXT    NOT NULL DEFAULT '[]',
  not_before INTEGER NOT NULL DEFAULT 0,
  not_after  INTEGER NOT NULL DEFAULT 0,
  sha256     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX certs_account ON certs(account_id);
`,
	// 10: protocol and user ids are never handed out again: a user's access, a proxy pass or a
	// device's traffic that named a removed protocol (or user) must not come to mean a new one.
	// SQLite reuses the highest id once its row is gone, so the highest removed id is kept here and
	// new rows take their id above it (see NextID).
	`
CREATE TABLE id_floor (name TEXT PRIMARY KEY, last INTEGER NOT NULL);
CREATE TRIGGER nodes_id_floor AFTER DELETE ON nodes BEGIN
  INSERT INTO id_floor (name, last) VALUES ('nodes', old.id)
    ON CONFLICT(name) DO UPDATE SET last = MAX(last, excluded.last);
END;
CREATE TRIGGER subs_id_floor AFTER DELETE ON subs BEGIN
  INSERT INTO id_floor (name, last) VALUES ('subs', old.id)
    ON CONFLICT(name) DO UPDATE SET last = MAX(last, excluded.last);
END;
`,
	// 11: each user's usage per protocol, exact: this cycle (reset with the user's cycle) and all time.
	// A removed protocol keeps its row (its id is never reused); traffic of a protocol removed while
	// the server reported it is kept under -server id. The totals so far come from the daily rows;
	// the panel fills in this cycle's once (it knows the time zone).
	`
CREATE TABLE sub_node_usage (
  sub_id     INTEGER NOT NULL REFERENCES subs(id) ON DELETE CASCADE,
  node_id    INTEGER NOT NULL,
  server_id  INTEGER NOT NULL DEFAULT 0,
  cycle_up   INTEGER NOT NULL DEFAULT 0,
  cycle_down INTEGER NOT NULL DEFAULT 0,
  total_up   INTEGER NOT NULL DEFAULT 0,
  total_down INTEGER NOT NULL DEFAULT 0,
  last_at    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (sub_id, node_id)
);
INSERT INTO sub_node_usage (sub_id, node_id, server_id, total_up, total_down)
  SELECT t.sub_id, t.node_id, MAX(t.server_id), SUM(t.up), SUM(t.down) FROM traffic_daily t
  JOIN subs s ON s.id = t.sub_id GROUP BY t.sub_id, t.node_id;
INSERT INTO settings (key, value) VALUES ('node_usage_cycle', 'pending')
  ON CONFLICT(key) DO UPDATE SET value = 'pending';
`,
	// 12: the key a server's proxy-pass credentials come from. It is the agent token's secret, but a
	// rotated token takes it over only when the reinstalled agent first connects: until then the old
	// agent's passes keep working on their exits (which would otherwise switch at once).
	`
ALTER TABLE servers ADD COLUMN pass_secret TEXT NOT NULL DEFAULT '';
UPDATE servers SET pass_secret = secret;
`,
	// 13: exits that are not servers - proxies elsewhere, imported from their links - and traffic
	// splitting: rules that send matching traffic directly, through a proxy pass, to a load balancer
	// (several exits taking turns) or nowhere. A protocol may pass through an external node.
	`
CREATE TABLE ext_nodes (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  endpoint   TEXT    NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  note       TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX ext_nodes_account ON ext_nodes(account_id);
ALTER TABLE nodes ADD COLUMN pass_ext INTEGER NOT NULL DEFAULT 0;
CREATE TABLE balancers (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  strategy   TEXT    NOT NULL DEFAULT 'random',
  members    TEXT    NOT NULL DEFAULT '[]',
  fallback   TEXT    NOT NULL DEFAULT 'block',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE routes (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  sort       INTEGER NOT NULL DEFAULT 0,
  name       TEXT    NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  servers    TEXT    NOT NULL DEFAULT '[]',
  nodes      TEXT    NOT NULL DEFAULT '[]',
  match      TEXT    NOT NULL DEFAULT '{}',
  target     TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX routes_account ON routes(account_id, sort);
`,
	// 14: a server whose agent keeps losing the panel reaches it through another server the operator
	// chooses (panel_relay; 0 = directly); a relay listens for them on relay_port (0 = none picked yet)
	`
ALTER TABLE servers ADD COLUMN panel_relay INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN relay_port INTEGER NOT NULL DEFAULT 0;
`,
	// 15: health checks: what the agents found that looks like a break-in or abuse (risks), what the
	// operator decided about each, keys expected on every server, and how far each server's check got
	`
CREATE TABLE risks (
  id          INTEGER PRIMARY KEY,
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  key         TEXT    NOT NULL,
  kind        TEXT    NOT NULL,
  severity    TEXT    NOT NULL,
  title       TEXT    NOT NULL,
  detail      TEXT    NOT NULL DEFAULT '',
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  count       INTEGER NOT NULL DEFAULT 1,
  active      INTEGER NOT NULL DEFAULT 0, -- it still holds (a process runs, a port is open)
  status      TEXT    NOT NULL DEFAULT 'open', -- open | acknowledged | expected
  decided_by  TEXT    NOT NULL DEFAULT '',
  decided_at  INTEGER NOT NULL DEFAULT 0,
  notified_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE (server_id, key)
);
CREATE INDEX risks_account ON risks(account_id, status);
CREATE TABLE risk_rules (
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  key        TEXT    NOT NULL,
  decided_by TEXT    NOT NULL DEFAULT '',
  decided_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, key)
);
CREATE TABLE server_health (
  server_id   INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  health_id   TEXT    NOT NULL DEFAULT '',
  seq         INTEGER NOT NULL DEFAULT 0,
  baseline_at INTEGER NOT NULL DEFAULT 0,
  scanned_at  INTEGER NOT NULL DEFAULT 0
);
`,
	// 16: plugins - the operator's own additions (internal/panel/plugins.go). Their files live in the
	// data directory (plugins/<id>); this keeps whether each is on, what the supervisor agreed to
	// when turning it on, and its last problem.
	`
CREATE TABLE plugins (
  id           TEXT    PRIMARY KEY,
  enabled      INTEGER NOT NULL DEFAULT 0,
  granted      TEXT    NOT NULL DEFAULT '[]',
  version      TEXT    NOT NULL DEFAULT '',
  sha256       TEXT    NOT NULL DEFAULT '',
  last_error   TEXT    NOT NULL DEFAULT '',
  installed_at INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
`,
	// 17: servers whose IP address changes (dynamic DNS): their address is a domain name the panel
	// checks against the addresses the agent reports, and may keep up to date in Cloudflare
	`
ALTER TABLE servers ADD COLUMN ddns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN ddns_cloudflare INTEGER NOT NULL DEFAULT 0;
`,
	// 18: what counts toward a user's quota (count_mode: both, down, up or max), when their period
	// started (starts_at; 0 = when they were created), usage reset every N days from then
	// (reset_every; 0 = by reset_day), a speed limit (Mbps; 0 = none), what happens to devices over
	// the limit (device_mode: '' = an alert only, refuse = new devices are turned away) - and preset
	// plans that fill all of that in
	`
ALTER TABLE subs ADD COLUMN count_mode TEXT NOT NULL DEFAULT 'both';
ALTER TABLE subs ADD COLUMN starts_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subs ADD COLUMN reset_every INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subs ADD COLUMN speed_limit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subs ADD COLUMN device_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE subs ADD COLUMN plan_id INTEGER NOT NULL DEFAULT 0;
CREATE TABLE plans (
  id            INTEGER PRIMARY KEY,
  account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  note          TEXT    NOT NULL DEFAULT '',
  quota         INTEGER NOT NULL DEFAULT 0,
  count_mode    TEXT    NOT NULL DEFAULT 'both',
  duration      INTEGER NOT NULL DEFAULT 0,
  duration_unit TEXT    NOT NULL DEFAULT 'day',
  reset_day     INTEGER NOT NULL DEFAULT 0,
  reset_every   INTEGER NOT NULL DEFAULT 0,
  ip_limit      INTEGER NOT NULL DEFAULT 0,
  device_mode   TEXT    NOT NULL DEFAULT '',
  speed_limit   INTEGER NOT NULL DEFAULT 0,
  scope         TEXT    NOT NULL DEFAULT '',
  price         REAL    NOT NULL DEFAULT 0,
  currency      TEXT    NOT NULL DEFAULT '',
  sort          INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX plans_account ON plans(account_id, sort);
`,
	// 19: providers' subscription links the panel reads again on a schedule (internal/panel/
	// extsources.go). Their nodes are external nodes with source_id set, matched on each refresh by
	// the provider's name for them (source_key); a node that left the subscription while something
	// still sends traffic to it is kept, marked missing_since. offer gives the nodes to users too.
	`
CREATE TABLE ext_sources (
  id          INTEGER PRIMARY KEY,
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name        TEXT    NOT NULL,
  url         TEXT    NOT NULL,
  client      TEXT    NOT NULL DEFAULT '',
  every_hours INTEGER NOT NULL DEFAULT 12,
  enabled     INTEGER NOT NULL DEFAULT 1,
  offer       INTEGER NOT NULL DEFAULT 0,
  offer_to    TEXT    NOT NULL DEFAULT '',
  prefix      TEXT    NOT NULL DEFAULT '',
  include     TEXT    NOT NULL DEFAULT '',
  exclude     TEXT    NOT NULL DEFAULT '',
  note        TEXT    NOT NULL DEFAULT '',
  fetched_at  INTEGER NOT NULL DEFAULT 0,
  ok_at       INTEGER NOT NULL DEFAULT 0,
  error       TEXT    NOT NULL DEFAULT '',
  skipped     TEXT    NOT NULL DEFAULT '',
  usage       TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX ext_sources_account ON ext_sources(account_id);
ALTER TABLE ext_nodes ADD COLUMN source_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ext_nodes ADD COLUMN source_key TEXT NOT NULL DEFAULT '';
ALTER TABLE ext_nodes ADD COLUMN missing_since INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ext_nodes_source ON ext_nodes(source_id);
`,
	// 20: limits per protocol, for a user and in a plan: bytes per cycle per protocol id (JSON), and
	// what happens when one is used up ('' = an alert only, stop = that protocol stops serving the
	// user until the cycle starts over)
	`
ALTER TABLE subs ADD COLUMN node_quotas TEXT NOT NULL DEFAULT '';
ALTER TABLE subs ADD COLUMN node_quota_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN node_quotas TEXT NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN node_quota_mode TEXT NOT NULL DEFAULT '';
`,
	// 21: servers another panel shares with this one (guest = 1): this panel runs its own protocols
	// there, but the console, the agent's and the cores' upgrades and relaying are the owner's
	`
ALTER TABLE servers ADD COLUMN guest INTEGER NOT NULL DEFAULT 0;
`,
	// 22: a server's details over time (the status page's charts): more of each minute's sample, and
	// five-minute averages kept a month; ping monitors - addresses the servers measure the way to at
	// fixed intervals - with their rounds, raw for two days and in five-minute steps for a month
	`
ALTER TABLE servers ADD COLUMN virt TEXT NOT NULL DEFAULT '';
ALTER TABLE server_metrics ADD COLUMN swap_used INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN load5 REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN load15 REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN udp INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN disk_read INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN disk_write INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN temp REAL NOT NULL DEFAULT 0;
CREATE TABLE server_metrics_5m (
  server_id  INTEGER NOT NULL,
  ts         INTEGER NOT NULL,
  cpu        REAL    NOT NULL DEFAULT 0,
  mem_used   INTEGER NOT NULL DEFAULT 0,
  swap_used  INTEGER NOT NULL DEFAULT 0,
  disk_used  INTEGER NOT NULL DEFAULT 0,
  load1      REAL    NOT NULL DEFAULT 0,
  load5      REAL    NOT NULL DEFAULT 0,
  load15     REAL    NOT NULL DEFAULT 0,
  rx_rate    INTEGER NOT NULL DEFAULT 0,
  tx_rate    INTEGER NOT NULL DEFAULT 0,
  disk_read  INTEGER NOT NULL DEFAULT 0,
  disk_write INTEGER NOT NULL DEFAULT 0,
  tcp        INTEGER NOT NULL DEFAULT 0,
  udp        INTEGER NOT NULL DEFAULT 0,
  online     INTEGER NOT NULL DEFAULT 0,
  temp       REAL    NOT NULL DEFAULT 0,
  PRIMARY KEY (server_id, ts)
);
CREATE TABLE ping_monitors (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  target     TEXT    NOT NULL,
  kind       TEXT    NOT NULL DEFAULT 'icmp',
  port       INTEGER NOT NULL DEFAULT 0,
  every_secs INTEGER NOT NULL DEFAULT 60,
  servers    TEXT    NOT NULL DEFAULT '',
  public     INTEGER NOT NULL DEFAULT 1,
  enabled    INTEGER NOT NULL DEFAULT 1,
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX ping_monitors_account ON ping_monitors(account_id);
CREATE TABLE ping_samples (
  monitor_id INTEGER NOT NULL,
  server_id  INTEGER NOT NULL,
  ts         INTEGER NOT NULL,
  sent       INTEGER NOT NULL DEFAULT 0,
  lost       INTEGER NOT NULL DEFAULT 0,
  avg_ms     REAL    NOT NULL DEFAULT 0,
  min_ms     REAL    NOT NULL DEFAULT 0,
  max_ms     REAL    NOT NULL DEFAULT 0,
  PRIMARY KEY (monitor_id, server_id, ts)
);
CREATE INDEX ping_samples_server ON ping_samples(server_id, ts);
CREATE TABLE ping_5m (
  monitor_id INTEGER NOT NULL,
  server_id  INTEGER NOT NULL,
  ts         INTEGER NOT NULL,
  sent       INTEGER NOT NULL DEFAULT 0,
  lost       INTEGER NOT NULL DEFAULT 0,
  avg_ms     REAL    NOT NULL DEFAULT 0,
  min_ms     REAL    NOT NULL DEFAULT 0,
  max_ms     REAL    NOT NULL DEFAULT 0,
  PRIMARY KEY (monitor_id, server_id, ts)
);
`,
	// 23: Telegram accounts linked to a user (up to two each) or to the supervisor, so the bot and its
	// Mini App know who writes; the users' own unlinks (one a month); and how a session was made
	// (via = 'telegram': signed in from the Mini App, which cannot change passwords, tokens or the
	// site rule).
	`
CREATE TABLE tg_links (
  id           INTEGER PRIMARY KEY,
  tg_id        INTEGER NOT NULL UNIQUE,
  tg_name      TEXT    NOT NULL DEFAULT '',
  sub_id       INTEGER REFERENCES subs(id) ON DELETE CASCADE,
  account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX tg_links_sub ON tg_links(sub_id);
CREATE INDEX tg_links_account ON tg_links(account_id);
CREATE TABLE tg_unlinks (
  sub_id INTEGER NOT NULL REFERENCES subs(id) ON DELETE CASCADE,
  at     INTEGER NOT NULL
);
CREATE INDEX tg_unlinks_sub ON tg_unlinks(sub_id);
ALTER TABLE sessions ADD COLUMN via TEXT NOT NULL DEFAULT '';
ALTER TABLE user_sessions ADD COLUMN via TEXT NOT NULL DEFAULT '';
`,
	// 24: passkeys - the panel's accounts sign in with a passkey (WebAuthn) instead of a password and a
	// code. A passkey carries a random handle for its account (webauthn_id), never the account's id;
	// credential is the passkey's public key and counters as the WebAuthn library keeps them (JSON).
	`
CREATE TABLE passkeys (
  id            INTEGER PRIMARY KEY,
  account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  credential_id TEXT    NOT NULL UNIQUE,
  credential    TEXT    NOT NULL,
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER NOT NULL DEFAULT 0,
  last_used_ip  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX passkeys_account ON passkeys(account_id);
ALTER TABLE accounts ADD COLUMN webauthn_id TEXT NOT NULL DEFAULT '';
`,
}

// NextID is an SQL expression for the id of a new row of nodes or subs: above every id the table
// has, or ever had (migration 10). Use it as the id value in the INSERT.
func NextID(table string) string {
	if table != "nodes" && table != "subs" {
		panic("NextID: " + table)
	}
	return "(SELECT MAX(v) + 1 FROM (SELECT COALESCE(MAX(id), 0) AS v FROM " + table + " UNION ALL SELECT COALESCE(MAX(last), 0) FROM id_floor WHERE name = '" +
		table + "') floor_ids)"
}
