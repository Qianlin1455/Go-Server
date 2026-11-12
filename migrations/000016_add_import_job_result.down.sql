
ALTER TABLE import_jobs
    DROP CHECK chk_import_jobs_result_fields,
    DROP COLUMN result_total_bytes,
    DROP COLUMN result_file_count,
    DROP COLUMN result_tree_sha;
