
CREATE TABLE outbox_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,

    event_id CHAR(36)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    job_id BIGINT UNSIGNED NOT NULL,

    event_type VARCHAR(64)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    revision_sha CHAR(40)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    schema_version INT UNSIGNED NOT NULL DEFAULT 1,

    payload JSON NOT NULL,

    status VARCHAR(16) NOT NULL DEFAULT 'pending',

    attempts INT UNSIGNED NOT NULL DEFAULT 0,

    last_error VARCHAR(512) NULL,

    created_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3),

    sent_at DATETIME(3) NULL,

    PRIMARY KEY (id),

    UNIQUE KEY uk_outbox_event_id (event_id),

    UNIQUE KEY uk_outbox_job_type (
        job_id, event_type
    ),

    KEY idx_outbox_status_id (
        status, id
    ),

    CONSTRAINT fk_outbox_import_job
        FOREIGN KEY (job_id)
        REFERENCES import_jobs(id)
        ON DELETE RESTRICT,

    CONSTRAINT chk_outbox_status
        CHECK (status IN ('pending', 'sent')),

    CONSTRAINT chk_outbox_schema_version
        CHECK (schema_version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
