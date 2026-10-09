package repository

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"GoServer/internal/database"

	"gorm.io/gorm"
)

// 初始化测试数据库
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := database.InitDB()
	if err != nil {
		t.Fatalf("数据库初始化失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}

	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("关闭连接池失败: %v", err)
		}
	})

	return db
}

// 开启测试事务，测试结束自动回滚
func setupTestTx(t *testing.T) *gorm.DB {
	t.Helper()

	db := setupTestDB(t)

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("开启事务失败: %v", tx.Error)
	}

	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("回滚事务失败: %v", err)
		}
	})

	return tx
}

// 生成测试数据后缀
func testSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

// 测试 CreateUser：正常创建、重复邮箱、重复用户名
func TestCreateUser(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	suffix := testSuffix()

	user := &Users{
		UserName: "test_" + suffix,
		Email:    "test_" + suffix + "@example.com",
		Password: "test-hash",
	}

	// 1. 正常创建
	err := CreateUser(ctx, tx, user)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	if user.ID == 0 {
		t.Fatal("创建成功后 ID 未回填")
	}

	// 2. 重复邮箱
	duplicateEmail := &Users{
		UserName: "other_" + suffix,
		Email:    user.Email,
		Password: "test-hash",
	}

	err = CreateUser(ctx, tx, duplicateEmail)

	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf(
			"重复邮箱：期望 ErrAlreadyExists，实际 %v",
			err,
		)
	}

	// 3. 重复用户名
	duplicateName := &Users{
		UserName: user.UserName,
		Email:    "other_" + suffix + "@example.com",
		Password: "test-hash",
	}

	err = CreateUser(ctx, tx, duplicateName)

	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf(
			"重复用户名：期望 ErrAlreadyExists，实际 %v",
			err,
		)
	}
}

// 测试通过 ID 和用户名查询用户
func TestGetUser(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	suffix := testSuffix()

	user := &Users{
		UserName: "get_" + suffix,
		Email:    "get_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := CreateUser(ctx, tx, user); err != nil {
		t.Fatal(err)
	}

	// 1. 按 ID 查询
	got, err := GetUserByID(ctx, tx, user.ID)
	if err != nil {
		t.Fatalf("按 ID 查询失败: %v", err)
	}

	if got == nil || got.ID != user.ID {
		t.Fatal("按 ID 查询结果错误")
	}

	// 2. 按用户名查询
	got, err = GetUserByName(ctx, tx, user.UserName)
	if err != nil {
		t.Fatalf("按用户名查询失败: %v", err)
	}

	if got == nil || got.UserName != user.UserName {
		t.Fatal("按用户名查询结果错误")
	}

	// 3. 查询不存在的用户名
	got, err = GetUserByName(
		ctx, tx, "missing_"+suffix,
	)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	if got != nil {
		t.Fatal("不存在的用户不应该返回对象")
	}

	// 4. 查询不存在的 ID
	got, err = GetUserByID(ctx, tx, ^uint64(0))

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	if got != nil {
		t.Fatal("不存在的 ID 不应该返回用户")
	}
}

// 测试 ListUser：列表查询和排序
func TestListUser(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	suffix := testSuffix()

	userA := &Users{
		UserName: "listA_" + suffix,
		Email:    "listA_" + suffix + "@test.com",
		Password: "test-hash",
	}

	userB := &Users{
		UserName: "listB_" + suffix,
		Email:    "listB_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, user := range []*Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatal(err)
		}
	}

	users, err := ListUser(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}

	if users == nil {
		t.Fatal("成功查询不应该返回 nil 切片")
	}

	foundA := false
	foundB := false

	for i, user := range users {
		if user.ID == userA.ID {
			foundA = true
		}
		if user.ID == userB.ID {
			foundB = true
		}

		if i > 0 && users[i-1].ID > user.ID {
			t.Fatal("用户列表没有按 ID 升序排列")
		}
	}

	if !foundA || !foundB {
		t.Fatal("用户列表缺少测试创建的用户")
	}
}

// 测试 Context 提前取消
func TestCreateUser_CanceledContext(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	cancel()

	suffix := testSuffix()

	user := &Users{
		UserName: "cancel_" + suffix,
		Email:    "cancel_" + suffix + "@test.com",
		Password: "test-hash",
	}

	err := CreateUser(ctx, db, user)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"期望 context.Canceled，实际: %v",
			err,
		)
	}

	// 验证取消后的操作没有创建记录
	var count int64
	if err := db.Model(&Users{}).
		Where("username = ?", user.UserName).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatal("Context 取消后仍然插入了用户")
	}
}

// 测试 Context 截止时间已过
func TestCreateUser_DeadlineExceeded(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithDeadline(
		context.Background(),
		time.Now().Add(-time.Second),
	)
	defer cancel()

	suffix := testSuffix()

	user := &Users{
		UserName: "timeout_" + suffix,
		Email:    "timeout_" + suffix + "@test.com",
		Password: "test-hash",
	}

	err := CreateUser(ctx, db, user)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf(
			"期望 context.DeadlineExceeded，实际: %v",
			err,
		)
	}

	var count int64
	if err := db.Model(&Users{}).
		Where("username = ?", user.UserName).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatal("Context 超时后仍然插入了用户")
	}
}

// 测试用户查询的 Context 取消传播
func TestUserQueries_CanceledContext(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	cancel()

	user, err := GetUserByID(ctx, db, 1)

	if user != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("GetUserByID 取消处理错误: %v", err)
	}

	user, err = GetUserByName(ctx, db, "test")

	if user != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("GetUserByName 取消处理错误: %v", err)
	}

	users, err := ListUser(ctx, db)

	if users != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ListUser 取消处理错误: %v", err)
	}
}

func TestGORM_TimeoutDuringSQL(t *testing.T) {
	db := setupTestDB(t)

	// 设置 300ms 超时
	ctx, cancel := context.WithTimeout(
		context.Background(),
		300*time.Millisecond,
	)
	defer cancel()

	start := time.Now()

	// SQL 正常执行需要约 2 秒
	var result int

	err := db.WithContext(ctx).
		Raw("SELECT SLEEP(2)").
		Scan(&result).Error

	elapsed := time.Since(start)

	t.Logf("实际执行时间: %v", elapsed)
	t.Logf("数据库返回错误: %v", err)
	t.Logf("Context 错误: %v", ctx.Err())

	// 1. 验证 Context 确实超时
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("预期 Context 超时，实际: %v", ctx.Err())
	}

	// 2. 验证 SQL 调用返回了错误
	if err == nil {
		t.Fatal("SQL 超时后不应该成功")
	}

	// 3. 验证数据库调用及时退出
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf(
			"数据库取消响应过慢，耗时: %v",
			elapsed,
		)
	}
}
