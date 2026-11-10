-- 0004_labels_and_prefix_ignore.sql
-- 持久化服务 OCI/容器标签元数据（版本标签、图标、源码仓库）与多段版本号前缀锁定规则（附录 D.1 / D.3）

ALTER TABLE service ADD COLUMN labels_json TEXT;
ALTER TABLE ignore_rule ADD COLUMN prefix TEXT;
