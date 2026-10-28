package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
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

	// ======================================
	// 验证项目更新后删除缓存
	// ======================================

	newName := "cache_updated_" + suffix

	// 1. 更新项目名称
	updated, err := svc.UpdateProjectName(
		ctx, user.ID, project.ID, newName,
	)
	if err != nil {
		t.Fatalf("更新项目失败: %v", err)
	}
	if updated.Name != newName {
		t.Fatalf("更新返回值错误: %s", updated.Name)
	}

	// 2. 验证旧缓存已经被删除
	key := fmt.Sprintf("project:%d:%d", user.ID, project.ID)
	_, hit, err = redisCache.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("UPDATE 后旧缓存仍然存在")
	}

	// 3. 再次读取，应当重新查询 MySQL
	beforeGet := counter.count.Load()

	got, err = svc.GetProject(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != newName {
		t.Fatalf("读取到旧数据: %s", got.Name)
	}

	afterGet := counter.count.Load()
	if afterGet-beforeGet != 1 {
		t.Fatalf("预期重新查询1次MySQL，实际%d次",
			afterGet-beforeGet)
	}

	// 4. 再读一次，应直接命中新缓存
	_, err = svc.GetProject(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counter.count.Load() != afterGet {
		t.Fatal("更新缓存重建后仍然查询了MySQL")
	}

	t.Log("PASS: 更新后删除缓存，重新读取新值")

}

func TestGetProject_RejectsPoisonedCache(t *testing.T) {
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

	// 防止误操作业务数据库
	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Scan(&dbName).Error; err != nil {
		t.Fatal(err)
	}
	if dbName != "go_server_test" {
		t.Fatalf("拒绝在 %s 上运行测试", dbName)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// 1. 创建两个不同的用户
	userA := &model.Users{
		UserName: "poison_a_" + suffix,
		Email:    "poison_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "poison_b_" + suffix,
		Email:    "poison_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := repository.CreateUser(ctx, tx, userA); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateUser(ctx, tx, userB); err != nil {
		t.Fatal(err)
	}

	// 2. 项目只属于用户 A
	project := &model.Project{
		UserID: userA.ID,
		Name:   "private_project_" + suffix,
		Status: model.ProjectStatusActive,
	}
	if err := repository.CreateProject(ctx, tx, project); err != nil {
		t.Fatal(err)
	}

	// 统计真实的 Project SELECT 次数
	counter := &projectSelectCounter{
		Interface: logger.Default,
	}
	countedDB := tx.Session(&gorm.Session{
		Logger: counter,
	})

	redisCache, err := cache.NewRedisCache(
		redisAddr, "", 0,
		"go-server:poison-test:"+suffix,
		200*time.Millisecond,
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	keyA := fmt.Sprintf("project:%d:%d", userA.ID, project.ID)
	keyB := fmt.Sprintf("project:%d:%d", userB.ID, project.ID)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 2*time.Second,
		)
		defer cancel()

		_ = redisCache.Delete(cleanupCtx, keyA)
		_ = redisCache.Delete(cleanupCtx, keyB)
		_ = redisCache.Close()
	})

	if err := redisCache.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	svc := NewProjectServiceWithCache(countedDB, redisCache)

	// 3. A 正常读取项目，产生真实缓存
	resultA, err := svc.GetProject(ctx, userA.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resultA.ID != project.ID {
		t.Fatal("用户 A 读取项目失败")
	}

	payloadA, hit, err := redisCache.Get(ctx, keyA)
	if err != nil || !hit {
		t.Fatalf("用户 A 缓存未建立: hit=%v err=%v", hit, err)
	}

	// 4. 模拟缓存污染：
	// 将 A 的真实缓存内容写到 B 的缓存 Key
	if err := redisCache.Set(ctx, keyB, payloadA); err != nil {
		t.Fatal(err)
	}

	_, hit, err = redisCache.Get(ctx, keyB)
	if err != nil || !hit {
		t.Fatalf("污染缓存未写入成功: %v", err)
	}

	// 5. B 尝试读取 A 的项目
	before := counter.count.Load()

	resultB, err := svc.GetProject(ctx, userB.ID, project.ID)

	after := counter.count.Load()

	// 6. 绝对不能泄漏 A 的项目
	if resultB != nil {
		t.Fatalf("跨用户缓存泄漏: %+v", resultB)
	}
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("预期 ErrProjectNotFound，实际: %v", err)
	}

	// 拒绝污染缓存后必须回源验证归属
	if after-before != 1 {
		t.Fatalf(
			"预期回源 MySQL 1 次，实际 %d 次",
			after-before,
		)
	}

	t.Log("PASS: 缓存污染未造成跨用户数据泄漏")

	// 缓存穿透负例：连续请求不存在的项目
	missingID := project.ID + 1_000_000_000

	// 确认测试目标确实不存在
	var exists int64
	if err := tx.Model(&model.Project{}).
		Where("id = ?", missingID).
		Count(&exists).Error; err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Fatal("测试目标项目已存在")
	}

	beforeMissing := counter.count.Load()

	for i := 0; i < 3; i++ {
		got, err := svc.GetProject(
			ctx, userB.ID, missingID,
		)

		if !errors.Is(err, ErrProjectNotFound) {
			t.Fatalf("第%d次请求错误: %v", i+1, err)
		}
		if got != nil {
			t.Fatalf("不存在的项目返回数据: %+v", got)
		}
	}

	afterMissing := counter.count.Load()

	t.Logf(
		"连续3次请求不存在项目，MySQL SELECT增加: %d",
		afterMissing-beforeMissing,
	)

	// 当前未实现负缓存，预期每次都回源
	if afterMissing-beforeMissing != 3 {
		t.Fatalf("预期3次查询，实际%d次",
			afterMissing-beforeMissing)
	}

}

