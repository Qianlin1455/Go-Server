package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"GoServer/events"

	"github.com/IBM/sarama"
)

const ImportsDLQTopic = "imports.dlq"

type DeadLetterProducer interface {
	SendMessage(*sarama.ProducerMessage) (partition int32, offset int64, err error)
}

type permanentMessageError struct {
	code    string
	message string
}

func (e *permanentMessageError) Error() string { return e.message }

func (h *Handler) sendDeadLetter(ctx context.Context, msg *sarama.ConsumerMessage, failure *permanentMessageError) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.dlq == nil {
		return fmt.Errorf("dead-letter producer unavailable: %w", failure)
	}
	id := fmt.Sprintf("%s:%d:%d", msg.Topic, msg.Partition, msg.Offset)
	envelope := events.ImportDeadLetter{
		SchemaVersion: events.ImportDeadLetterVersion,
		MessageID:     id, OriginalTopic: msg.Topic, OriginalPartition: msg.Partition,
		OriginalOffset: msg.Offset, OriginalKey: msg.Key, OriginalValue: msg.Value,
		EventID: readEventID(msg.Value), FailureCode: failure.code, FailureMessage: failure.message,
		FailedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode dead letter %s: %w", id, err)
	}
	_, _, err = h.dlq.SendMessage(&sarama.ProducerMessage{
		Topic: ImportsDLQTopic, Key: sarama.StringEncoder(id), Value: sarama.ByteEncoder(payload),
	})
	if err != nil {
		return fmt.Errorf("send dead letter %s code=%s: %w", id, failure.code, err)
	}
	log.Printf("dead letter acknowledged: message_id=%s event_id=%q job_id=%d code=%s reason=%s",
		id, envelope.EventID, readJobID(msg.Value), failure.code, failure.message)
	return nil
}
