package service

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"GoServer/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestSendMessage_RollbackWhenTouchFails(t *testing.T) {
	// 1. 连接专用测试数据库
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置 TEST_MYSQL_DSN")
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
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 2. 创建独立测试用户
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	user := model.Users{
		UserName: "rollback_" + suffix,
		Email:    "rollback_" + suffix + "@test.com",
		Password: "test-hash",
	}

	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	// 测试结束清理用户和关联数据
	t.Cleanup(func() {
		if err := db.Exec(
			"DELETE FROM users WHERE id = ?", user.ID,
		).Error; err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	// 3. 创建测试会话
	conversation := model.Conversation{
		UserID: user.ID,
		Title:  "Rollback Test",
	}

	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}

	// 记录原始 updated_at
	var before model.Conversation
	if err := db.First(&before, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}

	// 4. 设置故障注入
	forcedErr := errors.New("forced touch failure")

	messageInserted := false
	touchReached := false

	// 消息 INSERT 执行后记录成功标记
	err = db.Callback().Create().
		After("gorm:create").
		Register("test:message_inserted", func(tx *gorm.DB) {
			if tx.Statement.Schema != nil &&
				tx.Statement.Schema.Table == "messages" &&
				tx.Error == nil {
				messageInserted = true
			}
		})
	if err != nil {
		t.Fatal(err)
	}

	// 在 conversation UPDATE 执行前主动报错
	err = db.Callback().Update().
		Before("gorm:update").
		Register("test:fail_touch", func(tx *gorm.DB) {
			if tx.Statement.Schema != nil &&
				tx.Statement.Schema.Table == "conversations" {
				touchReached = true
				tx.AddError(forcedErr)
			}
		})
	if err != nil {
		t.Fatal(err)
	}

	// 5. 调用真实业务方法
	svc := &MessageService{db: db}
	content := "rollback-test-message"

	result, err := svc.SendMessage(
		context.Background(),
		user.ID,
		SendMessageInput{
			ConversationID: conversation.ID,
			Content:        content,
		},
	)

	// 6. 验证确实经过了消息插入
	if !messageInserted {
		t.Fatal("消息 INSERT 未成功执行")
	}

	// 验证确实到达了第二次写入
	if !touchReached {
		t.Fatal("没有执行到 TouchConversation")
	}

	// 验证返回预期错误
	if !errors.Is(err, forcedErr) {
		t.Fatalf("want forcedErr, got %v", err)
	}

	if result != nil {
		t.Fatal("事务失败不应返回 MessageResult")
	}

	// 7. 从数据库验证消息已经回滚
	var count int64
	err = db.Model(&model.Message{}).
		Where(
			"conversation_id = ? AND content = ?",
			conversation.ID, content,
		).
		Count(&count).Error

	if err != nil {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatalf("事务回滚失败，残留 %d 条消息", count)
	}

	// 8. 验证会话更新时间未变化
	var after model.Conversation
	if err := db.First(&after, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}

	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("事务失败后 updated_at 发生变化")
	}

	t.Log("PASS: INSERT 成功，UPDATE 注入失败，事务整体回滚")
}

func TestSendMessage_Commit(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置 TEST_MYSQL_DSN")
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
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 1. 准备独立测试数据
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	user := model.Users{
		UserName: "commit_" + suffix,
		Email:    "commit_" + suffix + "@test.com",
		Password: "test-hash",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := db.Delete(&user).Error; err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	conversation := model.Conversation{
		UserID: user.ID,
		Title:  "Commit Test",
	}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}

	// 2. 将更新时间设为一小时前，避免毫秒精度干扰
	oldTime := time.Now().Add(-time.Hour)

	err = db.Model(&conversation).
		UpdateColumn("updated_at", oldTime).Error
	if err != nil {
		t.Fatal(err)
	}

	// 3. 调用真实 SendMessage
	svc := NewMessageService(db)

	result, err := svc.SendMessage(
		context.Background(),
		user.ID,
		SendMessageInput{
			ConversationID: conversation.ID,
			Content:        "commit-test-message",
		},
	)

	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if result == nil || result.ID == 0 {
		t.Fatal("没有返回有效 MessageResult")
	}

	// 4. 使用原始 db 查询，证明消息已经提交
	var saved model.Message

	err = db.First(&saved, result.ID).Error
	if err != nil {
		t.Fatalf("message not committed: %v", err)
	}

	if saved.Content != "commit-test-message" {
		t.Fatal("消息内容不一致")
	}

	if saved.ConversationID != conversation.ID {
		t.Fatal("消息会话关联不正确")
	}

	// 5. 验证会话更新时间
	var updated model.Conversation

	err = db.First(&updated, conversation.ID).Error
	if err != nil {
		t.Fatal(err)
	}

	if !updated.UpdatedAt.After(oldTime) {
		t.Fatal("conversation.updated_at 未更新")
	}

	t.Log("PASS: 消息插入成功，会话时间更新，事务提交")
}

func TestListMessages_InvalidPagination(t *testing.T) {
	ctx := context.Background()

	// 当前平台 int 类型的最大值
	maxInt := int(^uint(0) >> 1)

	// 故意不连接数据库
	// 非法参数应该在 Service 层直接被拒绝
	svc := NewMessageService(nil)

	// 表驱动测试
	tests := []struct {
		name     string
		page     int
		pageSize int
	}{
		{
			name:     "page等于0",
			page:     0,
			pageSize: 20,
		},
		{
			name:     "page为负数",
			page:     -1,
			pageSize: 20,
		},
		{
			name:     "pageSize等于0",
			page:     1,
			pageSize: 0,
		},
		{
			name:     "pageSize为负数",
			page:     1,
			pageSize: -1,
		},
		{
			name:     "pageSize超过100",
			page:     1,
			pageSize: 101,
		},
		{
			name:     "OFFSET整数溢出",
			page:     maxInt,
			pageSize: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			// 调用 Service
			messages, err := svc.ListMessages(
				ctx,
				1, // userID
				1, // conversationID
				tt.page,
				tt.pageSize,
			)

			// 断言 1：必须返回 ErrInvalidPagination
			if !errors.Is(err, ErrInvalidPagination) {
				t.Fatalf(
					"page=%d, pageSize=%d: 期望 ErrInvalidPagination，实际错误: %v",
					tt.page,
					tt.pageSize,
					err,
				)
			}

			// 断言 2：非法分页不能返回消息
			if messages != nil {
				t.Fatalf(
					"page=%d, pageSize=%d: 期望 nil，实际返回: %+v",
					tt.page,
					tt.pageSize,
					messages,
				)
			}
		})
	}
}
