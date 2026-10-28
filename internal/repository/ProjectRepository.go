package repository

import (
	"context"

	"GoServer/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func CreateProject(
	ctx context.Context,
	db *gorm.DB,
	project *model.Project,
) error {
	result := db.WithContext(ctx).Create(project)
	return databaseError(ctx, "create project", result.Error)
}

func GetProjectByID(ctx context.Context, db *gorm.DB, userID uint64, projectID uint64) (*model.Project, error) {
	proj := model.Project{}
	result := db.WithContext(ctx).Where("id = ? AND user_id = ?", projectID, userID).First(&proj)
	if result.Error != nil {
		return nil, databaseError(ctx, "get project", result.Error)
	}
	return &proj, nil
}

// GetProjectForUpdate must be called within the caller's transaction.
func GetProjectForUpdate(ctx context.Context, tx *gorm.DB, userID, projectID uint64) (*model.Project, error) {
	return GetProjectByID(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), userID, projectID)
}

func ListProjects(
	ctx context.Context,
	db *gorm.DB,
	userID uint64,
	page int,
	pageSize int,
) ([]model.Project, error) {
	projects := make([]model.Project, 0)

	// 1. 分页参数校验
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}

	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}
	offset := (page - 1) * pageSize

	// 3. 用户隔离、稳定排序、分页查询
	result := db.WithContext(ctx).
		Model(&model.Project{}).
		Where("projects.user_id = ?", userID).
		Order("projects.created_at DESC").
		Order("projects.id DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&projects)
	if result.Error != nil {
		return nil, databaseError(ctx, "list projects", result.Error)
	}
	return projects, nil
}

func UpdateProjectName(
	ctx context.Context,
	db *gorm.DB,
	userID, projectID uint64,
	name string,
) (*model.Project, error) {
	var updated *model.Project

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 校验项目归属，并锁定目标记录
		_, err := GetProjectForUpdate(
			ctx, tx, userID, projectID,
		)
		if err != nil {
			return err
		}

		// 2. 在事务内更新项目名称
		result := tx.WithContext(ctx).
			Model(&model.Project{}).
			Where("id = ? AND user_id = ?", projectID, userID).
			Update("name", name)

		if result.Error != nil {
			return databaseError(
				ctx, "update project name", result.Error,
			)
		}

		// 3. 读取更新后的数据库记录
		updated, err = GetProjectByID(
			ctx, tx, userID, projectID,
		)
		return err
	})

	if err != nil {
		return nil, err
	}

	return updated, nil
}
