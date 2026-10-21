CREATE TABLE task_status_audits (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id BIGINT UNSIGNED NOT NULL,
    actor_user_id BIGINT UNSIGNED NOT NULL,

    from_status VARCHAR(16) NOT NULL,
    to_status VARCHAR(16) NOT NULL,

    from_version INT UNSIGNED NOT NULL,
    to_version INT UNSIGNED NOT NULL,

    created_at DATETIME(3) NOT NULL
      DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE KEY uk_task_audit_version
      (task_id, to_version),

    CONSTRAINT fk_task_audit_task
      FOREIGN KEY (task_id)
      REFERENCES tasks(id)
      ON DELETE RESTRICT,

    CONSTRAINT fk_task_audit_actor
      FOREIGN KEY (actor_user_id)
      REFERENCES users(id)
      ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;