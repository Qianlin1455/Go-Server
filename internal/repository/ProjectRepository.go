package repository

import (
	"context"
	"errors"
	"fmt"

	"GoServer/internal/model"
	"gorm.io/gorm"
)

func CreateProject(
	ctx context.Context,
	db *gorm.DB,
	project *model.Project,
) error {
	result := db.WithContext(ctx).Create(project)
	if result.Error == nil {
		return nil
	}

	// 优先处理 Context 取消或超时。
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 同一用户创建重名项目。
	if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
		return ErrAlreadyExists
	}

	// 项目引用了不存在的用户。
	if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
		return fmt.Errorf(
			"create project: %w",
			ErrInvalidReference,
		)
	}

	// 其他数据库错误。
	return fmt.Errorf(
		"create project failed: %w",
		ErrStorage,
	)
}

func GetProjectByID(ctx context.Context, db *gorm.DB, userID uint64, projectID uint64) (*model.Project, error) {
	proj := model.Project{}
	result := db.WithContext(ctx).Where("id = ? AND user_id = ?", projectID, userID).First(&proj)
	if result.Error == nil {
		return &proj, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf(
		"get project: %w",
		errors.Join(ErrStorage, result.Error),
	)
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

	// 4. 查询成功
	if result.Error == nil {
		return projects, nil
	}

	// 5. Context 取消或超时
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 6. 其他数据库错误
	return nil, fmt.Errorf(
		"list projects: %w",
		errors.Join(ErrStorage, result.Error),
	)
}
