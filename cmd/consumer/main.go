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
	"GoServer/internal/repository"
	"GoServer/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// 1. 初始化 MySQL
	db, err := database.InitDB()
	if err != nil {
		log.Fatalf("init MySQL: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("get MySQL connection pool: %v", err)
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
		log.Fatalf("create runner: %v", err)
	}

	// 5. 创建 Handler，注入 Repository 和 Runner
	handler, err := consumer.NewHandler(
		jobStore,
		runner,
	)
	if err != nil {
		log.Fatalf("create consumer handler: %v", err)
	}

	// 6. Kafka 配置
	brokers := []string{"127.0.0.1:9092"}

	if raw := os.Getenv("KAFKA_BROKERS"); raw != "" {
		brokers = strings.Split(raw, ",")
	}

	groupID := consumer.DefaultGroupID

	if raw := os.Getenv("KAFKA_GROUP_ID"); raw != "" {
		groupID = raw
	}

	// 7. 创建 Consumer
	c, err := consumer.NewConsumer(
		brokers,
		groupID,
		handler,
	)
	if err != nil {
		log.Fatalf("create kafka consumer: %v", err)
	}

	log.Printf(
		"consumer starting: brokers=%v group=%s worker=%s",
		brokers,
		groupID,
		workerID,
	)

	// 8. 启动消费
	if err := c.Run(ctx); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
