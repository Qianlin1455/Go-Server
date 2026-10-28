package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"GoServer/internal/cache"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGetProject_RedisRestartRecovery(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	addr := os.Getenv("TEST_REDIS_RESTART_ADDR")
	container := os.Getenv("TEST_REDIS_RESTART_CONTAINER")

	if dsn == "" || addr == "" || container == "" {
		t.Skip("未设置 Redis 重启测试环境变量")
	}

	// 安全保护：只允许操作专用测试容器。
	if container != "go-server-redis-recovery" ||
		addr != "127.0.0.1:6380" {
		t.Fatal("拒绝操作非专用 Redis 测试容器")
	}

	runDocker := func(args ...string) {
		t.Helper()
		cmdCtx, cancel := context.WithTimeout(
			context.Background(), 20*time.Second,
		)
		defer cancel()

		out, err := exec.CommandContext(
			cmdCtx, "docker", args...,
		).CombinedOutput()

		if err != nil {
			t.Fatalf("docker %v: %v, %s", args, err, out)
		}
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		TranslateError: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Scan(&dbName).Error; err != nil {
		t.Fatal(err)
	}
	if dbName != "go_server_test" {
		t.Fatal("只能在 go_server_test 中运行")
	}

	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// 使用真实提交的数据，而不是外层回滚事务。
	user := &model.Users{
		UserName: "restart_" + suffix,
		Email:    "restart_" + suffix + "@example.com",
		Password: "test-hash",
	}
	if err := repository.CreateUser(ctx, db, user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Delete(&model.Users{}, user.ID).Error
	})

	project := &model.Project{
		UserID: user.ID,
		Name:   "restart_project_" + suffix,
		Status: model.ProjectStatusActive,
	}
	if err := repository.CreateProject(ctx, db, project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Delete(&model.Project{}, project.ID).Error
	})

	counter := &projectSelectCounter{
		Interface: logger.Default,
	}
	countedDB := db.Session(&gorm.Session{
		Logger: counter,
	})

	redisCache, err := cache.NewRedisCache(
		addr, "", 0,
		"go-server:restart-test:"+suffix,
		100*time.Millisecond,
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer redisCache.Close()

	key := fmt.Sprintf("project:%d:%d", user.ID, project.ID)
	svc := NewProjectServiceWithCache(countedDB, redisCache)

	mustGet := func(expectedSQL int64) {
		t.Helper()
		callCtx, cancel := context.WithTimeout(
			context.Background(), 3*time.Second,
		)
		defer cancel()

		got, err := svc.GetProject(
			callCtx, user.ID, project.ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.ID != project.ID ||
			got.Name != project.Name {
			t.Fatalf("项目结果错误: %+v", got)
		}
		if n := counter.count.Load(); n != expectedSQL {
			t.Fatalf(
				"预期累计 SELECT=%d，实际=%d",
				expectedSQL, n,
			)
		}
	}

	// 确保开始时 Redis 可用。
	if err := redisCache.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	// 1. 首次读取回源，第二次命中缓存。
	mustGet(1)
	mustGet(1)
	t.Log("PASS: Redis 正常，缓存命中")

	// 防止测试失败后，专用容器一直处于停止状态。
	needsRestart := false
	t.Cleanup(func() {
		if needsRestart {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(), 20*time.Second,
			)
			defer cancel()
			out, err := exec.CommandContext(
				cleanupCtx, "docker", "start", container,
			).CombinedOutput()
			if err != nil {
				t.Errorf("恢复测试容器失败: %v, %s", err, out)
			}
		}
	})

	// 2. 真实停止 Redis。
	needsRestart = true
	runDocker("stop", "-t", "1", container)

	// Redis down 时业务应该回退 MySQL。
	mustGet(2)
	t.Log("PASS: Redis 停机，成功回退 MySQL")

	// 3. 重新启动同一个 Redis 容器。
	runDocker("start", container)
	needsRestart = false

	// 使用同一个 RedisCache 等待连接恢复。
	recovered := false
	for i := 0; i < 40; i++ {
		pingCtx, cancel := context.WithTimeout(
			context.Background(), 200*time.Millisecond,
		)
		err := redisCache.Ping(pingCtx)
		cancel()

		if err == nil {
			recovered = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("Redis 重启后未能恢复连接")
	}
	t.Log("PASS: 原 RedisCache 客户端恢复连接")

	// 禁用了持久化，重启后旧缓存应该消失。
	_, hit, err := redisCache.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("重启后不应保留旧缓存")
	}

	// 4. 再次回源并重新回填缓存。
	mustGet(3)
	mustGet(3)
	t.Log("PASS: Redis 恢复后重新回填并命中缓存")
}
