package repository

import (
	"GoServer/internal/model"
	"context"
	"gorm.io/gorm"
)

// Each child query scopes ownership in SQL, even after Service checks the task.
func ListTaskEvidenceByUser(ctx context.Context, db *gorm.DB, userID, taskID uint64) ([]model.TaskEvidence, error) {
	rows := make([]model.TaskEvidence, 0)
	err := db.WithContext(ctx).Model(&model.TaskEvidence{}).Select("task_evidence.*").
		Joins("JOIN tasks ON tasks.id = task_evidence.task_id AND tasks.user_id = task_evidence.created_by").
		Where("tasks.user_id = ? AND tasks.id = ?", userID, taskID).
		Order("task_evidence.id ASC").Find(&rows).Error
	return rows, databaseError(ctx, "list task evidence", err)
}

func ListTaskAuditsByUser(ctx context.Context, db *gorm.DB, userID, taskID uint64) ([]model.TaskStatusAudit, error) {
	rows := make([]model.TaskStatusAudit, 0)
	err := db.WithContext(ctx).Model(&model.TaskStatusAudit{}).Select("task_status_audits.*").
		Joins("JOIN tasks ON tasks.id = task_status_audits.task_id").
		Where("tasks.user_id = ? AND tasks.id = ?", userID, taskID).
		Order("task_status_audits.to_version ASC").Find(&rows).Error
	return rows, databaseError(ctx, "list task audits", err)
}

func GetTaskSourceByUser(ctx context.Context, db *gorm.DB, userID, projectID, messageID uint64) (*model.Message, error) {
	var row model.Message
	err := db.WithContext(ctx).Model(&model.Message{}).Select("messages.*").
		Joins("JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.id = ? AND conversations.user_id = ? AND conversations.project_id = ?", messageID, userID, projectID).
		First(&row).Error
	if err != nil {
		return nil, databaseError(ctx, "get task source", err)
	}
	return &row, nil
}
