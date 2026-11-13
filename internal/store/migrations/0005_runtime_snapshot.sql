-- 0005_runtime_snapshot.sql
-- 本地持久化各主机容器运行态快照，用于首次进入「容器」界面时秒级渲染并在后台静默刷新

CREATE TABLE IF NOT EXISTS host_runtime_snapshot (
  host_id    TEXT PRIMARY KEY REFERENCES host(id) ON DELETE CASCADE,
  trees_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
