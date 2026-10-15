package midware_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"GoServer/internal/database"

	"gorm.io/gorm"
)

func TestTimeoutRollsBackTransaction(t *testing.T) {
	if os.Getenv("MYSQL_DSN") == "" {
		t.Fatal("MYSQL_DSN 未设置")
	}

	db, err := database.InitDB()
	if err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	// 只能在专用测试库执行
	var dbName string
	if err := db.Raw("SELECT DATABASE()").
		Row().Scan(&dbName); err != nil {
		t.Fatal(err)
	}

	username := fmt.Sprintf("timeout_%d", time.Now().UnixNano())
	email := username + "@example.test"

	// 防御性清理：即使测试失败，也不保留测试用户
	defer db.Exec("DELETE FROM users WHERE username = ?", username)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		300*time.Millisecond,
	)
	defer cancel()

	inserted := false
	start := time.Now()

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// ① 先在事务内插入用户
		if err := tx.Exec(
			"INSERT INTO users (username, email, password_hash) VALUES (?, ?, ?)",
			username, email, "integration-test-only",
		).Error; err != nil {
			return err
		}

		inserted = true

		// ② 模拟需要 2 秒的数据库操作
		var result int
		return tx.Raw("SELECT SLEEP(2)").
			Scan(&result).Error
	})

	elapsed := time.Since(start)

	t.Logf("Transaction elapsed: %s", elapsed)
	t.Logf("Transaction error: %v", err)
	t.Logf("Context error: %v", ctx.Err())

	if !inserted {
		t.Fatal("插入步骤没有成功执行")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("预期超时，实际: %v", ctx.Err())
	}
	if err == nil {
		t.Fatal("事务意外提交成功")
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("事务未及时返回: %s", elapsed)
	}

	// ③ 使用新的、未取消的 Context 查询
	var count int64
	if err := db.WithContext(context.Background()).
		Table("users").
		Where("username = ?", username).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}

	t.Logf("Rows remaining after rollback: %d", count)

	if count != 0 {
		t.Fatalf("回滚失败：仍有 %d 条记录", count)
	}
}
