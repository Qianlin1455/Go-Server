package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"GoServer/events"
	"GoServer/internal/publisher"

	"github.com/IBM/sarama"
)

func TestConsumer_RealKafkaDeadLetter(t *testing.T) {
	broker := os.Getenv("TEST_KAFKA_BROKER")
	if broker == "" {
		t.Skip("set TEST_KAFKA_BROKER to a disposable Kafka broker with empty imports topics")
	}
	brokers := []string{broker}
	config := sarama.NewConfig()
	config.Net.DialTimeout = 3 * time.Second
	config.Net.ReadTimeout = 3 * time.Second
	config.Net.WriteTimeout = 3 * time.Second
	admin, err := sarama.NewClusterAdmin(brokers, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, topic := range []string{ImportsRequestedTopic, ImportsDLQTopic} {
		if err := admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 1, ReplicationFactor: 1}, false); err != nil {
			// Only an empty topic on the dedicated broker is safe to consume from OffsetOldest.
			if topicErr, ok := err.(*sarama.TopicError); !ok || topicErr.Err != sarama.ErrTopicAlreadyExists {
				t.Fatal(err)
			}
		}
	}
	client, err := sarama.NewClient(brokers, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	groupID := fmt.Sprintf("g12-dlq-%d", time.Now().UnixNano())
	defer admin.DeleteConsumerGroup(groupID)
	for _, topic := range []string{ImportsRequestedTopic, ImportsDLQTopic} {
		start, err := client.GetOffset(topic, 0, sarama.OffsetNewest)
		if err != nil {
			t.Fatal(err)
		}
		if start != 0 {
			t.Fatalf("%s must be empty on the disposable broker", topic)
		}
	}
	producer, err := publisher.NewSaramaSyncProducer(brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	handler, err := NewHandler(&failingHandlerStore{}, &failingJobProcessor{}, producer)
	if err != nil {
		t.Fatal(err)
	}
	source := []byte{0xff, '{'}
	key := []byte("kafka-poison")
	partition, offset, err := producer.SendMessage(&sarama.ProducerMessage{
		Topic: ImportsRequestedTopic, Key: sarama.ByteEncoder(key), Value: sarama.ByteEncoder(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewConsumer(brokers, groupID, handler)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	dlqReader, err := sarama.NewConsumerFromClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer dlqReader.Close()
	dlq, err := dlqReader.ConsumePartition(ImportsDLQTopic, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer dlq.Close()
	select {
	case msg := <-dlq.Messages():
		if msg == nil {
			t.Fatal("DLQ reader closed")
		}
		var letter events.ImportDeadLetter
		if err := json.Unmarshal(msg.Value, &letter); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%s:%d:%d", ImportsRequestedTopic, partition, offset)
		if letter.FailureCode != "INVALID_EVENT_JSON" || letter.MessageID != id || string(msg.Key) != id ||
			!bytes.Equal(letter.OriginalKey, key) || !bytes.Equal(letter.OriginalValue, source) {
			t.Fatalf("unexpected broker dead letter: %+v", letter)
		}
	case <-ctx.Done():
		t.Fatal("no dead letter at broker")
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		offsets, err := admin.ListConsumerGroupOffsets(groupID, map[string][]int32{ImportsRequestedTopic: {partition}})
		if err == nil {
			block := offsets.GetBlock(ImportsRequestedTopic, partition)
			if block != nil && block.Err == sarama.ErrNoError && block.Offset == offset+1 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("source offset was never committed: %v", err)
		case <-tick.C:
		}
	}
}
