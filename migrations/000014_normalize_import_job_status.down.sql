
-- 1. 删除新 CHECK 约束
ALTER TABLE import_jobs
    DROP CHECK chk_import_jobs_status,
    DROP CHECK chk_import_jobs_finished_at;

-- 2. 恢复旧状态
UPDATE import_jobs
SET status = 'pending'
WHERE status = 'queued';

-- 3. 恢复旧默认值和约束
ALTER TABLE import_jobs
    MODIFY COLUMN status VARCHAR(16)
        NOT NULL DEFAULT 'pending',

    ADD CONSTRAINT chk_import_jobs_status
        CHECK (
            status IN (
                'pending',
                'running',
                'succeeded',
                'failed'
            )
        ),

    ADD CONSTRAINT chk_import_jobs_finished_at
        CHECK (
            (status IN ('succeeded', 'failed')
                AND finished_at IS NOT NULL)
            OR
            (status IN ('pending', 'running')
                AND finished_at IS NULL)
        );
