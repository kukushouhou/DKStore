-- 0002_update_and_rollback.sql
-- 一键更新任务、分层下载进度、更新历史与有条件回退（立项文档 §7 / §8 / §9）

CREATE TABLE IF NOT EXISTS update_job (
    id                       TEXT PRIMARY KEY,
    kind                     TEXT NOT NULL DEFAULT 'upgrade', -- 'upgrade' | 'rollback'
    host_id                  TEXT NOT NULL,
    host_name                TEXT NOT NULL,
    app_key                  TEXT NOT NULL,
    app_name                 TEXT NOT NULL,
    service_key              TEXT NOT NULL,
    service_name             TEXT NOT NULL,
    source                   TEXT NOT NULL,                   -- 'standalone' | 'compose' | 'dsm_project' | 'vendor_managed'
    status                   TEXT NOT NULL DEFAULT 'queued',  -- 'queued' | 'running' | 'succeeded' | 'failed'
    phase                    TEXT NOT NULL DEFAULT 'precheck',-- 'precheck' | 'pulling' | 'recreating' | 'verifying' | 'done'
    progress_json            TEXT,                            -- 分层下载进度与当前阶段描述 JSON
    old_image_ref            TEXT NOT NULL,
    old_image_id             TEXT,
    old_repo_digest          TEXT,
    old_version              TEXT,
    old_matched_tags_json    TEXT,
    old_compose_path         TEXT,
    old_compose_sha256       TEXT,
    target_image_ref         TEXT NOT NULL,
    target_repo_digest       TEXT,
    target_version           TEXT,
    target_matched_tags_json TEXT,
    new_image_id             TEXT,
    new_repo_digest          TEXT,
    rollback_eligible        INTEGER NOT NULL DEFAULT 0,
    rollback_ref             TEXT,                            -- 可验证回退的目标精确标签镜像引用
    rollback_reason          TEXT,                            -- 不可回退原因码（如 no_exact_version_tag / tag_overwritten / vendor_managed）
    error_code               TEXT,
    error_msg                TEXT,
    logs_text                TEXT,
    started_at               INTEGER NOT NULL,
    finished_at              INTEGER
);

CREATE INDEX IF NOT EXISTS idx_update_job_started ON update_job(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_update_job_service ON update_job(service_key, started_at DESC);
