-- 0007_host_metrics_snapshot.sql
-- 本地持久化各设备系统资源快照（CPU、内存与存储空间），用于进入「设备管理」页时秒开渲染并在后台刷新

CREATE TABLE IF NOT EXISTS host_metrics_snapshot (
  host_id      TEXT PRIMARY KEY REFERENCES host(id) ON DELETE CASCADE,
  metrics_json TEXT NOT NULL,
  updated_at   INTEGER NOT NULL
);
