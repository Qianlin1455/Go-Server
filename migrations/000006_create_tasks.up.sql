CREATE TABLE tasks (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    source_message_id BIGINT UNSIGNED NULL,

    title VARCHAR(200) NOT NULL,
    description TEXT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'todo',
    version INT UNSIGNED NOT NULL DEFAULT 1,
    completed_at DATETIME(3) NULL,

    created_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    PRIMARY KEY (id),

    KEY idx_tasks_project_user_status_id
        (project_id, user_id, status, id),

    KEY idx_tasks_source_message_id (source_message_id),

    CONSTRAINT fk_tasks_project_user
        FOREIGN KEY (project_id, user_id)
        REFERENCES projects(id, user_id)
        ON DELETE RESTRICT,

    CONSTRAINT fk_tasks_source_message
        FOREIGN KEY (source_message_id)
        REFERENCES messages(id)
        ON DELETE SET NULL,

    CONSTRAINT chk_tasks_status
        CHECK (status IN ('todo', 'doing', 'done'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;