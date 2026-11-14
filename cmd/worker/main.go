package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"GoServer/internal/database"
	"GoServer/internal/githubclient"
	"GoServer/internal/repository"
	"GoServer/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// V1 仅允许在明确选择核验模式时启动
	if os.Getenv("WORKER_MODE") != "revision-check" {
		return fmt.Errorf(
			"set WORKER_MODE=revision-check " +
				"to enable metadata-only worker",
		)
	}

	// 1. Worker 根 Context
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	grace := 15 * time.Second
	if raw := os.Getenv("WORKER_SHUTDOWN_GRACE"); raw != "" {
		var err error
		grace, err = time.ParseDuration(raw)
		if err != nil || grace <= 0 {
			return fmt.Errorf("WORKER_SHUTDOWN_GRACE must be a positive duration")
		}
	}

	// 2. 初始化 MySQL
	db, err := database.InitDB()
	if err != nil {
		return fmt.Errorf("init MySQL: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get DB pool: %w", err)
	}
	defer sqlDB.Close()

	// 3. 每个进程使用不同的 Worker ID
	workerID := fmt.Sprintf(
		"worker-%d",
		os.Getpid(),
	)

	// 4. 注入真实 GitHub Executor
	executor := worker.NewGitHubExecutor(
		db,
		githubclient.NewClient(),
	)

	// 5. 创建 Runner
	runner, err := worker.NewRunner(
		repository.NewImportJobStore(db),
		executor,
		workerID,
	)
	if err != nil {
		return fmt.Errorf("init runner: %w", err)
	}

	// 6. 启动任务调度循环
	return runner.Run(ctx, grace)
}
