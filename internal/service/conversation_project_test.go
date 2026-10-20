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

func TestListProjectConversationsService(t *testing.T) {
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

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	ctx := context.Background()
	svc := NewConversationService(tx)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// 1. 准备用户
	createUser := func(name string) uint64 {
		t.Helper()

		u := model.Users{
			UserName: name + suffix,
			Email:    name + suffix + "@test.com",
			Password: "test-hash",
		}
		if err := tx.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		return u.ID
	}

	userA := createUser("svc_conv_a_")
	userB := createUser("svc_conv_b_")

	// 2. 准备项目
	createProject := func(userID uint64, name string) uint64 {
		t.Helper()

		p := model.Project{
			UserID: userID,
			Name:   name + suffix,
			Status: model.ProjectStatusActive,
		}
		if err := tx.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	projectA := createProject(userA, "svc_project_a_")
	projectA2 := createProject(userA, "svc_project_a2_")
	projectB := createProject(userB, "svc_project_b_")
	emptyProject := createProject(userA, "svc_empty_")

	// 创建后删除，得到一个确定不存在的项目 ID
	missing := model.Project{
		UserID: userA,
		Name:   "svc_missing_" + suffix,
		Status: model.ProjectStatusActive,
	}
	if err := tx.Create(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Delete(&missing).Error; err != nil {
		t.Fatal(err)
	}

	// 3. 准备会话
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	createConv := func(
		userID uint64,
		projectID *uint64,
		title string,
		createdAt time.Time,
	) uint64 {
		t.Helper()

		c := model.Conversation{
			UserID:    userID,
			ProjectID: projectID,
			Title:     title,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}
		if err := tx.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
		return c.ID
	}

	// 同一时间创建，验证 id DESC
	conv1 := createConv(userA, &projectA, "会话1", base)
	conv2 := createConv(userA, &projectA, "会话2", base)

	// 干扰数据：其他项目、用户和历史 NULL 会话
	createConv(userA, &projectA2, "其他项目", base)
	createConv(userB, &projectB, "用户B的会话", base)
	createConv(userA, nil, "历史会话", base)

	// 4. 正常查询
	t.Run("正常查询", func(t *testing.T) {
		got, err := svc.ListProjectConversations(
			ctx, userA, projectA, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("期望2条，实际%d条", len(got))
		}

		if got[0].ID != conv2 || got[1].ID != conv1 {
			t.Fatalf("排序或隔离错误: %+v", got)
		}

		for _, c := range got {
			if c.ProjectID == nil ||
				*c.ProjectID != projectA {
				t.Fatalf("ProjectID错误: %+v", c)
			}
		}
	})

	// 5. 验证分页
	t.Run("分页", func(t *testing.T) {
		first, err := svc.ListProjectConversations(
			ctx, userA, projectA, 1, 1,
		)
		if err != nil {
			t.Fatal(err)
		}

		second, err := svc.ListProjectConversations(
			ctx, userA, projectA, 2, 1,
		)
		if err != nil {
			t.Fatal(err)
		}

		if len(first) != 1 || first[0].ID != conv2 {
			t.Fatalf("第一页错误: %+v", first)
		}
		if len(second) != 1 || second[0].ID != conv1 {
			t.Fatalf("第二页错误: %+v", second)
		}
	})

	// 6. 项目存在但是没有会话
	t.Run("空项目返回空数组", func(t *testing.T) {
		got, err := svc.ListProjectConversations(
			ctx, userA, emptyProject, 1, 20,
		)

		if err != nil {
			t.Fatalf("空项目不应报错: %v", err)
		}

		if got == nil || len(got) != 0 {
			t.Fatalf("期望非nil空数组，实际: %+v", got)
		}
	})

	// 7. 用户 A 访问用户 B 的项目
	t.Run("跨用户项目返回NotFound", func(t *testing.T) {
		got, err := svc.ListProjectConversations(
			ctx, userA, projectB, 1, 20,
		)

		if !errors.Is(err, ErrProjectNotFound) {
			t.Fatalf("期望ErrProjectNotFound，实际: %v", err)
		}

		if got != nil {
			t.Fatalf("越权不能返回会话: %+v", got)
		}
	})

	// 8. 项目不存在
	t.Run("不存在的项目返回NotFound", func(t *testing.T) {
		got, err := svc.ListProjectConversations(
			ctx, userA, missing.ID, 1, 20,
		)

		if !errors.Is(err, ErrProjectNotFound) {
			t.Fatalf("期望ErrProjectNotFound，实际: %v", err)
		}

		if got != nil {
			t.Fatal("不存在的项目不应返回会话")
		}
	})

	// 9. 验证用户 B 正常查询自己的项目
	t.Run("用户B查询自己的项目", func(t *testing.T) {
		got, err := svc.ListProjectConversations(
			ctx, userB, projectB, 1, 20,
		)

		if err != nil || len(got) != 1 {
			t.Fatalf("用户B查询失败: got=%+v err=%v", got, err)
		}
	})

	// 10. 参数校验
	t.Run("非法参数", func(t *testing.T) {
		maxInt := int(^uint(0) >> 1)

		cases := []struct {
			name     string
			userID   uint64
			projID   uint64
			page     int
			pageSize int
			wantErr  error
		}{
			{"userID为0", 0, projectA, 1, 20, ErrInvalidProjectInput},
			{"projectID为0", userA, 0, 1, 20, ErrInvalidProjectInput},
			{"page为0", userA, projectA, 0, 20, ErrInvalidPagination},
			{"pageSize为0", userA, projectA, 1, 0, ErrInvalidPagination},
			{"pageSize超限", userA, projectA, 1, 101, ErrInvalidPagination},
			{"OFFSET溢出", userA, projectA, maxInt, 100, ErrInvalidPagination},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := svc.ListProjectConversations(
					ctx, tc.userID, tc.projID,
					tc.page, tc.pageSize,
				)

				if !errors.Is(err, tc.wantErr) {
					t.Fatalf(
						"期望 %v，实际 %v",
						tc.wantErr, err,
					)
				}

				if got != nil {
					t.Fatalf("非法参数不应返回数据: %+v", got)
				}
			})
		}
	})

	// 11. Context 取消
	t.Run("Context取消", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		got, err := svc.ListProjectConversations(
			cancelCtx, userA, projectA, 1, 20,
		)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("期望context.Canceled，实际: %v", err)
		}
		if got != nil {
			t.Fatal("取消后的请求不应返回结果")
		}
	})

	t.Logf(
		"测试完成: userA=%d, projectA=%d",
		userA, projectA,
	)
}
