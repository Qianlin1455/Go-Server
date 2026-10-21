package model

import "time"

type TaskStatusAudit struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement"`

	TaskID      uint64 `gorm:"column:task_id;not null"`
	ActorUserID uint64 `gorm:"column:actor_user_id;not null"`

	FromStatus string `gorm:"column:from_status;type:varchar(16);not null"`
	ToStatus   string `gorm:"column:to_status;type:varchar(16);not null"`

	FromVersion uint32 `gorm:"column:from_version;not null"`
	ToVersion   uint32 `gorm:"column:to_version;not null"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (TaskStatusAudit) TableName() string {
	return "task_status_audits"
}
