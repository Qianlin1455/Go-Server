package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"GoServer/internal/database"
	"GoServer/internal/publisher"
	"GoServer/internal/repository"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("publisher exited:%v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	db, err := database.InitDB()
	if err != nil {
		return fmt.Errorf("init MySQL:%w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("got DB poll:%w", err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			log.Printf("close MySQL:%v", err)
		}
	}()
	//读取kafka broker配置
	brokers := []string{"127.0.0.1:9092"}

	if raw := os.Getenv("KAFKA_BROKERS"); raw != "" {
		brokers = nil
		for _, broker := range strings.Split(raw, ",") {
			broker = strings.TrimSpace(broker)
			if broker != "" {
				brokers = append(brokers, broker)
			}
		}
	}

	if len(brokers) == 0 {
		return fmt.Errorf("KAFKA_BROKERS cannot be empty")
	}
	//初始化Kafka SyncProducer
	producer, err := publisher.NewSaramaSyncProducer(brokers)

	defer func() {
		if err := producer.Close(); err != nil {
			log.Printf("close kafka producer:%v", err)
		}
	}()

	// 5. 配置批量大小
	batchSize := publisher.DefaultBatchSize

	if raw := os.Getenv("PUBLISHER_BATCH_SIZE"); raw != "" {
		batchSize, err = strconv.Atoi(raw)
		if err != nil || batchSize <= 0 {
			return fmt.Errorf("invalid PUBLISHER_BATCH_SIZE: %s", raw)
		}
	}

	// 6. 配置扫描间隔
	interval := time.Second

	if raw := os.Getenv("PUBLISHER_INTERVAL"); raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return fmt.Errorf("invalid PUBLISHER_INTERVAL: %s", raw)
		}
	}
	// 7. 初始化 Outbox Repository
	outboxStore := repository.NewOutboxEventStore(db)

	// 8. 创建 Publisher
	p, err := publisher.New(
		outboxStore,
		producer,
		batchSize,
	)
	if err != nil {
		return fmt.Errorf("create publisher: %w", err)
	}

	log.Printf(
		"publisher started: brokers=%v batch_size=%d interval=%s topic=%s",
		brokers,
		batchSize,
		interval,
		publisher.ImportsRequestedTopic,
	)

	// 9. 启动持续发布循环
	return p.Run(ctx, interval)
}
