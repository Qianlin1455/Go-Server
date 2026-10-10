package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/internal/security"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 创建测试专用 JWTManager
func newTestJWTManager(t *testing.T) *security.JWTManager {
	t.Helper()

	manager, err := security.NewJWTManager(
		strings.Repeat("a", 32),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("创建 JWTManager 失败: %v", err)
	}

	return manager
}

// ================================
// 1. 注册参数单元测试
// ================================

func TestRegister_InvalidInput(t *testing.T) {
	// 非法参数在访问数据库前就会被拦截
	svc := NewAuthService(nil, newTestJWTManager(t))

	tests := []struct {
		name  string
		input RegisterInput
	}{
		{
			name: "用户名太短",
			input: RegisterInput{
				Username: "ab",
				Email:    "alice@example.com",
				Password: "Alice@123456",
			},
		},
		{
			name: "邮箱为空",
			input: RegisterInput{
				Username: "alice",
				Email:    "",
				Password: "Alice@123456",
			},
		},
		{
			name: "密码太短",
			input: RegisterInput{
				Username: "alice",
				Email:    "alice@example.com",
				Password: "123",
			},
		},
		{
			name: "密码超过72字节",
			input: RegisterInput{
				Username: "alice",
				Email:    "alice@example.com",
				Password: strings.Repeat("a", 73),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Register(t.Context(), tt.input)

			if !errors.Is(err, ErrInvalidRegisterInput) {
				t.Fatalf(
					"期望 ErrInvalidRegisterInput，实际: %v",
					err,
				)
			}
		})
	}
}

// ================================
// 2. 注册 + 登录 + JWT 集成测试
// ================================

func TestRegister_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置测试数据库 TEST_MYSQL_DSN")
	}

	// 1. 初始化测试数据库
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("数据库连接失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// 测试结束时关闭连接池
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("关闭连接池失败: %v", err)
		}
	})

	// 2. 初始化 JWTManager 和 AuthService
	jwtManager := newTestJWTManager(t)
	svc := NewAuthService(db, jwtManager)

	ctx := t.Context()

	// 3. 为测试生成唯一用户
	username := fmt.Sprintf("test_%d", time.Now().UnixNano())
	email := username + "@example.com"
	password := "Alice@123456"

	// 测试结束时删除测试数据
	// Cleanup 按后进先出执行，因此会先删数据再关闭连接池
	t.Cleanup(func() {
		err := db.Exec(
			"DELETE FROM users WHERE username = ?",
			username,
		).Error

		if err != nil {
			t.Errorf("清理测试数据失败: %v", err)
		}
	})

	// ================================
	// 测试 1：正常注册
	// ================================

	t.Run("正常注册", func(t *testing.T) {
		result, err := svc.Register(ctx, RegisterInput{
			Username: username,
			Email:    email,
			Password: password,
		})

		if err != nil {
			t.Fatalf("注册失败: %v", err)
		}

		if result == nil || result.ID == 0 {
			t.Fatal("注册没有返回有效用户 ID")
		}
	})

	// ================================
	// 测试 2：验证数据库密码哈希
	// ================================

	t.Run("密码哈希验证", func(t *testing.T) {
		var storedHash string

		err := db.Raw(
			"SELECT password_hash FROM users WHERE username = ?",
			username,
		).Row().Scan(&storedHash)

		if err != nil {
			t.Fatal(err)
		}

		if storedHash == password {
			t.Fatal("数据库存储了明文密码")
		}

		if err := security.CheckPassword(storedHash, password); err != nil {
			t.Fatalf("数据库密码哈希无法验证: %v", err)
		}
	})

	// ================================
	// 测试 3：重复用户名
	// ================================

	t.Run("重复用户名", func(t *testing.T) {
		_, err := svc.Register(ctx, RegisterInput{
			Username: username,
			Email:    username + "_another@example.com",
			Password: password,
		})

		if !errors.Is(err, ErrUserAlreadyExists) {
			t.Fatalf("重复用户名未被拒绝: %v", err)
		}
	})

	// ================================
	// 测试 4：重复邮箱
	// ================================

	t.Run("重复邮箱", func(t *testing.T) {
		_, err := svc.Register(ctx, RegisterInput{
			Username: username + "_other",
			Email:    email,
			Password: password,
		})

		if !errors.Is(err, ErrUserAlreadyExists) {
			t.Fatalf("重复邮箱未被拒绝: %v", err)
		}
	})

	// ================================
	// 测试 5：正确密码登录 + JWT 验证
	// ================================

	t.Run("正常登录并签发JWT", func(t *testing.T) {
		loginResult, err := svc.Login(ctx, LoginInput{
			Email:    email,
			Password: password,
		})

		if err != nil {
			t.Fatalf("登录失败: %v", err)
		}

		if loginResult == nil {
			t.Fatal("登录结果为空")
		}

		// 检查是否签发 JWT
		if loginResult.AccessToken == "" {
			t.Fatal("登录成功但未返回 JWT")
		}

		// 检查 Token 类型
		if loginResult.TokenType != "Bearer" {
			t.Fatalf(
				"TokenType 错误: %s",
				loginResult.TokenType,
			)
		}

		// 检查有效期
		if loginResult.ExpiresIn != 3600 {
			t.Fatalf(
				"ExpiresIn 错误: %d",
				loginResult.ExpiresIn,
			)
		}

		// 解析 JWT
		userID, err := jwtManager.Verify(
			loginResult.AccessToken,
		)
		if err != nil {
			t.Fatalf("登录返回的 JWT 无法验证: %v", err)
		}

		// 查询数据库中的真实用户 ID
		var storedUserID uint64

		err = db.Raw(
			"SELECT id FROM users WHERE username = ?",
			username,
		).Row().Scan(&storedUserID)

		if err != nil {
			t.Fatal(err)
		}

		// JWT 中的用户 ID 必须与数据库一致
		if userID != storedUserID {
			t.Fatalf(
				"JWT 用户 ID 错误，期望=%d，实际=%d",
				storedUserID,
				userID,
			)
		}
	})

	// ================================
	// 测试 6：错误密码登录
	// ================================

	t.Run("错误密码登录", func(t *testing.T) {
		result, err := svc.Login(ctx, LoginInput{
			Email:    email,
			Password: "WrongPassword123",
		})

		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("错误密码未被正确拒绝: %v", err)
		}

		if result != nil {
			t.Fatal("错误密码不应该返回登录结果")
		}
	})

	// ================================
	// 测试 7：不存在的邮箱
	// ================================

	t.Run("不存在的邮箱", func(t *testing.T) {
		result, err := svc.Login(ctx, LoginInput{
			Email:    username + "_missing@example.com",
			Password: password,
		})

		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("不存在的邮箱未被拒绝: %v", err)
		}

		if result != nil {
			t.Fatal("不存在的用户不应该获得登录结果")
		}
	})
}
