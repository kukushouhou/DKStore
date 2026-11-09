-- 0003_host_fingerprint.sql
-- SSH 主机指纹家庭内网简化策略（实施文档 §3.1）：指纹变化时自动更新，并记录旧指纹与变化时间供设备卡片提示。

ALTER TABLE host ADD COLUMN ssh_prev_host_key TEXT;
ALTER TABLE host ADD COLUMN ssh_host_key_changed_at INTEGER;
