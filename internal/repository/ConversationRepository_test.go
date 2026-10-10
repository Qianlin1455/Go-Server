package repository

import (
	"GoServer/internal/model"
	"context"
	"errors"
	"testing"
	"time"
)

// 测试：创建会话时引用不存在的用户
func TestCreateConversation_InvalidUser(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	const invalidUserID uint64 = 0

	var count int64

	err := tx.Model(&model.Users{}).
		Where("id = ?", invalidUserID).
		Count(&count).Error

	if err != nil {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatal("测试用户 ID 已存在")
	}

	con := &model.Conversation{
		UserID: invalidUserID,
		Title:  "Test Conversation",
	}

	err = CreateConversation(ctx, tx, con)

	if !errors.Is(err, ErrInvalidReference) {
		t.Fatalf(
			"期望 ErrInvalidReference，实际: %v",
			err,
		)
	}
}

// 测试：只能查询属于自己的会话
func TestGetConversationByID_Isolation(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	suffix := testSuffix()

	userA := &model.Users{
		UserName: "userA_" + suffix,
		Email:    "a_" + suffix + "@test.com",
		Password: "test-hash",
	}

	userB := &model.Users{
		UserName: "userB_" + suffix,
		Email:    "b_" + suffix + "@test.com",
		Password: "test-hash",
	}

	// 创建两个用户
	if err := CreateUser(ctx, tx, userA); err != nil {
		t.Fatal(err)
	}

	if err := CreateUser(ctx, tx, userB); err != nil {
		t.Fatal(err)
	}

	// 用户 B 创建一条会话
	con := &model.Conversation{
		UserID: userB.ID,
		Title:  "Private Conversation",
	}

	if err := CreateConversation(ctx, tx, con); err != nil {
		t.Fatal(err)
	}

	if con.ID == 0 {
		t.Fatal("会话 ID 未回填")
	}

	// 1. B 查询自己的会话
	got, err := GetConversationByID(
		ctx, tx, userB.ID, con.ID,
	)

	if err != nil {
		t.Fatalf("用户 B 查询失败: %v", err)
	}

	if got == nil || got.ID != con.ID {
		t.Fatal("查询结果不正确")
	}

	// 2. A 查询 B 的会话，必须返回 ErrNotFound
	got, err = GetConversationByID(
		ctx, tx, userA.ID, con.ID,
	)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf(
			"期望 ErrNotFound，实际: %v",
			err,
		)
	}

	if got != nil {
		t.Fatal("数据隔离失败：返回了其他用户的会话")
	}
}

// 测试：ListConversation 的隔离、排序和空列表
func TestListConversation_Isolation(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	suffix := testSuffix()

	userA := &model.Users{
		UserName: "listA_" + suffix,
		Email:    "listA_" + suffix + "@test.com",
		Password: "test-hash",
	}

	userB := &model.Users{
		UserName: "listB_" + suffix,
		Email:    "listB_" + suffix + "@test.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatal(err)
		}
	}

	// 只给用户 B 创建两条会话
	for _, title := range []string{
		"Go学习",
		"MySQL学习",
	} {
		con := &model.Conversation{
			UserID: userB.ID,
			Title:  title,
		}

		if err := CreateConversation(ctx, tx, con); err != nil {
			t.Fatal(err)
		}
	}

	// 1. 查询 B 的会话
	listB, err := ListConversation(ctx, tx, userB.ID)

	if err != nil {
		t.Fatal(err)
	}

	if len(listB) != 2 {
		t.Fatalf(
			"期望 2 条会话，实际 %d 条",
			len(listB),
		)
	}

	// 2. 验证数据隔离
	for _, con := range listB {
		if con.UserID != userB.ID {
			t.Fatal("数据隔离失败")
		}
	}

	// 3. 验证 ID 升序
	if listB[0].ID >= listB[1].ID {
		t.Fatal("会话没有按照 ID 升序排列")
	}

	// 4. A 没有会话，应该返回空切片
	listA, err := ListConversation(ctx, tx, userA.ID)

	if err != nil {
		t.Fatal(err)
	}

	if listA == nil || len(listA) != 0 {
		t.Fatal("空列表处理错误")
	}
}

// 测试 ConversationRepository 的 Context 取消
func TestConversation_CanceledContext(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	cancel()

	// 1. CreateConversation
	con := &model.Conversation{
		UserID: 0,
		Title:  "Canceled Test",
	}

	err := CreateConversation(ctx, db, con)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"CreateConversation 期望 context.Canceled，实际 %v",
			err,
		)
	}

	// 2. GetConversationByID
	got, err := GetConversationByID(ctx, db, 1, 1)

	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"GetConversationByID 取消处理错误: %v",
			err,
		)
	}

	// 3. ListConversation
	list, err := ListConversation(ctx, db, 1)

	if list != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"ListConversation 取消处理错误: %v",
			err,
		)
	}
}

// 测试 ConversationRepository 的 Context 超时
func TestConversation_DeadlineExceeded(t *testing.T) {
	db := setupTestDB(t)

	// 创建一个截止时间已经过去的 Context
	ctx, cancel := context.WithDeadline(
		context.Background(),
		time.Now().Add(-time.Second),
	)
	defer cancel()

	// 1. CreateConversation
	con := &model.Conversation{
		UserID: 0,
		Title:  "Timeout Test",
	}

	err := CreateConversation(ctx, db, con)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf(
			"CreateConversation 期望 DeadlineExceeded，实际 %v",
			err,
		)
	}

	// 2. GetConversationByID
	got, err := GetConversationByID(ctx, db, 1, 1)

	if got != nil ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf(
			"GetConversationByID 超时处理错误: %v",
			err,
		)
	}

	// 3. ListConversation
	list, err := ListConversation(ctx, db, 1)

	if list != nil ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf(
			"ListConversation 超时处理错误: %v",
			err,
		)
	}
}
