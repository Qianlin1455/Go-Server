ALTER TABLE conversations
    ADD COLUMN project_id BIGINT UNSIGNED NULL
        AFTER user_id,
    ADD KEY idx_conversations_project_user
        (project_id, user_id),
    ADD CONSTRAINT fk_conversations_project_user
        FOREIGN KEY (project_id, user_id)
        REFERENCES projects(id, user_id)
        ON DELETE RESTRICT;