package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"GoServer/internal/cache"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 只统计针对 projects 表的 SELECT。
type projectSelectCounter struct {
	logger.Interface
	count atomic.Int64
}

func (l *projectSelectCounter) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	sql, rows := fc()
	query := strings.ToLower(strings.TrimSpace(sql))

	if strings.HasPrefix(query, "select") &&
		(strings.Contains(query, "from `projects`") ||
			strings.Contains(query, "from projects")) {
		l.count.Add(1)
	}

	l.Interface.Trace(ctx, begin, func() (string, int64) {
		return sql, rows
	}, err)
}

func TestGetProject_CacheReducesDBQueries(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	redisAddr := os.Getenv("TEST_REDIS_ADDR")

	if dsn == "" || redisAddr == "" {
		t.Skip("需要 TEST_MYSQL_DSN 和 TEST_REDIS_ADDR")
	}

	ctx := t.Context()

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
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 防止测试误操作业务数据库。
	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Scan(&dbName).Error; err != nil {
		t.Fatal(err)
	}
	if dbName != "go_server_test" {
		t.Fatalf("拒绝在 %s 上运行测试", dbName)
	}

	// 测试数据放进事务，结束时回滚。
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	user := &model.Users{
		UserName: "cache_test_" + suffix,
		Email:    "cache_test_" + suffix + "@example.com",
		Password: "test-hash",
	}
	if err := repository.CreateUser(ctx, tx, user); err != nil {
		t.Fatal(err)
	}

	project := &model.Project{
		UserID: user.ID,
		Name:   "cache_project_" + suffix,
		Status: model.ProjectStatusActive,
	}
	if err := repository.CreateProject(ctx, tx, project); err != nil {
		t.Fatal(err)
	}

	// 只从这里开始统计业务 SELECT。
	counter := &projectSelectCounter{
		Interface: logger.Default,
	}
	countedDB := tx.Session(&gorm.Session{
		Logger: counter,
	})

	redisCache, err := cache.NewRedisCache(
		redisAddr, "", 0,
		"go-server:cache-test:"+suffix,
		100*time.Millisecond,
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		key := fmt.Sprintf("project:%d:%d", user.ID, project.ID)
		_ = redisCache.Delete(context.Background(), key)
		_ = redisCache.Close()
	})

	if err := redisCache.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	svc := NewProjectServiceWithCache(countedDB, redisCache)

	// 第一次读取：Redis MISS -> MySQL。
	first, err := svc.GetProject(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.ID != project.ID {
		t.Fatalf("首次读取结果不正确: %+v", first)
	}

	firstCount := counter.count.Load()

	// 第二次读取：Redis HIT -> 不查 MySQL。
	second, err := svc.GetProject(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.ID != project.ID {
		t.Fatalf("第二次读取结果不正确: %+v", second)
	}

	secondCount := counter.count.Load()

	t.Logf("首次读取累计 Project SELECT: %d", firstCount)
	t.Logf("第二次读取累计 Project SELECT: %d", secondCount)

	if firstCount != 1 {
		t.Fatalf("首次读取预期1次SQL，实际%d次", firstCount)
	}
	if secondCount != firstCount {
		t.Fatalf("缓存命中后不应增加SQL查询次数")
	}

	// 验证用户隔离：B 不能读取 A 的项目。
	userB := &model.Users{
		UserName: "cache_user_b_" + suffix,
		Email:    "cache_user_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := repository.CreateUser(ctx, tx, userB); err != nil {
		t.Fatal(err)
	}

	// 用户 B 的缓存 Key 应当不存在。
	keyB := fmt.Sprintf(
		"project:%d:%d",
		userB.ID,
		project.ID,
	)

	_, hit, err := redisCache.Get(ctx, keyB)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("用户 B 不应该命中用户 A 的项目缓存")
	}

	// B 请求 A 的 projectID，应当被拒绝。
	got, err := svc.GetProject(ctx, userB.ID, project.ID)

	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf(
			"跨用户访问应返回 ErrProjectNotFound，实际: %v",
			err,
		)
	}
	if got != nil {
		t.Fatalf("用户 B 不应该获取用户 A 的项目: %+v", got)
	}

	// B 未命中缓存，MySQL 需要执行一次隔离查询。
	if n := counter.count.Load(); n != 2 {
		t.Fatalf("预期累计 2 次 Project SELECT，实际 %d", n)
	}

	t.Log("PASS: 用户 B 无法读取用户 A 的项目缓存")

	// ======================================
	// 验证 Redis 不可用时回退 MySQL
	// ======================================

	// 获取一个临时端口并关闭监听，
	// 用于模拟 Redis 连接失败。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	offlineAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	offlineCache, err := cache.NewRedisCache(
		offlineAddr,
		"",
		0,
		"go-server:offline-test:"+suffix,
		200*time.Millisecond,
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = offlineCache.Close()
	})

	// 使用不可用的 Redis Client。
	offlineSvc := NewProjectServiceWithCache(
		countedDB,
		offlineCache,
	)

	before := counter.count.Load()

	// Redis GET 失败后，应回退 MySQL。
	result, err := offlineSvc.GetProject(
		ctx,
		user.ID,
		project.ID,
	)

	if err != nil {
		t.Fatalf("Redis down 时仍应查询 MySQL: %v", err)
	}

	if result == nil || result.ID != project.ID {
		t.Fatalf("故障降级结果不正确: %+v", result)
	}

	after := counter.count.Load()

	// 必须真正查询了 MySQL。
	if after-before != 1 {
		t.Fatalf(
			"Redis down 后预期新增1次SQL，实际%d次",
			after-before,
		)
	}

	t.Log("PASS: Redis 不可用时成功回退 MySQL")

}
