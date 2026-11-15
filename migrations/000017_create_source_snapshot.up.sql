ALTER TABLE import_jobs
    ADD UNIQUE KEY uk_import_jobs_source_scope
        (id, user_id, project_id, repository_id, revision_sha);

CREATE TABLE source_files (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    import_job_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    project_id BIGINT UNSIGNED NOT NULL,
    repository_id BIGINT UNSIGNED NOT NULL,
    revision_sha CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    path VARBINARY(768) NOT NULL,
    blob_sha CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    content_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    language VARCHAR(32) CHARACTER SET ascii NOT NULL,
    size_bytes INT UNSIGNED NOT NULL,
    content MEDIUMTEXT NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_source_files_job_path (import_job_id, path),
    KEY idx_source_files_job_scope (import_job_id, user_id, project_id, repository_id, revision_sha),
    CONSTRAINT fk_source_files_job_scope
        FOREIGN KEY (import_job_id, user_id, project_id, repository_id, revision_sha)
        REFERENCES import_jobs (id, user_id, project_id, repository_id, revision_sha)
        ON DELETE CASCADE,
    CONSTRAINT chk_source_file_size CHECK (size_bytes = OCTET_LENGTH(content))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE source_spans (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    source_file_id BIGINT UNSIGNED NOT NULL,
    span_index INT UNSIGNED NOT NULL,
    line_start INT UNSIGNED NOT NULL,
    line_end INT UNSIGNED NOT NULL,
    content MEDIUMTEXT NOT NULL,
    content_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_source_spans_file_index (source_file_id, span_index),
    CONSTRAINT fk_source_spans_file FOREIGN KEY (source_file_id)
        REFERENCES source_files (id) ON DELETE CASCADE,
    CONSTRAINT chk_source_span_lines CHECK (line_start > 0 AND line_end >= line_start)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
