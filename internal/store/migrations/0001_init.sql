-- 第一阶段初始 schema，详见 docs/实施文档-第一阶段.md §9

CREATE TABLE host (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL,
  kind                 TEXT NOT NULL,          -- docker | synology
  transport            TEXT NOT NULL,          -- local | ssh
  address              TEXT,
  port                 INTEGER,
  ssh_user             TEXT,
  ssh_auth             TEXT,                   -- password | key
  ssh_secret_ref       TEXT,
  ssh_host_key         TEXT,
  transport_mode       TEXT,                   -- streamlocal | dial_stdio | sudo_dial_stdio
  dsm_url              TEXT,
  dsm_user             TEXT,
  dsm_secret_ref       TEXT,
  dsm_device_token_ref TEXT,
  info_json            TEXT,
  status               TEXT,
  last_seen_at         INTEGER,
  sort                 INTEGER NOT NULL DEFAULT 0,
  created_at           INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE secret (
  id     TEXT PRIMARY KEY,
  nonce  BLOB NOT NULL,
  cipher BLOB NOT NULL
);

CREATE TABLE app (
  key          TEXT PRIMARY KEY,
  host_id      TEXT NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  source       TEXT NOT NULL,                  -- standalone | compose | dsm_project | vendor_managed
  project_json TEXT,
  icon         TEXT,
  gone_at      INTEGER,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX ix_app_host ON app(host_id);

CREATE TABLE service (
  key               TEXT PRIMARY KEY,
  app_key           TEXT NOT NULL REFERENCES app(key) ON DELETE CASCADE,
  name              TEXT NOT NULL,
  image_ref         TEXT,
  image_id          TEXT,
  repo_digest       TEXT,
  platform          TEXT,
  state             TEXT,
  current_tags_json TEXT,
  gone_at           INTEGER,
  updated_at        INTEGER NOT NULL
);
CREATE INDEX ix_service_app ON service(app_key);

CREATE TABLE finding (
  service_key      TEXT PRIMARY KEY,
  kind             TEXT NOT NULL,              -- none | digest_update | version_update | check_failed | uncomparable
  category         TEXT,                       -- normal | caution
  reason           TEXT,
  current_version  TEXT,
  target_version   TEXT,
  target_ref       TEXT,
  target_digest    TEXT,
  target_tags_json TEXT,
  hint             TEXT,
  error_kind       TEXT,
  error_msg        TEXT,
  links_json       TEXT,
  first_seen_at    INTEGER,
  checked_at       INTEGER,
  notified_at      INTEGER
);

CREATE TABLE finding_history (
  id           INTEGER PRIMARY KEY,
  check_run_id INTEGER NOT NULL,
  service_key  TEXT NOT NULL,
  change       TEXT NOT NULL,                  -- appeared | changed | resolved
  snapshot     TEXT NOT NULL,
  created_at   INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX ix_finding_history_run ON finding_history(check_run_id);

CREATE TABLE check_run (
  id           INTEGER PRIMARY KEY,
  trigger      TEXT NOT NULL,                  -- manual | schedule
  started_at   INTEGER NOT NULL,
  finished_at  INTEGER,
  status       TEXT NOT NULL,                  -- running | done | failed
  summary_json TEXT,
  log          TEXT
);

CREATE TABLE ignore_rule (
  id            INTEGER PRIMARY KEY,
  service_key   TEXT NOT NULL,
  mode          TEXT NOT NULL,                 -- this_version | forever
  target_digest TEXT,
  target_tag    TEXT,
  note          TEXT,
  created_at    INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE UNIQUE INDEX ux_ignore_service ON ignore_rule(service_key);

CREATE TABLE registry_cache (
  k          TEXT PRIMARY KEY,
  v          BLOB NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE digest_tag_cache (
  repo      TEXT NOT NULL,
  digest    TEXT NOT NULL,
  tags_json TEXT NOT NULL,
  PRIMARY KEY (repo, digest)
);

CREATE TABLE registry_credential (
  registry   TEXT PRIMARY KEY,
  username   TEXT NOT NULL,
  secret_ref TEXT NOT NULL
);

CREATE TABLE setting (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
