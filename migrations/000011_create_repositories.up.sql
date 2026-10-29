CREATE TABLE repositories (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    project_id BIGINT UNSIGNED NOT NULL,

    -- GitHub 仓库标识
    owner VARCHAR(39)
        CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL,
    repo VARCHAR(100)
        CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL,

    -- GitHub 仓库元信息
    default_branch VARCHAR(255) NOT NULL,
    last_sha CHAR(40)
        CHARACTER SET ascii COLLATE ascii_bin NOT NULL,

    -- 时间戳
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL
        DEFAULT CURRENT_TIMESTAMP(3)
        ON UPDATE CURRENT_TIMESTAMP(3),

    PRIMARY KEY (id),

    -- 同一项目不能重复绑定同一仓库
    UNIQUE KEY uk_repositories_project_owner_repo
        (project_id, owner, repo),

    -- 支持按项目查询仓库列表和稳定分页
    KEY idx_repositories_project_id_id (project_id, id),

    -- 项目删除时，自动删除对应的仓库绑定记录
    CONSTRAINT fk_repositories_project
        FOREIGN KEY (project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
        ON UPDATE RESTRICT

) ENGINE=InnoDB
  DEFAULT CHARSET=utf8mb4;