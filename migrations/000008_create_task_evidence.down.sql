DROP TABLE IF EXISTS task_evidence;

ALTER TABLE tasks
DROP INDEX uk_tasks_id_user;