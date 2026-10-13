package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"GoServer/internal/model"
)

// ============================================================
// 1. 消息隔离测试
// 验证：
// - A 能读取自己的消息
// - A 不能读取 B 的消息
// - B 能读取自己的消息
// ============================================================

func TestListMessages_Isolation(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建用户 A、B
	userA := &model.Users{
		UserName: "message_A_" + suffix,
		Email:    "message_A_" + suffix + "@test.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "message_B_" + suffix,
		Email:    "message_B_" + suffix + "@test.com",
		Password: "test-hash",
	}

	if err := CreateUser(ctx, tx, userA); err != nil {
		t.Fatalf("创建用户 A 失败: %v", err)
	}
	if err := CreateUser(ctx, tx, userB); err != nil {
		t.Fatalf("创建用户 B 失败: %v", err)
	}

	// 2. 创建各自的 Conversation
	conA := &model.Conversation{
		UserID: userA.ID,
		Title:  "User A Private Conversation",
	}
	conB := &model.Conversation{
		UserID: userB.ID,
		Title:  "User B Private Conversation",
	}

	if err := CreateConversation(ctx, tx, conA); err != nil {
		t.Fatalf("创建会话 A 失败: %v", err)
	}
	if err := CreateConversation(ctx, tx, conB); err != nil {
		t.Fatalf("创建会话 B 失败: %v", err)
	}

	// 3. 创建消息
	msgA1 := &model.Message{
		ConversationID: conA.ID,
		Role:           "user",
		Content:        "Hello from User A - 1",
	}
	msgA2 := &model.Message{
		ConversationID: conA.ID,
		Role:           "user",
		Content:        "Hello from User A - 2",
	}
	msgB1 := &model.Message{
		ConversationID: conB.ID,
		Role:           "user",
		Content:        "Hello from User B - 1",
	}

	for _, msg := range []*model.Message{
		msgA1, msgA2, msgB1,
	} {
		if err := CreateMessage(ctx, tx, msg); err != nil {
			t.Fatalf("创建消息失败: %v", err)
		}
	}

	// 4. A 查询自己的消息
	messagesA, err := ListMessages(
		ctx, tx, userA.ID, conA.ID, 1, 10,
	)
	if err != nil {
		t.Fatalf("A 查询自己的消息失败: %v", err)
	}

	if len(messagesA) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d 条", len(messagesA))
	}

	if messagesA[0].ID != msgA1.ID ||
		messagesA[1].ID != msgA2.ID {
		t.Fatal("A 的消息 ID 或排序不正确")
	}

	for _, msg := range messagesA {
		if msg.ConversationID != conA.ID {
			t.Fatal("A 查询到了其他会话的消息")
		}
	}

	// 5. A 越权查询 B 的消息
	messagesCross, err := ListMessages(
		ctx, tx, userA.ID, conB.ID, 1, 10,
	)
	if err != nil {
		t.Fatalf("跨用户查询执行失败: %v", err)
	}

	if len(messagesCross) != 0 {
		t.Fatalf(
			"数据隔离失败：A 查询 B 的会话返回 %d 条消息",
			len(messagesCross),
		)
	}

	// 6. B 查询自己的消息
	messagesB, err := ListMessages(
		ctx, tx, userB.ID, conB.ID, 1, 10,
	)
	if err != nil {
		t.Fatalf("B 查询自己的消息失败: %v", err)
	}

	if len(messagesB) != 1 {
		t.Fatalf("期望 B 有 1 条消息，实际 %d 条", len(messagesB))
	}

	if messagesB[0].ID != msgB1.ID {
		t.Fatalf(
			"B 消息 ID 不匹配：期望 %d，实际 %d",
			msgB1.ID,
			messagesB[0].ID,
		)
	}

	if messagesB[0].ConversationID != conB.ID {
		t.Fatal("B 查询到了错误会话的消息")
	}

	t.Log("PASS: Repository 用户数据隔离正确")
}

// ============================================================
// 2. 消息分页测试
// 验证：
// - 第一页
// - 第二页
// - 最后一页不足 pageSize
// - 超出最后一页返回空切片
// - 稳定排序
// ============================================================

