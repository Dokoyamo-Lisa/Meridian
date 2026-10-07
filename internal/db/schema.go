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
}
