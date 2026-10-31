package model

import "time"

type Repository struct {
	ID            uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID     uint64    `gorm:"column:project_id;not null"`
	Owner         string    `gorm:"column:owner;not null"`
	Repo          string    `gorm:"column:repo;not null"`
	DefaultBranch string    `gorm:"column:default_branch;not null"`
	LastSHA       string    `gorm:"column:last_sha;not null"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (Repository) TableName() string {
	return "repositories"
}
