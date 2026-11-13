-- 0006_resolved_version_tags.sql
-- 结构化保存检测阶段与升级任务中真实识别到的精确版本标签，避免从 UI 展示文本二次截取

ALTER TABLE finding ADD COLUMN current_resolved_tag TEXT;
ALTER TABLE finding ADD COLUMN target_resolved_tag TEXT;

ALTER TABLE update_job ADD COLUMN old_resolved_tag TEXT;
ALTER TABLE update_job ADD COLUMN target_resolved_tag TEXT;
