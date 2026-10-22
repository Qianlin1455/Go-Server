CREATE TABLE task_reviews (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    task_id BIGINT UNSIGNED NOT NULL,

    reviewer_type VARCHAR(16) NOT NULL,
    reviewer_user_id BIGINT UNSIGNED NULL,

    decision VARCHAR(32) NOT NULL,
    feedback TEXT NULL,

    created_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3),

    PRIMARY KEY (id),

    -- 按任务查询审核历史
    KEY idx_task_reviews_task_id
        (task_id, id),

    -- 任务外键
    CONSTRAINT fk_task_reviews_task
        FOREIGN KEY (task_id)
        REFERENCES tasks(id)
        ON DELETE RESTRICT,

    -- 人工审核者外键
    CONSTRAINT fk_task_reviews_reviewer
        FOREIGN KEY (reviewer_user_id)
        REFERENCES users(id)
        ON DELETE RESTRICT,

    -- 审核者类型约束
    CONSTRAINT chk_task_reviews_reviewer_type
        CHECK (
            reviewer_type IN ('human', 'ai')
        ),

    -- 审核结论约束
    CONSTRAINT chk_task_reviews_decision
        CHECK (
            decision IN (
                'approved',
                'rejected',
                'suggested_pass',
                'suggested_fail'
            )
        ),

    -- 审核者身份与结论必须匹配
    CONSTRAINT chk_task_reviews_consistency
        CHECK (
            (
                reviewer_type = 'human'
                AND reviewer_user_id IS NOT NULL
                AND decision IN ('approved', 'rejected')
            )
            OR
            (
                reviewer_type = 'ai'
                AND reviewer_user_id IS NULL
                AND decision IN (
                    'suggested_pass',
                    'suggested_fail'
                )
            )
        )

) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;