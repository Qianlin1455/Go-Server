package consumer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/IBM/sarama"
)

const (
	ImportsRequestedTopic = "imports.requested"

	DefaultGroupID = "go-server-import-workers-v1"

	consumeRetryInterval = 2 * time.Second
)

type Consumer struct {
	group   sarama.ConsumerGroup
	handler sarama.ConsumerGroupHandler
}

// NewConsumer创建Kafka Comsumer Group
func NewConsumer(
	brokers []string,
	groupID string,
	handler sarama.ConsumerGroupHandler,
) (*Consumer, error) {
	if len(brokers) == 0 || groupID == "" || handler == nil {
		return nil, errors.New("invalid consumer configuration")
	}

	//清理broker地址
	cleanBrokers := make([]string, 0, len(brokers))

	for _, broker := range brokers {
		broker = strings.TrimSpace(broker)
		if broker != "" {
			cleanBrokers = append(cleanBrokers, broker)
		}
	}

	if len(cleanBrokers) == 0 {
		return nil, errors.New("empty kafka brokers")
	}

	//创建Sarama配置
	config := sarama.NewConfig()
	//没有offset时候，从最早开始消费
	config.Consumer.Offsets.Initial = sarama.OffsetOldest
	//关闭自动提交
	config.Consumer.Offsets.AutoCommit.Enable = false
	//开启Consumer Group错误通知
	config.Consumer.Return.Errors = true
	// The handler uses session.Context as the ownership boundary. An eager
	// rebalance cancels the entire session before claims are reassigned.
	config.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{sarama.NewBalanceStrategyRange()}

	//创建Consumer Group
	group, err := sarama.NewConsumerGroup(
		cleanBrokers,
		groupID,
		config,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create kafka consumer group:%w", err,
		)
	}
	return &Consumer{
		group:   group,
		handler: handler,
	}, nil

}

// Run启动Consumer消费循环
func (c *Consumer) Run(ctx context.Context) error {
	defer func() {
		if err := c.group.Close(); err != nil {
			log.Printf("close consumer group:%v", err)
		}
	}()

	//监听Consumer group错误
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-c.group.Errors():
				if !ok {
					return
				}
				log.Printf(
					"kafka consumer group error:%v", err,
				)
			}
		}
	}()

	log.Printf("consumer started:topic=%s", ImportsRequestedTopic)
	for ctx.Err() == nil {
		//Consumer是一次Consumer Session
		//发生Rebalance后，Consumer可能返回所以必须循环调用
		err := c.group.Consume(
			ctx, []string{ImportsRequestedTopic}, c.handler,
		)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			log.Printf("consume failed,retrying:%v", err)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(consumeRetryInterval):
			}
		}

	}
	log.Println("consumer stopped: canceled Kafka session; unfinished jobs use lease recovery")
	return nil
}
