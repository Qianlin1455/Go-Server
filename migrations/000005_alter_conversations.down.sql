ALTER TABLE conversations
    DROP FOREIGN KEY fk_conversations_project_user,
    DROP INDEX idx_conversations_project_user,
    DROP COLUMN project_id;