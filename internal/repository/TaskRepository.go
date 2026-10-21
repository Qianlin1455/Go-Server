package repository

import (
	"GoServer/internal/model"
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

func CreateTask(
	ctx context.Context,
	db *gorm.DB,
	task *model.Task,
) error {
	// 1. 执行 INSERT
	result := db.WithContext(ctx).Create(task)

	if result.Error == nil {
		return nil
	}

	// 2. Context 取消或超时
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 3. 外键约束冲突
	if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
		return fmt.Errorf(
			"create task: %w",
			errors.Join(ErrInvalidReference, result.Error),
		)
	}

	// 4. 其他数据库错误
	return fmt.Errorf(
		"create task: %w",
		errors.Join(ErrStorage, result.Error),
	)
}

func GetTaskByID(
	ctx context.Context,
	db *gorm.DB,
	userID uint64,
	projectID uint64,
	taskID uint64,
) (*model.Task, error) {
	task := model.Task{}

	// 1. 同时校验用户、项目和任务 ID
	result := db.WithContext(ctx).
		Where(
			"id = ? AND project_id = ? AND user_id = ?",
			taskID, projectID, userID,
		).
		First(&task)

	// 2. 查询成功
	if result.Error == nil {
		return &task, nil
	}

	// 3. Context 取消或超时
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 4. 任务不存在或不属于当前用户/项目
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}

	// 5. 其他数据库错误
	return nil, fmt.Errorf(
		"get task: %w",
		errors.Join(ErrStorage, result.Error),
	)
}

func ListTasks(
	ctx context.Context,
	db *gorm.DB,
	userID uint64,
	projectID uint64,
	page int,
	pageSize int,
) ([]model.Task, error) {
	tasks := make([]model.Task, 0)

	// 1. 分页参数校验
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return nil, ErrInvalidPagination
	}

	// 2. 防止 OFFSET 整数溢出
	maxInt := int(^uint(0) >> 1)

	if page-1 > maxInt/pageSize {
		return nil, ErrInvalidPagination
	}

	// 3. 用户与项目隔离、确定性排序、分页
	result := db.WithContext(ctx).
		Model(&model.Task{}).
		Where(
			"tasks.user_id = ? AND tasks.project_id = ?",
			userID, projectID,
		).
		Order("tasks.created_at DESC").
		Order("tasks.id DESC").
		Scopes(Paginate(page, pageSize)).
		Find(&tasks)

	// 4. 查询成功
	if result.Error == nil {
		return tasks, nil
	}

	// 5. Context 取消或超时
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 6. 其他数据库错误
	return nil, fmt.Errorf(
		"list tasks: %w",
		errors.Join(ErrStorage, result.Error),
	)
}

func UpdateTaskStatusCAS(
	ctx context.Context,
	db *gorm.DB,
	userID uint64,
	projectID uint64,
	taskID uint64,
	fromStatus string,
	toStatus string,
	expectedVersion uint32,
	completedAt *time.Time,
) (int64, error) {
	if userID < 1 || projectID < 1 || taskID < 1 {
		return 0, ErrInvalidReference
	}
	if !canTransition(fromStatus, toStatus) {
		return 0, ErrTaskStatusCannotTransit
	}
	result := db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND user_id = ? AND project_id = ? AND status = ? AND version = ?",
			taskID, userID, projectID, fromStatus, expectedVersion).
		Updates(map[string]any{
			"status":       toStatus,
			"version":      expectedVersion + 1,
			"completed_at": completedAt,
		})
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if result.Error != nil {
		return 0, fmt.Errorf("update task status: %w", errors.Join(ErrStorage, result.Error))
	}
	if result.RowsAffected == 0 {
		return 0, ErrNotFound
	}
	return result.RowsAffected, nil
}

func canTransition(from, to string) bool {
	if from == "todo" && to == "doing" {
		return true
	}
	if from == "doing" && (to == "done" || to == "todo") {
		return true
	}
	return false
}
