package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"GoServer/events"
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/worker"

	"github.com/IBM/sarama"
)

func TestHandler_RebalanceCancelsWorkWithoutOffset(t *testing.T) {
	db := setupConsumerTestDB(t)
	job := createConsumerTestJob(t, db)
	store := repository.NewImportJobStore(db)
	ctx, revoke := context.WithCancelCause(t.Context())
	defer revoke(nil)
	runner, err := worker.NewRunner(store, worker.ExecutorFunc(func(ctx context.Context, _ uint64) (model.ImportJobResult, error) {
		revoke(errors.New("test rebalance"))
		<-ctx.Done()
		return model.ImportJobResult{}, ctx.Err()
	}), "rebalance-test")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store, runner)
	if err != nil {
		t.Fatal(err)
	}
	session := newTestConsumerSession()
	session.ctx = ctx
	claim := &testConsumerClaim{messages: make(chan *sarama.ConsumerMessage, 1)}
	event := events.ImportRequestedV1{
		EventID: "rebalance-test", EventType: events.ImportRequestedType, SchemaVersion: events.ImportRequestedVersion,
		OccurredAt: time.Now(), JobID: job.ID, UserID: job.UserID, ProjectID: job.ProjectID,
		RepositoryID: job.RepositoryID, RevisionSHA: strings.Repeat("1", 40),
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	claim.messages <- &sarama.ConsumerMessage{Topic: ImportsRequestedTopic, Value: body}
	close(claim.messages)
	if err := handler.ConsumeClaim(session, claim); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled session, got %v", err)
	}
	if len(session.marked) != 0 || session.commitCount != 0 {
		t.Fatal("revoked session marked or committed the message")
	}
	got, err := store.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ImportJobRunning || got.AttemptCount != 1 || got.LastErrorCode != nil {
		t.Fatalf("canceled task must remain recoverable: %+v", got)
	}
}
