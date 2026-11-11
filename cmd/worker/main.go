package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"GoServer/internal/database"
	"GoServer/internal/githubclient"
	"GoServer/internal/repository"
	"GoServer/internal/worker"
)

func main() {
	// V1 仅允许在明确选择核验模式时启动
	if os.Getenv("WORKER_MODE") != "revision-check" {
		log.Fatal(
			"set WORKER_MODE=revision-check " +
				"to enable metadata-only worker",
		)
	}

	// 1. Worker 根 Context
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
	)
	defer stop()

	// 2. 初始化 MySQL
	db, err := database.InitDB()
	if err != nil {
		log.Fatalf("init MySQL: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("get DB pool: %v", err)
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
		log.Fatalf("init runner: %v", err)
	}

	// 6. 启动任务调度循环
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("run worker: %v", err)
	}
}
