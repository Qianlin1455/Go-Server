package repository

import (
	"strings"
	"testing"

	"GoServer/internal/model"
	"gorm.io/gorm"
)

func TestMessageQuery_Parameterized(t *testing.T) {
	tx := setupTestTx(t)

	input := "' OR 1=1 --"

	var messages []model.Message

	stmt := tx.Session(
		&gorm.Session{DryRun: true},
	).
		Model(&model.Message{}).
		Where("messages.content = ?", input).
		Find(&messages).
		Statement

	// 1. SQL 应保留参数占位符
	if !strings.Contains(stmt.SQL.String(), "?") {
		t.Fatal("SQL 中未找到参数占位符")
	}

	// 2. 恶意输入应作为参数传递
	if len(stmt.Vars) != 1 {
		t.Fatalf("期望 1 个参数，实际 %d 个", len(stmt.Vars))
	}

	if stmt.Vars[0] != input {
		t.Fatal("查询参数与输入不一致")
	}
}
