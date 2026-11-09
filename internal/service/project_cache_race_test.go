package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"GoServer/internal/cache"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// ============================================================
// Test Cache Wrappers
// ============================================================

// blockingSetProjectCache:
// 第一次执行 Set 时暂停，控制缓存回填时序。
type blockingSetProjectCache struct {
	ProjectCache

	entered chan struct{}
	release chan struct{}

	once        sync.Once
	releaseOnce sync.Once
}

func (c *blockingSetProjectCache) Set(
	ctx context.Context,
	key string,
	value []byte,
) error {
	c.once.Do(func() {
		close(c.entered)

		select {
		case <-c.release:
		case <-ctx.Done():
		}
	})

	return c.ProjectCache.Set(ctx, key, value)
}

func (c *blockingSetProjectCache) unblock() {
	c.releaseOnce.Do(func() {
		close(c.release)
	})
}

// deleteFailProjectCache:
// 模拟 Redis 删除失败。
type deleteFailProjectCache struct {
	ProjectCache
	err error
}

func (c *deleteFailProjectCache) Delete(
	ctx context.Context,
	key string,
) error {
	return c.err
}

// ============================================================
// Test Fixture
// ============================================================

type projectCacheRaceFixture struct {
	db         *gorm.DB
	redisCache *cache.RedisCache

	user    *model.Users
	project *model.Project

	key string
}

func newProjectCacheRaceFixture(
	t *testing.T,
) *projectCacheRaceFixture {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	redisAddr := os.Getenv("TEST_REDIS_ADDR")

	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN 未设置")
	}

	if redisAddr == "" {
		t.Skip("TEST_REDIS_ADDR 未设置")
	}

	ctx := context.Background()

	// --------------------------------------------------------
	// MySQL
	// --------------------------------------------------------

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{
			TranslateError: true,
		},
	)
	if err != nil {
		t.Fatalf("连接 MySQL 失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)

	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	// 确保只操作测试数据库。
	var dbName string

	if err := db.Raw(
		"SELECT DATABASE()",
	).Scan(&dbName).Error; err != nil {
		t.Fatal(err)
	}

	if dbName != "go_server_test" {
		t.Fatalf(
			"拒绝在非测试数据库上运行: %s",
			dbName,
		)
	}

	// --------------------------------------------------------
	// Redis
	// --------------------------------------------------------

	suffix := strconv.FormatInt(
		time.Now().UnixNano(),
		10,
	)

	redisCache, err := cache.NewRedisCache(
		redisAddr,
		"",
		0,
		"go-server:race-test:"+suffix,
		200*time.Millisecond,
		5*time.Minute,
	)
	if err != nil {
		t.Fatalf("创建 Redis Cache 失败: %v", err)
	}

	if err := redisCache.Ping(ctx); err != nil {
		t.Fatalf("Redis Ping 失败: %v", err)
	}

	t.Cleanup(func() {
		_ = redisCache.Close()
	})

	// --------------------------------------------------------
	// User
	// --------------------------------------------------------

	user := &model.Users{
		UserName: "race_user_" + suffix,
		Email:    "race_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := repository.CreateUser(
		ctx,
		db,
		user,
	); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()

		_ = db.WithContext(cleanupCtx).
			Delete(&model.Users{}, user.ID).Error
	})

	// --------------------------------------------------------
	// Project
	// --------------------------------------------------------

	project := &model.Project{
		UserID: user.ID,
		Name:   "old_project_" + suffix,
		Status: model.ProjectStatusActive,
	}

	if err := repository.CreateProject(
		ctx,
		db,
		project,
	); err != nil {
		t.Fatalf("创建测试项目失败: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()

		_ = db.WithContext(cleanupCtx).
			Delete(&model.Project{}, project.ID).Error
	})

	key := fmt.Sprintf(
		"project:%d:%d",
		user.ID,
		project.ID,
	)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()

		_ = redisCache.Delete(cleanupCtx, key)
	})

	return &projectCacheRaceFixture{
		db:         db,
		redisCache: redisCache,
		user:       user,
		project:    project,
		key:        key,
	}
}

// ============================================================
// MySQL Lock Wait Detection
// ============================================================

