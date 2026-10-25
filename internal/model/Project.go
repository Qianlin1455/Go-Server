package model

import "time"

const (
	ProjectStatusActive   = "active"
	ProjectStatusArchived = "archived"
)

type Project struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement;uniqueIndex:uk_projects_id_user_id,priority:1;index:idx_projects_user_id_id,priority:2"`

	UserID uint64 `gorm:"column:user_id;not null;uniqueIndex:uk_projects_user_name,priority:1;uniqueIndex:uk_projects_id_user_id,priority:2;index:idx_projects_user_id_id,priority:1"`

	Name string `gorm:"column:name;type:varchar(128);not null;uniqueIndex:uk_projects_user_name,priority:2"`

	Description *string `gorm:"column:description;type:text"`

	Status string `gorm:"column:status;type:varchar(16);not null;default:active"`

	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);not null;autoCreateTime"`

	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(3);not null;autoUpdateTime"`
}

func (Project) TableName() string {
	return "projects"
}
