
ALTER TABLE import_jobs
    ADD COLUMN result_tree_sha CHAR(40)
        CHARACTER SET ascii COLLATE ascii_bin
        NULL AFTER revision_sha,

    ADD COLUMN result_file_count INT UNSIGNED
        NULL AFTER result_tree_sha,

    ADD COLUMN result_total_bytes BIGINT UNSIGNED
        NULL AFTER result_file_count,

    ADD CONSTRAINT chk_import_jobs_result_fields
        CHECK (
            (
                result_tree_sha IS NULL
                AND result_file_count IS NULL
                AND result_total_bytes IS NULL
            )
            OR
            (
                result_tree_sha IS NOT NULL
                AND result_file_count IS NOT NULL
                AND result_total_bytes IS NOT NULL
            )
        );
