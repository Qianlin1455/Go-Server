package events

import (
	"errors"
	"regexp"
	"time"
)

const (
	ImportRequestedType    = "ImportRequested"
	ImportRequestedVersion = 1
)

var commitSHAPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{40}$`,
)

// ImportRequestedV1 描述导入任务创建事件。
// 事件本身不执行任务，也不直接发送 Kafka。
type ImportRequestedV1 struct {
	EventID       string    `json:"event_id"`
	EventType     string    `json:"event_type"`
	SchemaVersion int       `json:"schema_version"`
	OccurredAt    time.Time `json:"occurred_at"`

	JobID        uint64 `json:"job_id"`
	UserID       uint64 `json:"user_id"`
	ProjectID    uint64 `json:"project_id"`
	RepositoryID uint64 `json:"repository_id"`
	RevisionSHA  string `json:"revision_sha"`
}

func (e ImportRequestedV1) Validate() error {
	if e.EventID == "" {
		return errors.New("event_id is required")
	}

	if e.EventType != ImportRequestedType {
		return errors.New("invalid event_type")
	}

	if e.SchemaVersion != ImportRequestedVersion {
		return errors.New("unsupported event_version")
	}

	if e.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}

	if e.JobID == 0 ||
		e.UserID == 0 ||
		e.ProjectID == 0 ||
		e.RepositoryID == 0 {
		return errors.New("invalid resource ID")
	}

	if !commitSHAPattern.MatchString(e.RevisionSHA) {
		return errors.New("invalid revision_sha")
	}

	return nil
}
