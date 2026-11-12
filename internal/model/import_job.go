package model

import "time"

const (
	ImportJobQueued    = "queued"
	ImportJobRunning   = "running"
	ImportJobFailed    = "failed"
	ImportJobSucceeded = "succeeded"
)

type ImportJob struct {
	ID                 uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	UserID             uint64 `gorm:"column:user_id;not null"`
	ProjectID          uint64 `gorm:"column:project_id;not null"`
	RepositoryID       uint64 `gorm:"column:repository_id;not null"`
	Endpoint           string `gorm:"column:endpoint;not null"`
	IdempotencyKey     string `gorm:"column:idempotency_key;not null"`
	RequestFingerprint string `gorm:"column:request_fingerprint;not null"`
	RevisionSHA        string `gorm:"column:revision_sha;not null"`
	// 导入结果：任务尚未完成时为 nil
	ResultTreeSHA    *string `gorm:"column:result_tree_sha"`
	ResultFileCount  *uint32 `gorm:"column:result_file_count"`
	ResultTotalBytes *uint64 `gorm:"column:result_total_bytes"`
	Status           string  `gorm:"column:status;not null"`

	// 执行与重试
	AttemptCount uint16    `gorm:"column:attempt_count;default:0"`
	MaxAttempts  uint16    `gorm:"column:max_attempts;default:3"`
	AvailableAt  time.Time `gorm:"column:available_at;default:CURRENT_TIMESTAMP(3)"`

	StartedAt     *time.Time `gorm:"column:started_at"`
	LastAttemptAt *time.Time `gorm:"column:last_attempt_at"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`

	// Worker 租约
	LeaseOwner     *string    `gorm:"column:lease_owner"`
	LeaseToken     []byte     `gorm:"column:lease_token;type:binary(16)"`
	LeaseExpiresAt *time.Time `gorm:"column:lease_expires_at"`

	// 错误信息
	LastErrorCode    *string `gorm:"column:last_error_code"`
	LastErrorMessage *string `gorm:"column:last_error_message"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// ImportJobResult 表示一次导入任务的执行结果。
type ImportJobResult struct {
	TreeSHA    string
	FileCount  uint32
	TotalBytes uint64
}

func (ImportJob) TableName() string {
	return "import_jobs"
}