func TestListMessages_Pagination(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建测试用户
	userC := &model.Users{
		UserName: "User_C_" + suffix,
		Email:    "User_C_" + suffix + "@test.com",
		Password: "pass_hash",
	}

	if err := CreateUser(ctx, tx, userC); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 2. 创建 Conversation
	conC := &model.Conversation{
		UserID: userC.ID,
		Title:  "UserC-1 conversation",
	}

	if err := CreateConversation(ctx, tx, conC); err != nil {
		t.Fatalf("创建 Conversation 失败: %v", err)
	}

	// 3. 创建 5 条消息并保存真实 ID
	messageIDs := make([]uint64, 0, 5)

	for i := 1; i <= 5; i++ {
		msg := &model.Message{
			ConversationID: conC.ID,
			Role:           "user",
			Content:        fmt.Sprintf("Message %d", i),
		}

		if err := CreateMessage(ctx, tx, msg); err != nil {
			t.Fatalf("创建第 %d 条消息失败: %v", i, err)
		}

		messageIDs = append(messageIDs, msg.ID)
	}

	// 4. 定义分页测试用例
	tests := []struct {
		name     string
		page     int
		pageSize int
		wantIDs  []uint64
	}{
		{
			name:     "第一页",
			page:     1,
			pageSize: 2,
			wantIDs:  messageIDs[0:2],
		},
		{
			name:     "第二页",
			page:     2,
			pageSize: 2,
			wantIDs:  messageIDs[2:4],
		},
		{
			name:     "最后一页",
			page:     3,
			pageSize: 2,
			wantIDs:  messageIDs[4:5],
		},
		{
			name:     "空页",
			page:     4,
			pageSize: 2,
			wantIDs:  []uint64{},
		},
		{
			name:     "pageSize大于总数量",
			page:     1,
			pageSize: 10,
			wantIDs:  messageIDs,
		},
	}

	// 5. 执行表驱动测试
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			messages, err := ListMessages(
				ctx,
				tx,
				userC.ID,
				conC.ID,
				tt.page,
				tt.pageSize,
			)

			// 检查 SQL 执行是否成功
			if err != nil {
				t.Fatalf(
					"分页查询失败 page=%d pageSize=%d: %v",
					tt.page,
					tt.pageSize,
					err,
				)
			}

			// 检查消息数量
			if len(messages) != len(tt.wantIDs) {
				t.Fatalf(
					"期望 %d 条消息，实际 %d 条",
					len(tt.wantIDs),
					len(messages),
				)
			}

			// 检查每条消息的 ID 和顺序
			for i, msg := range messages {
				if msg.ID != tt.wantIDs[i] {
					t.Fatalf(
						"第 %d 条消息 ID 错误：期望 %d，实际 %d",
						i,
						tt.wantIDs[i],
						msg.ID,
					)
				}

				// 检查会话归属
				if msg.ConversationID != conC.ID {
					t.Fatalf(
						"消息 %d 不属于目标会话 %d",
						msg.ID,
						conC.ID,
					)
				}
			}

			// 空页应返回非 nil 的空切片
			if len(tt.wantIDs) == 0 && messages == nil {
				t.Fatal("空页期望返回空切片，而不是 nil")
			}
		})
	}

	t.Log("PASS: 分页数量、顺序、边界均符合预期")
}

func TestListMessages_InvalidPagination(t *testing.T) {
	ctx := context.Background()
	maxInt := int(^uint(0) >> 1)

	tests := []struct {
		name     string
		page     int
		pageSize int
	}{
		{"page为0", 0, 20},
		{"pageSize为负数", 1, -1},
		{"pageSize超过上限", 1, 101},
		{"offset溢出", maxInt, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages, err := ListMessages(
				ctx,
				nil,
				1,
				1,
				tt.page,
				tt.pageSize,
			)

			if !errors.Is(err, ErrInvalidPagination) {
				t.Fatalf(
					"期望 ErrInvalidPagination，实际: %v",
					err,
				)
			}

			if messages != nil {
				t.Fatalf("非法分页不应返回消息")
			}
		})
	}
}