func TestGetProject_ConcurrentMissBaseline(t *testing.T) {
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

	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Scan(&dbName).Error; err != nil {
		t.Fatal(err)
	}
	if dbName != "go_server_test" {
		t.Fatalf("拒绝在 %s 上运行测试", dbName)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	user := &model.Users{
		UserName: "miss_user_" + suffix,
		Email:    "miss_" + suffix + "@example.com",
		Password: "test-hash",
	}
	if err := repository.CreateUser(ctx, db, user); err != nil {
		t.Fatal(err)
	}

	project := &model.Project{
		UserID: user.ID,
		Name:   "miss_project_" + suffix,
		Status: model.ProjectStatusActive,
	}

	// 数据真实提交，多个连接可以并发查询。
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		if err := db.WithContext(cleanupCtx).
			Where("id = ? AND user_id = ?", project.ID, user.ID).
			Delete(&model.Project{}).Error; err != nil {
			t.Errorf("清理项目失败: %v", err)
		}
		if err := db.WithContext(cleanupCtx).
			Where("id = ?", user.ID).
			Delete(&model.Users{}).Error; err != nil {
			t.Errorf("清理用户失败: %v", err)
		}
	})

	if err := repository.CreateProject(ctx, db, project); err != nil {
		t.Fatal(err)
	}

	counter := &projectSelectCounter{
		Interface: logger.Default.LogMode(logger.Silent),
	}
	countedDB := db.Session(&gorm.Session{
		Logger: counter,
	})

	redisCache, err := cache.NewRedisCache(
		redisAddr, "", 0,
		"go-server:miss-test:"+suffix,
		500*time.Millisecond,
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	key := fmt.Sprintf("project:%d:%d", user.ID, project.ID)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 2*time.Second,
		)
		defer cancel()
		_ = redisCache.Delete(cleanupCtx, key)
		_ = redisCache.Close()
	})

	if err := redisCache.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	svc := NewProjectServiceWithCache(countedDB, redisCache)

	const workers = 50
	const rounds = 3

	for round := 1; round <= rounds; round++ {
		// 每轮都制造一个全新的 Cache Miss。
		if err := redisCache.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}

		before := counter.count.Load()

		start := make(chan struct{})
		errCh := make(chan error, workers)
		var wg sync.WaitGroup

		for i := 0; i < workers; i++ {
			wg.Add(1)

			go func() {
				defer wg.Done()

				// 所有 goroutine 等待统一信号
				<-start

				got, err := svc.GetProject(
					ctx, user.ID, project.ID,
				)
				if err != nil {
					errCh <- err
					return
				}
				if got == nil || got.ID != project.ID {
					errCh <- fmt.Errorf("返回项目不正确")
				}
			}()
		}

		// 同时放行 50 个请求
		close(start)
		wg.Wait()
		close(errCh)

		for err := range errCh {
			t.Errorf("并发请求失败: %v", err)
		}
		if t.Failed() {
			return
		}

		delta := counter.count.Load() - before

		t.Logf(
			"第%d轮: 并发请求=%d, MySQL SELECT=%d",
			round, workers, delta,
		)

		if delta < 1 {
			t.Fatalf("缓存已删除但未观察到回源查询")
		}
	}

	// ==========================================
	// 不同 Project Key 的并发执行实验
	// ==========================================

	// 1. 创建第二个 Project
	projectB := &model.Project{
		UserID: user.ID,
		Name:   "parallel_project_" + suffix,
		Status: model.ProjectStatusActive,
	}

	if err := repository.CreateProject(ctx, db, projectB); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()

		if err := db.WithContext(cleanupCtx).
			Where("id = ? AND user_id = ?", projectB.ID, user.ID).
			Delete(&model.Project{}).Error; err != nil {
			t.Errorf("清理 Project B 失败: %v", err)
		}
	})

	keyB := fmt.Sprintf(
		"project:%d:%d", user.ID, projectB.ID,
	)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(), 2*time.Second,
		)
		defer cancel()
		_ = redisCache.Delete(cleanupCtx, keyB)
	})

	// 2. 确保两个 Key 都没有缓存
	for _, k := range []string{key, keyB} {
		if err := redisCache.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
	}

	// 3. 设置查询前的同步点
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var hookCount atomic.Int64

	hookName := "test:hold-project-query"

	err = countedDB.Callback().Query().
		Before("gorm:query").
		Register(hookName, func(tx *gorm.DB) {
			if tx.Statement.Table != "projects" {
				return
			}

			// 只拦截最先进入的两次查询
			if hookCount.Add(1) <= 2 {
				entered <- struct{}{}

				select {
				case <-release:
				case <-tx.Statement.Context.Done():
				}
			}
		})

	if err != nil {
		t.Fatal(err)
	}
	defer countedDB.Callback().Query().Remove(hookName)

	before := counter.count.Load()

	// 4. 发起 50 个请求，两个 Project 各 25 个
	const total = 50

	start := make(chan struct{})
	errCh := make(chan error, total)
	var wg sync.WaitGroup

	for i := 0; i < total; i++ {
		id := project.ID
		if i%2 == 1 {
			id = projectB.ID
		}

		wg.Add(1)
		go func(projectID uint64) {
			defer wg.Done()
			<-start

			got, err := svc.GetProject(
				ctx, user.ID, projectID,
			)
			if err != nil {
				errCh <- err
				return
			}

			if got == nil || got.ID != projectID {
				errCh <- fmt.Errorf(
					"项目返回错误: expected=%d",
					projectID,
				)
			}
		}(id)
	}

	close(start)

	// 5. 检查两个查询是否能够同时进入
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for i := 0; i < 2; i++ {
		select {
		case <-entered:
			t.Logf("第%d个查询已进入同步点", i+1)

		case <-timer.C:
			close(release)
			wg.Wait()
			t.Fatal("不同 Key 未能同时进入查询阶段")
		}
	}

	// 两个查询都已进入，放行执行
	close(release)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("并发请求失败: %v", err)
	}
	if t.Failed() {
		return
	}

	// 6. 统计真实 SQL 次数
	delta := counter.count.Load() - before

	t.Logf(
		"不同Key并发: 请求=%d, MySQL SELECT=%d",
		total, delta,
	)

	if delta != 2 {
		t.Fatalf(
			"预期两个不同Key各查询1次，实际%d次",
			delta,
		)
	}

}
