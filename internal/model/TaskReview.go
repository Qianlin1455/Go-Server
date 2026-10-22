package model

import "time"

const (
	ReviewerTypeHuman = "human"
	ReviewerTypeAI    = "ai"

	ReviewApproved      = "approved"
	ReviewRejected      = "rejected"
	ReviewSuggestedPass = "suggested_pass"
	ReviewSuggestedFail = "suggested_fail"
)

type TaskReview struct {
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement"`

	TaskID         uint64  `gorm:"column:task_id;not null"`
	ReviewerType   string  `gorm:"column:reviewer_type;type:varchar(16);not null"`
	ReviewerUserID *uint64 `gorm:"column:reviewer_user_id"`

	Decision string  `gorm:"column:decision;type:varchar(32);not null"`
	Feedback *string `gorm:"column:feedback;type:text"`

	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);autoCreateTime"`
}

func (TaskReview) TableName() string {
	return "task_reviews"
}
