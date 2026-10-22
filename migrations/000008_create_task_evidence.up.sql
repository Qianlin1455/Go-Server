ALTER TABLE tasks
ADD UNIQUE KEY uk_tasks_id_user (id, user_id);

CREATE TABLE task_evidence (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    task_id BIGINT UNSIGNED NOT NULL,
    created_by BIGINT UNSIGNED NOT NULL,

    type VARCHAR(32) NOT NULL,
    text TEXT NULL,
    commit_sha VARCHAR(64) NULL,

    status VARCHAR(16) NOT NULL DEFAULT 'draft',

    created_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3),

    PRIMARY KEY (id),

    KEY idx_task_evidence_task_status_id
        (task_id, status, id),

    CONSTRAINT fk_task_evidence_task
        FOREIGN KEY (task_id)
        REFERENCES tasks(id)
        ON DELETE RESTRICT,

    CONSTRAINT fk_task_evidence_creator
        FOREIGN KEY (created_by)
        REFERENCES users(id)
        ON DELETE RESTRICT,

    CONSTRAINT fk_task_evidence_owner
        FOREIGN KEY (task_id, created_by)
        REFERENCES tasks(id, user_id)
        ON DELETE RESTRICT,

    CONSTRAINT chk_task_evidence_type
        CHECK (type IN (
            'experiment_text',
            'test_output',
            'commit_sha'
        )),

    CONSTRAINT chk_task_evidence_status
        CHECK (status IN (
            'draft',
            'submitted'
        ))

) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
