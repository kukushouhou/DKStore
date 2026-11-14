-- 0008_container_meta.sql
-- 持久化服务对外端口映射与项目/容器本地编辑信息（备注名、标签、访问入口、备注）

ALTER TABLE service ADD COLUMN ports_json TEXT;

CREATE TABLE IF NOT EXISTS local_meta (
  target_key  TEXT PRIMARY KEY,
  host_id     TEXT NOT NULL REFERENCES host(id) ON DELETE CASCADE,
  alias       TEXT NOT NULL DEFAULT '',
  tags_json   TEXT NOT NULL DEFAULT '[]',
  access_mode TEXT NOT NULL DEFAULT 'auto',   -- auto | custom | none
  scheme      TEXT NOT NULL DEFAULT '',       -- '' | http | https
  fixed_port  TEXT NOT NULL DEFAULT '',
  custom_url  TEXT NOT NULL DEFAULT '',
  note        TEXT NOT NULL DEFAULT '',
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_local_meta_host ON local_meta(host_id);
