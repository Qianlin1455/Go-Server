package events

import "time"

const ImportDeadLetterVersion = 1

// ImportDeadLetter preserves the original bytes as base64 in JSON.
type ImportDeadLetter struct {
	SchemaVersion     int       `json:"schema_version"`
	MessageID         string    `json:"message_id"`
	OriginalTopic     string    `json:"original_topic"`
	OriginalPartition int32     `json:"original_partition"`
	OriginalOffset    int64     `json:"original_offset"`
	OriginalKey       []byte    `json:"original_key"`
	OriginalValue     []byte    `json:"original_value"`
	EventID           string    `json:"event_id,omitempty"`
	FailureCode       string    `json:"failure_code"`
	FailureMessage    string    `json:"failure_message"`
	FailedAt          time.Time `json:"failed_at"`
}
