
-- 1. 删除需要修改的旧 CHECK 约束
ALTER TABLE import_jobs
    DROP CHECK chk_import_jobs_status,
    DROP CHECK chk_import_jobs_finished_at;

-- 2. 转换已有任务的数据
UPDATE import_jobs
SET status = 'queued'
WHERE status = 'pending';

-- 3. 修改默认状态并重新建立约束
ALTER TABLE import_jobs
    MODIFY COLUMN status VARCHAR(16)
        NOT NULL DEFAULT 'queued',

    ADD CONSTRAINT chk_import_jobs_status
        CHECK (
            status IN (
                'queued',
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
            (status IN ('queued', 'running')
                AND finished_at IS NULL)
        );
