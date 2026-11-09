
-- 1. 为 Repository 增加复合唯一索引。
-- 用于保证 ImportJob 引用的 Repository 属于同一个 Project。
ALTER TABLE repositories
ADD UNIQUE KEY uk_repositories_id_project_id (
    id,
    project_id
);

-- 2. 将 Repository 外键升级为复合外键。
-- 旧的单列外键被替换。
ALTER TABLE import_jobs
    DROP FOREIGN KEY fk_import_jobs_repository,
    ADD KEY idx_import_jobs_repository_project (
        repository_id,
        project_id
    ),
    ADD CONSTRAINT fk_import_jobs_repository_project
        FOREIGN KEY (repository_id, project_id)
        REFERENCES repositories (id, project_id)
        ON DELETE RESTRICT;

-- 3. 扩展 Worker 执行生命周期。
ALTER TABLE import_jobs
    ADD COLUMN attempt_count SMALLINT UNSIGNED
        NOT NULL DEFAULT 0 AFTER status,

    ADD COLUMN max_attempts SMALLINT UNSIGNED
        NOT NULL DEFAULT 3 AFTER attempt_count,

    ADD COLUMN available_at DATETIME(3)
        NOT NULL DEFAULT CURRENT_TIMESTAMP(3) AFTER max_attempts,

    ADD COLUMN started_at DATETIME(3)
        NULL AFTER available_at,

    ADD COLUMN last_attempt_at DATETIME(3)
        NULL AFTER started_at,

    ADD COLUMN finished_at DATETIME(3)
        NULL AFTER last_attempt_at,

    ADD COLUMN lease_owner VARCHAR(128)
        CHARACTER SET ascii COLLATE ascii_bin
        NULL AFTER finished_at,

    ADD COLUMN lease_token BINARY(16)
        NULL AFTER lease_owner,

    ADD COLUMN lease_expires_at DATETIME(3)
        NULL AFTER lease_token,

    ADD COLUMN last_error_code VARCHAR(64)
        CHARACTER SET ascii COLLATE ascii_bin
        NULL AFTER lease_expires_at,

    ADD COLUMN last_error_message VARCHAR(1024)
        NULL AFTER last_error_code,

    -- Worker 领取任务的查询索引。
    ADD KEY idx_import_jobs_claim (
        status,
        available_at,
        id
    ),

    -- 扫描过期租约的查询索引。
    ADD KEY idx_import_jobs_expired_lease (
        status,
        lease_expires_at,
        id
    ),

    -- 最大尝试次数必须大于 0。
    ADD CONSTRAINT chk_import_jobs_attempts
        CHECK (
            max_attempts > 0
            AND attempt_count <= max_attempts
        ),

    -- 尝试次数和执行时间必须一致。
    ADD CONSTRAINT chk_import_jobs_attempt_timestamps
        CHECK (
            (
                attempt_count = 0
                AND started_at IS NULL
                AND last_attempt_at IS NULL
            )
            OR
            (
                attempt_count > 0
                AND started_at IS NOT NULL
                AND last_attempt_at IS NOT NULL
            )
        ),

    -- 只有 running 状态允许持有租约。
    ADD CONSTRAINT chk_import_jobs_lease_state
        CHECK (
            (
                status = 'running'
                AND lease_owner IS NOT NULL
                AND lease_token IS NOT NULL
                AND lease_expires_at IS NOT NULL
            )
            OR
            (
                status <> 'running'
                AND lease_owner IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
            )
        ),

    -- 终态必须具有完成时间。
    ADD CONSTRAINT chk_import_jobs_finished_at
        CHECK (
            (
                status IN ('succeeded', 'failed')
                AND finished_at IS NOT NULL
            )
            OR
            (
                status IN ('pending', 'running')
                AND finished_at IS NULL
            )
        );
