package repository

import (
	"GoServer/internal/model"
	"context"
	"time"

	"gorm.io/gorm"
)

// GetTaskByUserID is used when the API supplies only a task ID. Ownership stays
// in the query so another user's task is indistinguishable from a missing task.
func GetTaskByUserID(ctx context.Context, db *gorm.DB, userID, taskID uint64) (*model.Task, error) {
	var task model.Task
	err := db.WithContext(ctx).Where("id = ? AND user_id = ?", taskID, userID).First(&task).Error
	if err != nil {
		return nil, databaseError(ctx, "get task by user", err)
	}
	return &task, nil
}

func CreateTask(
	ctx context.Context,
	db *gorm.DB,
	task *model.Task,
) error {
	// 1. 执行 INSERT
	result := db.WithContext(ctx).Create(task)
	return databaseError(ctx, "create task", result.Error)
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
	if result.Error != nil {
		return nil, databaseError(ctx, "get task", result.Error)
	}
	return &task, nil
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
	if result.Error != nil {
		return nil, databaseError(ctx, "list tasks", result.Error)
	}
	return tasks, nil
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
	result := db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND user_id = ? AND project_id = ? AND status = ? AND version = ?",
			taskID, userID, projectID, fromStatus, expectedVersion).
		Updates(map[string]any{
			"status":       toStatus,
			"version":      gorm.Expr("version + 1"),
			"completed_at": completedAt,
		})
	if err := databaseError(ctx, "update task status", result.Error); err != nil {
		return 0, err
	}
	return result.RowsAffected, nil
}
