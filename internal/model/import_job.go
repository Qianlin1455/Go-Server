package model

import "time"

const (
	ImportJobPending = "pending"
	ImportJobRunning = "running"
	ImportJobFailed  = "failed"
	ImportJobSuccess = "success"
)

type ImportJob struct {
	ID                 uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	UserID             uint64    `gorm:"column:user_id;not null"`
	ProjectID          uint64    `gorm:"column:project_id;not null"`
	RepositoryID       uint64    `gorm:"column:repository_id;not null"`
	Endpoint           string    `gorm:"column:endpoint;not null"`
	IdempotencyKey     string    `gorm:"column:idempotency_key;not null"`
	RequestFingerprint string    `gorm:"column:request_fingerprint;not null"`
	RevisionSHA        string    `gorm:"column:revision_sha;not null"`
	Status             string    `gorm:"column:status;not null"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

func (ImportJob) TableName() string {
	return "import_jobs"
}
