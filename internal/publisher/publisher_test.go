package publisher

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"GoServer/events"
	"GoServer/internal/model"

	"github.com/IBM/sarama"
)

// FakeOutboxStore 模拟数据库操作
type FakeOutboxStore struct {
	events []model.OutboxEvent

	markSentErr     error
	markSentErrOnce bool
}

// 查询 pending 事件
func (s *FakeOutboxStore) ListPending(
	ctx context.Context,
	eventType string,
	limit int,
) ([]model.OutboxEvent, error) {
	var result []model.OutboxEvent

	for _, event := range s.events {
		if event.Status == model.OutboxPending &&
			event.EventType == eventType {
			result = append(result, event)
		}
	}

	if len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

// 标记发送成功
func (s *FakeOutboxStore) MarkSent(
	ctx context.Context,
	id uint64,
	sentAt time.Time,
) error {
	if s.markSentErr != nil {
		err := s.markSentErr

		if s.markSentErrOnce {
			s.markSentErr = nil
		}

		return err
	}

	for i := range s.events {
		if s.events[i].ID == id &&
			s.events[i].Status == model.OutboxPending {
			s.events[i].Status = model.OutboxSent
			s.events[i].SentAt = &sentAt
			return nil
		}
	}

	return errors.New("event not pending")
}

// 记录发布失败
func (s *FakeOutboxStore) MarkPublishFailed(
	ctx context.Context,
	id uint64,
	publishErr error,
) error {
	for i := range s.events {
		if s.events[i].ID == id &&
			s.events[i].Status == model.OutboxPending {
			s.events[i].Attempts++

			message := publishErr.Error()
			s.events[i].LastError = &message

			return nil
		}
	}

	return errors.New("event not pending")
}

// FakeProducer 模拟 Kafka Producer
type FakeProducer struct {
	messages []*sarama.ProducerMessage
	err      error
}

func (p *FakeProducer) SendMessage(
	msg *sarama.ProducerMessage,
) (int32, int64, error) {
	p.messages = append(p.messages, msg)

	if p.err != nil {
		return 0, 0, p.err
	}

	return 0, int64(len(p.messages) - 1), nil
}

// 构造测试事件
func newTestOutboxEvent() model.OutboxEvent {
	return model.OutboxEvent{
		ID:            1,
		EventID:       "550e8400-e29b-41d4-a716-446655440000",
		JobID:         1587,
		EventType:     events.ImportRequestedType,
		RevisionSHA:   "0123456789abcdef0123456789abcdef01234567",
		SchemaVersion: 1,
		Payload:       `{"event_id":"550e8400-e29b-41d4-a716-446655440000","schema_version":1}`,
		Status:        model.OutboxPending,
	}
}

func newTestPublisher(
	t *testing.T,
	store *FakeOutboxStore,
	producer *FakeProducer,
) *Publisher {
	t.Helper()

	p, err := New(store, producer, 100)
	if err != nil {
		t.Fatal(err)
	}

	return p
}
func TestPublishOnce_Success(t *testing.T) {
	store := &FakeOutboxStore{
		events: []model.OutboxEvent{
			newTestOutboxEvent(),
		},
	}

	producer := &FakeProducer{}
	p := newTestPublisher(t, store, producer)

	published, err := p.PublishOnce(context.Background())

	if err != nil {
		t.Fatal(err)
	}

	if published != 1 {
		t.Fatalf("expected 1 published, got %d", published)
	}

	if len(producer.messages) != 1 {
		t.Fatalf(
			"expected 1 Kafka message, got %d",
			len(producer.messages),
		)
	}

	if store.events[0].Status != model.OutboxSent {
		t.Fatalf(
			"expected sent, got %s",
			store.events[0].Status,
		)
	}

	t.Log("PASS: Kafka message sent")
	t.Log("PASS: Outbox marked sent")
}
func TestPublishOnce_KafkaFailure(t *testing.T) {
	store := &FakeOutboxStore{
		events: []model.OutboxEvent{
			newTestOutboxEvent(),
		},
	}

	producer := &FakeProducer{
		err: errors.New("kafka unavailable"),
	}

	p := newTestPublisher(t, store, producer)

	published, err := p.PublishOnce(context.Background())

	if err == nil {
		t.Fatal("expected publish error")
	}

	if published != 0 {
		t.Fatalf("expected 0 published, got %d", published)
	}

	event := store.events[0]

	if event.Status != model.OutboxPending {
		t.Fatalf("expected pending, got %s", event.Status)
	}

	if event.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", event.Attempts)
	}

	t.Log("PASS: failed event remains pending")
	t.Log("PASS: attempts incremented")
}

func TestPublishOnce_MarkSentFailure(t *testing.T) {
	store := &FakeOutboxStore{
		events: []model.OutboxEvent{
			newTestOutboxEvent(),
		},
		markSentErr: errors.New("mysql update failed"),
	}

	producer := &FakeProducer{}

	p := newTestPublisher(t, store, producer)

	published, err := p.PublishOnce(context.Background())

	if err == nil {
		t.Fatal("expected mark sent error")
	}

	if published != 0 {
		t.Fatalf("expected 0 published, got %d", published)
	}

	// Kafka 已经收到消息
	if len(producer.messages) != 1 {
		t.Fatalf(
			"expected 1 Kafka message, got %d",
			len(producer.messages),
		)
	}

	// 但数据库更新失败，因此仍然是 pending
	if store.events[0].Status != model.OutboxPending {
		t.Fatalf(
			"expected pending, got %s",
			store.events[0].Status,
		)
	}

	t.Log("PASS: Kafka accepted message")
	t.Log("PASS: MySQL MarkSent failed")
	t.Log("PASS: event remains pending")
}

func TestPublishOnce_RetryAfterMarkSentFailure(t *testing.T) {
	store := &FakeOutboxStore{
		events: []model.OutboxEvent{
			newTestOutboxEvent(),
		},
		markSentErr:     errors.New("mysql update failed"),
		markSentErrOnce: true,
	}

	producer := &FakeProducer{}

	p := newTestPublisher(t, store, producer)

	// 第一次：Kafka 成功，但 MySQL 更新失败
	_, err := p.PublishOnce(context.Background())

	if err == nil {
		t.Fatal("expected first publish to return error")
	}

	if store.events[0].Status != model.OutboxPending {
		t.Fatal("event should remain pending")
	}

	// 第二次：重新扫描并发送
	published, err := p.PublishOnce(context.Background())

	if err != nil {
		t.Fatal(err)
	}

	if published != 1 {
		t.Fatalf("expected 1 published, got %d", published)
	}

	// 两次发送都成功到达 Mock Kafka
	if len(producer.messages) != 2 {
		t.Fatalf(
			"expected 2 messages, got %d",
			len(producer.messages),
		)
	}

	// 验证两次发送的 Payload 完全一致
	first, err := producer.messages[0].Value.Encode()
	if err != nil {
		t.Fatal(err)
	}

	second, err := producer.messages[1].Value.Encode()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Fatal("expected identical event payloads")
	}

	// 第二次发送成功后，状态才变成 sent
	if store.events[0].Status != model.OutboxSent {
		t.Fatalf(
			"expected sent, got %s",
			store.events[0].Status,
		)
	}

	t.Log("PASS: same event published twice")
	t.Log("PASS: event payload remained identical")
	t.Log("PASS: event eventually marked sent")
}
