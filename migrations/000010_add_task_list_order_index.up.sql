CREATE INDEX idx_tasks_project_user_created_id
ON tasks (
    project_id,
    user_id,
    created_at DESC,
    id DESC
);