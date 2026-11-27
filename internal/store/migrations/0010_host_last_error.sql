-- 0010_host_last_error.sql
-- 记录设备最近一次连接或同步失败原因，恢复在线后自动清空

ALTER TABLE host ADD COLUMN last_error TEXT;
