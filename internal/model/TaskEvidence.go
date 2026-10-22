package model

import "time"

const (
	EvidenceTypeExperimentText = "experiment_text"
	EvidenceTypeTestOutput     = "test_output"
	EvidenceTypeCommitSHA      = "commit_sha"

	EvidenceStatusDraft     = "draft"
	EvidenceStatusSubmitted = "submitted"
)

type TaskEvidence struct {
	ID        uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	TaskID    uint64 `gorm:"column:task_id;not null"`
	CreatedBy uint64 `gorm:"column:created_by;not null"`

	Type      string  `gorm:"column:type;type:varchar(32);not null"`
	Text      *string `gorm:"column:text;type:text"`
	CommitSHA *string `gorm:"column:commit_sha;type:varchar(64)"`

	Status string `gorm:"column:status;type:varchar(16);not null;default:draft"`

	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);autoCreateTime"`
}

func (TaskEvidence) TableName() string {
	return "task_evidence"
}