// waitForProjectLockWait:
// 只检查当前项目 PRIMARY KEY 的 InnoDB 行锁等待。
// 不使用整个 MySQL 实例的全局锁等待数量。
func waitForProjectLockWait(
	t *testing.T,
	db *gorm.DB,
	projectID uint64,
	updateDone <-chan error,
) {
	t.Helper()

	deadline := time.Now().Add(1 * time.Second)

	projectIDText := strconv.FormatUint(
		projectID,
		10,
	)

	for time.Now().Before(deadline) {

		// 更新请求提前完成，说明没有观察到预期阻塞。
		select {
		case err := <-updateDone:
			t.Fatalf(
				"更新请求在观察到行锁等待前结束: %v",
				err,
			)
		default:
		}

		var waits int64

		err := db.Raw(`
			SELECT COUNT(*)
			FROM performance_schema.data_lock_waits w
			JOIN performance_schema.data_locks r
			  ON r.ENGINE = w.ENGINE
			 AND r.ENGINE_LOCK_ID =
			     w.REQUESTING_ENGINE_LOCK_ID
			WHERE r.OBJECT_SCHEMA = DATABASE()
			  AND r.OBJECT_NAME = 'projects'
			  AND r.INDEX_NAME = 'PRIMARY'
			  AND r.LOCK_TYPE = 'RECORD'
			  AND r.LOCK_STATUS = 'WAITING'
			  AND r.LOCK_DATA = ?
		`, projectIDText).Scan(&waits).Error

		if err != nil {
			t.Fatalf(
				"查询 MySQL InnoDB 锁等待失败: %v",
				err,
			)
		}

		if waits > 0 {
			t.Log(
				"PASS: 检测到当前项目真实 InnoDB 行锁等待",
			)
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal(
		"没有观察到当前项目预期的 InnoDB 行锁等待",
	)
}

// ============================================================
// Test 1:
// Shared Lock Protects Cache Backfill
// ============================================================

func TestGetProject_SharedLockBlocksUpdateUntilCacheFill(
	t *testing.T,
) {
	f := newProjectCacheRaceFixture(t)

	oldName := f.project.Name
	newName := "new_project_" +
		strconv.FormatInt(time.Now().UnixNano(), 10)

	blockingCache := &blockingSetProjectCache{
		ProjectCache: f.redisCache,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}

	svc := NewProjectServiceWithCache(
		f.db,
		blockingCache,
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	var wg sync.WaitGroup

	// 即使测试提前失败，也释放阻塞并等待后台请求退出。
	defer func() {
		blockingCache.unblock()
		cancel()

		done := make(chan struct{})

		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("后台测试 goroutine 未能及时退出")
		}
	}()

	type readResult struct {
		project *ProjectResult
		err     error
	}

	readDone := make(chan readResult, 1)

	// --------------------------------------------------------
	// Request A: Cache MISS -> MySQL FOR SHARE -> Redis SET
	// --------------------------------------------------------

	wg.Add(1)

	go func() {
		defer wg.Done()

		got, err := svc.GetProject(
			ctx,
			f.user.ID,
			f.project.ID,
		)

		readDone <- readResult{
			project: got,
			err:     err,
		}
	}()

	// 等待 A 进入 Redis Set，并暂停。
	select {
	case <-blockingCache.entered:
		t.Log(
			"PASS: 请求 A 已读取旧数据，并暂停在 Redis Set",
		)

	case result := <-readDone:
		t.Fatalf(
			"请求 A 未进入缓存回填: project=%+v err=%v",
			result.project,
			result.err,
		)

	case <-time.After(3 * time.Second):
		t.Fatal(
			"请求 A 未能进入缓存回填阶段",
		)
	}

	// --------------------------------------------------------
	// Request B: MySQL FOR UPDATE
	// --------------------------------------------------------

	updateDone := make(chan error, 1)

	wg.Add(1)

	go func() {
		defer wg.Done()

		_, err := svc.UpdateProjectName(
			ctx,
			f.user.ID,
			f.project.ID,
			newName,
		)

		updateDone <- err
	}()

	// 必须观察到当前项目的真实行锁等待。
	// 此时 A 仍然暂停，持有 FOR SHARE 锁。
	waitForProjectLockWait(
		t,
		f.db,
		f.project.ID,
		updateDone,
	)

	// --------------------------------------------------------
	// Verify B is blocked
	// --------------------------------------------------------

	select {
	case err := <-updateDone:
		t.Fatalf(
			"请求 B 不应该在请求 A 释放共享锁前完成: %v",
			err,
		)

	default:
		t.Log(
			"PASS: 请求 B 正在等待 MySQL 共享锁释放",
		)
	}

	// --------------------------------------------------------
	// Release A
	// --------------------------------------------------------

	blockingCache.unblock()

	// A 应当完成 Redis Set 和事务提交。
	// A 返回旧快照是允许的。
	var result readResult

	select {
	case result = <-readDone:
		if result.err != nil {
			t.Fatalf(
				"请求 A 读取失败: %v",
				result.err,
			)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("请求 A 等待超时")
	}

	if result.project == nil {
		t.Fatal("请求 A 返回空项目")
	}

	if result.project.Name != oldName {
		t.Fatalf(
			"请求 A 快照异常: got=%s want=%s",
			result.project.Name,
			oldName,
		)
	}

	t.Log(
		"PASS: 请求 A 完成缓存回填并释放共享锁",
	)

	// --------------------------------------------------------
	// Verify B succeeds
	// --------------------------------------------------------

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf(
				"请求 B 更新失败: %v",
				err,
			)
		}

	case <-time.After(3 * time.Second):
		t.Fatal("请求 B 更新等待超时")
	}

	t.Log(
		"PASS: 请求 B 获得排他锁，完成数据库更新和缓存失效",
	)

	// --------------------------------------------------------
	// Verify cache invalidation
	// --------------------------------------------------------

	_, hit, err := f.redisCache.Get(
		ctx,
		f.key,
	)
	if err != nil {
		t.Fatalf(
			"检查 Redis 缓存失败: %v",
			err,
		)
	}

	if hit {
		t.Fatal(
			"项目更新提交后，Redis 中仍然存在旧缓存",
		)
	}

	t.Log(
		"PASS: 更新提交后旧缓存已失效",
	)

	// --------------------------------------------------------
	// Request C: Must read new value
	// --------------------------------------------------------

	got, err := svc.GetProject(
		ctx,
		f.user.ID,
		f.project.ID,
	)
	if err != nil {
		t.Fatalf(
			"请求 C 读取失败: %v",
			err,
		)
	}

	if got.Name != newName {
		t.Fatalf(
			"请求 C 读取到错误数据: got=%s want=%s",
			got.Name,
			newName,
		)
	}

	t.Log(
		"PASS: 请求 C 读取到最新项目数据",
	)
}

// ============================================================
// Test 2:
// Redis Delete Failure Must Roll Back MySQL
// ============================================================

func TestUpdateProjectName_CacheDeleteFailureRollsBack(
	t *testing.T,
) {
	f := newProjectCacheRaceFixture(t)

	oldName := f.project.Name
	newName := "should_not_commit_" +
		strconv.FormatInt(time.Now().UnixNano(), 10)

	cacheErr := errors.New(
		"simulated redis delete failure",
	)

	failingCache := &deleteFailProjectCache{
		ProjectCache: f.redisCache,
		err:          cacheErr,
	}

	svc := NewProjectServiceWithCache(
		f.db,
		failingCache,
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	// --------------------------------------------------------
	// Attempt update
	// --------------------------------------------------------

	_, err := svc.UpdateProjectName(
		ctx,
		f.user.ID,
		f.project.ID,
		newName,
	)

	if err == nil {
		t.Fatal(
			"Redis 删除失败时，更新操作应该返回错误",
		)
	}

	if !errors.Is(err, cacheErr) {
		t.Fatalf(
			"错误原因没有正确传播: got=%v",
			err,
		)
	}

	t.Log(
		"PASS: Redis 删除失败，Service 返回错误",
	)

	// --------------------------------------------------------
	// Verify MySQL rollback
	// --------------------------------------------------------

	persisted, err := repository.GetProjectByID(
		ctx,
		f.db,
		f.user.ID,
		f.project.ID,
	)
	if err != nil {
		t.Fatalf(
			"重新读取项目失败: %v",
			err,
		)
	}

	if persisted.Name != oldName {
		t.Fatalf(
			"MySQL 事务未正确回滚: got=%s want=%s",
			persisted.Name,
			oldName,
		)
	}

	if persisted.Name == newName {
		t.Fatal(
			"Redis 删除失败后，MySQL 不应该提交新名称",
		)
	}

	t.Log(
		"PASS: Redis 删除失败，MySQL 更新事务已回滚",
	)
}
