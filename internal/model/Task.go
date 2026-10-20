package model

import "time"

const (
	TaskStatusTodo  = "todo"
	TaskStatusDoing = "doing"
	TaskStatusDone  = "done"
)

type Task struct {
	ID              uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	UserID          uint64     `gorm:"column:user_id;not null"`
	ProjectID       uint64     `gorm:"column:project_id;not null"`
	SourceMessageID *uint64    `gorm:"column:source_message_id"`
	Title           string     `gorm:"column:title;type:varchar(200);not null"`
	Description     *string    `gorm:"column:description;type:text"`
	Status          string     `gorm:"column:status;type:varchar(16);not null;default:todo"`
	Version         uint32     `gorm:"column:version;not null;default:1"`
	CompletedAt     *time.Time `gorm:"column:completed_at;type:datetime(3)"`
	CreatedAt       time.Time  `gorm:"column:created_at;type:datetime(3);autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;type:datetime(3);autoUpdateTime"`
}

func (Task) TableName() string {
	return "tasks"
}
