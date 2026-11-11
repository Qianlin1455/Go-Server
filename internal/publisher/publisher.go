package publisher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"GoServer/events"
	"GoServer/internal/model"

	"github.com/IBM/sarama"
)

const (
	ImportsRequestedTopic = "imports.requested"
	DefaultBatchSize      = 100
)

// OutboxStore 定义 Publisher 所需的数据库操作。
type OutboxStore interface {
	ListPending(
		ctx context.Context,
		eventType string,
		limit int,
	) ([]model.OutboxEvent, error)

	MarkSent(
		ctx context.Context,
		id uint64,
		sentAt time.Time,
	) error

	MarkPublishFailed(
		ctx context.Context,
		id uint64,
		publishErr error,
	) error
}

// MessageProducer 抽象 Kafka 同步生产者，方便单元测试。
type MessageProducer interface {
	SendMessage(
		msg *sarama.ProducerMessage,
	) (partition int32, offset int64, err error)
}

type Publisher struct {
	store     OutboxStore
	producer  MessageProducer
	batchSize int
}

func New(
	store OutboxStore,
	producer MessageProducer,
	batchSize int,
) (*Publisher, error) {
	if store == nil || producer == nil {
		return nil, errors.New("publisher dependencies cannot be nil")
	}

	if batchSize <= 0 {
		return nil, errors.New("batch size must be positive")
	}

	return &Publisher{
		store:     store,
		producer:  producer,
		batchSize: batchSize,
	}, nil
}

// PublishOnce扫描并发布一批Outbox事件
// 返回值：published:成功发送且成功标记sent事件数量
func (p *Publisher) PublishOnce(
	ctx context.Context,
) (int, error) {
	//查询pending事件
	pendingEvents, err := p.store.ListPending(
		ctx, events.ImportRequestedType, p.batchSize,
	)
	if err != nil {
		return 0, fmt.Errorf("list pending outbox events:%w", err)
	}

	published := 0
	var publishErrors []error

	for _, event := range pendingEvents {
		if err := ctx.Err(); err != nil {
			publishErrors = append(publishErrors, err)
			break
		}

		//构造kafka消息
		msg := &sarama.ProducerMessage{
			Topic: ImportsRequestedTopic,
			//同一个Job事件使用相同的kafka key
			Key: sarama.StringEncoder(
				strconv.FormatUint(event.JobID, 10),
			),
			Value: sarama.ByteEncoder(event.Payload),
		}
		_, _, sendErr := p.producer.SendMessage(msg)

		if sendErr != nil {
			markErr := p.store.MarkPublishFailed(
				ctx, event.ID, sendErr,
			)
			if markErr != nil {
				publishErrors = append(publishErrors,
					fmt.Errorf("record publish failure for event %d : %w", event.ID, markErr))
			}

			publishErrors = append(publishErrors,
				fmt.Errorf("publish event %s:%w", event.EventID, sendErr))
			continue
		}
		//kafka确定成功后才标记sent
		if err := p.store.MarkSent(ctx, event.ID, time.Now().UTC()); err != nil {
			//kafka可能收到消息不能标记为失败，保留pending
			publishErrors = append(publishErrors,
				fmt.Errorf("mark event %s as sent :%w", event.EventID, err))
			continue
		}
		published++
	}
	return published, errors.Join(publishErrors...)
}

// Run 定期扫描并发布 Outbox 事件。
func (p *Publisher) Run(
	ctx context.Context,
	interval time.Duration,
) error {
	if interval <= 0 {
		return errors.New("publisher interval must be positive")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return nil
		}

		published, err := p.PublishOnce(ctx)

		if err != nil {
			log.Printf(
				"publisher: publish batch failed: %v",
				err,
			)
		}

		if published > 0 {
			log.Printf(
				"publisher: successfully published %d events",
				published,
			)
		}

		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
		}
	}
}

// NewSaramaSyncProducer 创建 Kafka 同步生产者。
func NewSaramaSyncProducer(
	brokers []string,
) (sarama.SyncProducer, error) {
	if len(brokers) == 0 {
		return nil, errors.New("kafka brokers cannot be empty")
	}

	config := sarama.NewConfig()

	// 等待所有 ISR 副本确认。
	config.Producer.RequiredAcks = sarama.WaitForAll

	// SyncProducer 必须开启成功回执。
	config.Producer.Return.Successes = true

	// Sarama 内部发送重试。
	config.Producer.Retry.Max = 3

	// 网络超时边界。
	config.Net.DialTimeout = 5 * time.Second
	config.Net.ReadTimeout = 10 * time.Second
	config.Net.WriteTimeout = 10 * time.Second

	return sarama.NewSyncProducer(
		brokers,
		config,
	)
}
