package consumer

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"GoServer/events"
	"GoServer/internal/repository"

	"github.com/IBM/sarama"
)

type testDeadLetterProducer func(*sarama.ProducerMessage) (int32, int64, error)

func (f testDeadLetterProducer) SendMessage(msg *sarama.ProducerMessage) (int32, int64, error) {
	return f(msg)
}

func TestHandler_DeadLetterBeforeOffset(t *testing.T) {
	db := setupConsumerTestDB(t)
	job := createConsumerTestJob(t, db)
	store := repository.NewImportJobStore(db)
	event := events.ImportRequestedV1{
		EventID: "poison-test", EventType: events.ImportRequestedType, SchemaVersion: events.ImportRequestedVersion,
		OccurredAt: time.Now(), JobID: job.ID, UserID: job.UserID, ProjectID: job.ProjectID,
		RepositoryID: job.RepositoryID, RevisionSHA: job.RevisionSHA,
	}
	brokerErr := errors.New("broker unavailable")
	for _, tc := range []struct {
		name, code string
		edit       func(*events.ImportRequestedV1)
		sendErr    error
	}{
		{"invalid JSON", "INVALID_EVENT_JSON", nil, nil},
		{"missing event ID", "INVALID_EVENT_FIELDS", func(e *events.ImportRequestedV1) { e.EventID = "" }, nil},
		{"unsupported schema", "UNSUPPORTED_EVENT_VERSION", func(e *events.ImportRequestedV1) { e.SchemaVersion++ }, nil},
		{"invalid type", "INVALID_EVENT_TYPE", func(e *events.ImportRequestedV1) { e.EventType = "Unexpected" }, nil},
		{"job mismatch", "IMPORT_JOB_MISMATCH", func(e *events.ImportRequestedV1) { e.UserID++ }, nil},
		{"job absent", "IMPORT_JOB_NOT_FOUND", func(e *events.ImportRequestedV1) { e.JobID = ^uint64(0) }, nil},
		{"broker failure", "INVALID_EVENT_JSON", nil, brokerErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte{0xff, '{'}
			input := event
			wantEventID := ""
			if tc.edit != nil {
				tc.edit(&input)
				var err error
				body, err = json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				wantEventID = input.EventID
			}
			msg := &sarama.ConsumerMessage{Topic: ImportsRequestedTopic, Partition: 2, Offset: 17, Key: []byte{0, 0xff}, Value: body}
			session := newTestConsumerSession()
			processor := &failingJobProcessor{}
			sends := 0
			producer := testDeadLetterProducer(func(out *sarama.ProducerMessage) (int32, int64, error) {
				sends++
				if len(session.marked) != 0 || session.commitCount != 0 {
					t.Fatal("source offset advanced before broker ACK")
				}
				payload, err := out.Value.Encode()
				if err != nil {
					t.Fatal(err)
				}
				var letter events.ImportDeadLetter
				if err := json.Unmarshal(payload, &letter); err != nil {
					t.Fatal(err)
				}
				key, err := out.Key.Encode()
				if err != nil {
					t.Fatal(err)
				}
				if out.Topic != ImportsDLQTopic || string(key) != "imports.requested:2:17" ||
					letter.MessageID != string(key) || letter.SchemaVersion != 1 ||
					letter.OriginalTopic != msg.Topic || letter.OriginalPartition != msg.Partition || letter.OriginalOffset != msg.Offset ||
					!bytes.Equal(letter.OriginalKey, msg.Key) || !bytes.Equal(letter.OriginalValue, msg.Value) ||
					letter.EventID != wantEventID || letter.FailureCode != tc.code || letter.FailureMessage == "" || letter.FailedAt.IsZero() {
					t.Fatalf("unexpected dead letter: %+v", letter)
				}
				return 0, 0, tc.sendErr
			})
			handler, err := NewHandler(store, processor, producer)
			if err != nil {
				t.Fatal(err)
			}
			claim := singleMessageClaim(msg)
			err = handler.ConsumeClaim(session, claim)
			if sends != 1 || processor.calls != 0 {
				t.Fatalf("sends=%d, processor calls=%d", sends, processor.calls)
			}
			if tc.sendErr != nil {
				if !errors.Is(err, tc.sendErr) || len(session.marked) != 0 || session.commitCount != 0 {
					t.Fatalf("source offset advanced after send error: %v", err)
				}
			} else if err != nil || len(session.marked) != 1 || session.marked[0] != msg || session.commitCount != 1 {
				t.Fatalf("source offset was not advanced after ACK: %v", err)
			}
		})
	}
}

func TestHandler_NoDeadLetterWhenMySQLUnavailable(t *testing.T) {
	db := setupCommittedConsumerTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	processor := &failingJobProcessor{}
	producer := testDeadLetterProducer(func(*sarama.ProducerMessage) (int32, int64, error) {
		t.Fatal("temporary database failure sent to DLQ")
		return 0, 0, nil
	})
	handler, err := NewHandler(repository.NewImportJobStore(db), processor, producer)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"event_id":"db-down","event_type":"ImportRequested","schema_version":1,
		"occurred_at":"2026-01-01T00:00:00Z","job_id":1,"user_id":1,"project_id":1,
		"repository_id":1,"revision_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	session := newTestConsumerSession()
	err = handler.ConsumeClaim(session, singleMessageClaim(&sarama.ConsumerMessage{Topic: ImportsRequestedTopic, Value: body}))
	if err == nil || len(session.marked) != 0 || session.commitCount != 0 || processor.calls != 0 {
		t.Fatalf("temporary database failure did not block processing: %v", err)
	}
}

func singleMessageClaim(msg *sarama.ConsumerMessage) *testConsumerClaim {
	claim := &testConsumerClaim{messages: make(chan *sarama.ConsumerMessage, 1)}
	claim.messages <- msg
	close(claim.messages)
	return claim
}
