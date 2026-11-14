package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"GoServer/internal/consumer"
	"GoServer/internal/database"
	"GoServer/internal/githubclient"
	"GoServer/internal/publisher"
	"GoServer/internal/repository"
	"GoServer/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// 1. 初始化 MySQL
	db, err := database.InitDB()
	if err != nil {
		return fmt.Errorf("init MySQL: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get MySQL connection pool: %w", err)
	}
	defer sqlDB.Close()

	// 2. 创建 Repository
	jobStore := repository.NewImportJobStore(db)

	// 3. 创建 GitHub Executor
	executor := worker.NewGitHubExecutor(
		db,
		githubclient.NewClient(),
	)

	// 4. 创建 Runner
	workerID := fmt.Sprintf(
		"consumer-worker-%d",
		os.Getpid(),
	)

	runner, err := worker.NewRunner(
		jobStore,
		executor,
		workerID,
	)
	if err != nil {
		return fmt.Errorf("create runner: %w", err)
	}

	// 6. Kafka 配置
	brokers := []string{"127.0.0.1:9092"}

	if raw := os.Getenv("KAFKA_BROKERS"); raw != "" {
		brokers = nil
		for _, broker := range strings.Split(raw, ",") {
			if broker = strings.TrimSpace(broker); broker != "" {
				brokers = append(brokers, broker)
			}
		}
	}

	groupID := consumer.DefaultGroupID

	if raw := os.Getenv("KAFKA_GROUP_ID"); raw != "" {
		groupID = raw
	}

	producer, err := publisher.NewSaramaSyncProducer(brokers)
	if err != nil {
		return fmt.Errorf("create DLQ producer: %w", err)
	}
	defer func() {
		if err := producer.Close(); err != nil {
			log.Printf("close DLQ producer: %v", err)
		}
	}()

	handler, err := consumer.NewHandler(jobStore, runner, producer)
	if err != nil {
		return fmt.Errorf("create consumer handler: %w", err)
	}

	c, err := consumer.NewConsumer(
		brokers,
		groupID,
		handler,
	)
	if err != nil {
		return fmt.Errorf("create kafka consumer: %w", err)
	}

	log.Printf(
		"consumer starting: brokers=%v group=%s worker=%s",
		brokers,
		groupID,
		workerID,
	)

	// 8. 启动消费
	return c.Run(ctx)
}
