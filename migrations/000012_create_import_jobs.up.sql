CREATE TABLE import_jobs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    repository_id BIGINT UNSIGNED NOT NULL,

    endpoint VARCHAR(128)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    idempotency_key VARCHAR(128)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    request_fingerprint CHAR(64)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    revision_sha CHAR(40)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    status VARCHAR(16) NOT NULL DEFAULT 'pending',

    created_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3),

    updated_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    PRIMARY KEY (id),

    UNIQUE KEY uk_import_jobs_idempotency (
        user_id,
        endpoint,
        idempotency_key
    ),

    KEY idx_import_jobs_project_id_id (
        project_id, id
    ),

    CONSTRAINT fk_import_jobs_project_user
        FOREIGN KEY (project_id, user_id)
        REFERENCES projects(id, user_id)
        ON DELETE RESTRICT,

    CONSTRAINT fk_import_jobs_repository
        FOREIGN KEY (repository_id)
        REFERENCES repositories(id)
        ON DELETE RESTRICT,

    CONSTRAINT chk_import_jobs_status
        CHECK (
            status IN (
                'pending',
                'running',
                'succeeded',
                'failed'
            )
        )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;