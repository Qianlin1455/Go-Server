package repository

import (
	"context"

	"GoServer/internal/model"
	"gorm.io/gorm"
)

type GitHubRepositoryStore struct {
	db *gorm.DB
}

func NewGitHubRepositoryStore(db *gorm.DB) *GitHubRepositoryStore {
	return &GitHubRepositoryStore{db: db}
}

// Create 保存仓库绑定记录。
// 调用前必须由 Service 校验项目归属和仓库有效性。
func (r *GitHubRepositoryStore) Create(
	ctx context.Context,
	repo *model.Repository,
) error {
	return r.db.WithContext(ctx).
		Create(repo).Error
}

// GetByProject 查询指定用户、项目下的仓库绑定。
func (r *GitHubRepositoryStore) GetByProject(
	ctx context.Context,
	userID, projectID, repoID uint64,
) (*model.Repository, error) {

	var repo model.Repository

	err := r.db.WithContext(ctx).
		Table("repositories AS r").
		Select("r.*").
		Joins(
			"JOIN projects AS p ON p.id = r.project_id",
		).
		Where(
			"p.user_id = ? AND r.project_id = ? AND r.id = ?",
			userID,
			projectID,
			repoID,
		).
		Take(&repo).Error

	if err != nil {
		return nil, err
	}

	return &repo, nil
}
