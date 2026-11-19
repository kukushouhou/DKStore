-- 0009_host_mirrors.sql
-- 持久化各设备的 Docker 镜像源配对列表（源仓库 -> 加速网址列表）

CREATE TABLE IF NOT EXISTS host_mirror (
  host_id    TEXT PRIMARY KEY REFERENCES host(id) ON DELETE CASCADE,
  pairs_json TEXT NOT NULL DEFAULT '[]',
  updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);
