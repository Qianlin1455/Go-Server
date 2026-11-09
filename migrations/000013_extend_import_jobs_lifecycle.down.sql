
-- 1. 恢复原来的单列 Repository 外键。
ALTER TABLE import_jobs
    DROP FOREIGN KEY fk_import_jobs_repository_project,
    DROP INDEX idx_import_jobs_repository_project,
    ADD CONSTRAINT fk_import_jobs_repository
        FOREIGN KEY (repository_id)
        REFERENCES repositories (id)
        ON DELETE RESTRICT;

-- 2. 删除新增的生命周期约束、索引和字段。
ALTER TABLE import_jobs
    DROP CHECK chk_import_jobs_attempts,
    DROP CHECK chk_import_jobs_attempt_timestamps,
    DROP CHECK chk_import_jobs_lease_state,
    DROP CHECK chk_import_jobs_finished_at,

    DROP INDEX idx_import_jobs_claim,
    DROP INDEX idx_import_jobs_expired_lease,

    DROP COLUMN attempt_count,
    DROP COLUMN max_attempts,
    DROP COLUMN available_at,
    DROP COLUMN started_at,
    DROP COLUMN last_attempt_at,
    DROP COLUMN finished_at,
    DROP COLUMN lease_owner,
    DROP COLUMN lease_token,
    DROP COLUMN lease_expires_at,
    DROP COLUMN last_error_code,
    DROP COLUMN last_error_message;

-- 3. 删除 Repository 复合唯一索引。
ALTER TABLE repositories
DROP INDEX uk_repositories_id_project_id;
