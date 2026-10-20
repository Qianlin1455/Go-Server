package repository

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

// 使用真实 MySQL，每次测试开启事务，结束时回滚。
func setupProjectConversationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要配置 TEST_MYSQL_DSN")
	}

	db, err := gorm.Open(
		mysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}

	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("回滚测试事务失败: %v", err)
		}
	})

	return tx
}

func TestListProjectConversations(t *testing.T) {
	tx := setupProjectConversationTestDB(t)
	ctx := context.Background()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// =====================================
	// 1. 准备两个不同用户
	// =====================================

	createUser := func(name string) uint64 {
		t.Helper()

		user := model.Users{
			UserName: name + "_" + suffix,
			Email:    name + "_" + suffix + "@test.com",
			Password: "test-hash",
		}

		if err := tx.Create(&user).Error; err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}

		return user.ID
	}

	userA := createUser("repo_conversation_a")
	userB := createUser("repo_conversation_b")

	// =====================================
	// 2. 准备三个项目
	// =====================================

	createProject := func(
		userID uint64,
		name string,
	) uint64 {
		t.Helper()

		project := model.Project{
			UserID: userID,
			Name:   name + "_" + suffix,
			Status: model.ProjectStatusActive,
		}

		if err := tx.Create(&project).Error; err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}

		return project.ID
	}

	projectA := createProject(userA, "project_a")
	projectA2 := createProject(userA, "project_a_2")
	projectB := createProject(userB, "project_b")
	emptyProject := createProject(userA, "empty_project")

	// =====================================
	// 3. 准备会话测试数据
	// =====================================

	// 时间固定，确保排序测试可重复。
	base := time.Date(
		2026, 10, 20,
		10, 0, 0, 0,
		time.UTC,
	)

	createConversation := func(
		userID uint64,
		projectID *uint64,
		title string,
		createdAt time.Time,
	) uint64 {
		t.Helper()

		conversation := model.Conversation{
			UserID:    userID,
			ProjectID: projectID,
			Title:     title,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}

		if err := tx.Create(&conversation).Error; err != nil {
			t.Fatalf("创建会话失败: %v", err)
		}

		return conversation.ID
	}

	// 用户 A 的目标项目：
	// 前三个会话 created_at 相同，
	// 用于验证 id DESC 的第二排序键。
	sameTime := base.Add(2 * time.Minute)

	conv1 := createConversation(
		userA, &projectA, "conversation_1", sameTime,
	)

	conv2 := createConversation(
		userA, &projectA, "conversation_2", sameTime,
	)

	conv3 := createConversation(
		userA, &projectA, "conversation_3", sameTime,
	)

	// 创建时间更早，但 ID 更大
	conv4 := createConversation(
		userA,
		&projectA,
		"conversation_4",
		base.Add(time.Minute),
	)

	// 用户 A 的另一个项目
	otherConv := createConversation(
		userA,
		&projectA2,
		"other_project_conversation",
		sameTime,
	)

	// 用户 A 的历史会话：project_id = NULL
	legacyConv := createConversation(
		userA,
		nil,
		"legacy_conversation",
		sameTime,
	)

	// 用户 B 的项目和会话
	convB := createConversation(
		userB,
		&projectB,
		"user_b_conversation",
		sameTime,
	)

	// =====================================
	// 4. 辅助断言：严格比较 ID 顺序
	// =====================================

	assertIDs := func(
		t *testing.T,
		got []model.Conversation,
		want []uint64,
	) {
		t.Helper()

		if len(got) != len(want) {
			t.Fatalf(
				"结果数量错误: want=%d got=%d",
				len(want),
				len(got),
			)
		}

		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf(
					"index=%d: want ID=%d, got ID=%d",
					i,
					want[i],
					got[i].ID,
				)
			}
		}
	}

	// =====================================
	// 5. 正常查询 + 稳定排序
	// =====================================

	t.Run("正常查询和稳定排序", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, projectA, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		// created_at DESC, id DESC
		assertIDs(t, got, []uint64{
			conv3, conv2, conv1, conv4,
		})

		for _, c := range got {
			if c.UserID != userA {
				t.Fatalf("用户隔离失败: %+v", c)
			}

			if c.ProjectID == nil ||
				*c.ProjectID != projectA {
				t.Fatalf("项目隔离失败: %+v", c)
			}
		}
	})

	// =====================================
	// 6. 分页：第一页
	// =====================================

	t.Run("第一页", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, projectA, 1, 2,
		)
		if err != nil {
			t.Fatal(err)
		}

		assertIDs(t, got, []uint64{
			conv3, conv2,
		})
	})

	// =====================================
	// 7. 分页：第二页
	// =====================================

	t.Run("第二页", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, projectA, 2, 2,
		)
		if err != nil {
			t.Fatal(err)
		}

		assertIDs(t, got, []uint64{
			conv1, conv4,
		})
	})

	// =====================================
	// 8. 分页：超出最后一页
	// =====================================

	t.Run("超出最后一页", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, projectA, 3, 2,
		)
		if err != nil {
			t.Fatal(err)
		}

		if got == nil {
			t.Fatal("空页应该返回非 nil 切片")
		}

		if len(got) != 0 {
			t.Fatalf("期望空页，实际 %d 条", len(got))
		}
	})

	// =====================================
	// 9. 同用户不同项目隔离
	// =====================================

	t.Run("不同项目隔离", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, projectA2, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		assertIDs(t, got, []uint64{
			otherConv,
		})
	})

	// =====================================
	// 10. 不能查到其他用户的会话
	// =====================================

	t.Run("跨用户隔离", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userB, projectA, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf(
				"用户 B 不应查到用户 A 的会话: %+v",
				got,
			)
		}

		// 用户 B 查询自己的项目应该正常
		own, err := ListProjectConversations(
			ctx, tx, userB, projectB, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		assertIDs(t, own, []uint64{convB})
	})

	// =====================================
	// 11. 历史 NULL 会话不能混入
	// =====================================

	t.Run("排除历史NULL会话", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, projectA, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		for _, c := range got {
			if c.ID == legacyConv {
				t.Fatal("历史 NULL 会话混入项目查询")
			}
			if c.ID == otherConv || c.ID == convB {
				t.Fatal("查询混入了其他项目或用户的会话")
			}
		}

		assertIDs(t, got, []uint64{
			conv3, conv2, conv1, conv4,
		})
	})

	// =====================================
	// 12. 空项目返回非 nil 空切片
	// =====================================

	t.Run("空项目", func(t *testing.T) {
		got, err := ListProjectConversations(
			ctx, tx, userA, emptyProject, 1, 20,
		)
		if err != nil {
			t.Fatal(err)
		}

		if got == nil {
			t.Fatal("期望非 nil 空切片")
		}

		if len(got) != 0 {
			t.Fatalf(
				"空项目不应有会话，实际 %d 条",
				len(got),
			)
		}
	})

	// =====================================
	// 13. 非法参数和 OFFSET 溢出
	// =====================================

	t.Run("非法参数", func(t *testing.T) {
		maxInt := int(^uint(0) >> 1)

		cases := []struct {
			name    string
			userID  uint64
			projID  uint64
			page    int
			size    int
			wantErr error
		}{
			{
				name:    "userID为0",
				userID:  0,
				projID:  projectA,
				page:    1,
				size:    20,
				wantErr: ErrInvalidReference,
			},
			{
				name:    "projectID为0",
				userID:  userA,
				projID:  0,
				page:    1,
				size:    20,
				wantErr: ErrInvalidReference,
			},
			{
				name:    "page为0",
				userID:  userA,
				projID:  projectA,
				page:    0,
				size:    20,
				wantErr: ErrInvalidPagination,
			},
			{
				name:    "page为负数",
				userID:  userA,
				projID:  projectA,
				page:    -1,
				size:    20,
				wantErr: ErrInvalidPagination,
			},
			{
				name:    "pageSize为0",
				userID:  userA,
				projID:  projectA,
				page:    1,
				size:    0,
				wantErr: ErrInvalidPagination,
			},
			{
				name:    "pageSize为101",
				userID:  userA,
				projID:  projectA,
				page:    1,
				size:    101,
				wantErr: ErrInvalidPagination,
			},
			{
				name:    "OFFSET溢出",
				userID:  userA,
				projID:  projectA,
				page:    maxInt,
				size:    100,
				wantErr: ErrInvalidPagination,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := ListProjectConversations(
					ctx,
					tx,
					tc.userID,
					tc.projID,
					tc.page,
					tc.size,
				)

				if !errors.Is(err, tc.wantErr) {
					t.Fatalf(
						"%s: want %v, got %v",
						tc.name,
						tc.wantErr,
						err,
					)
				}
			})
		}
	})

	// =====================================
	// 14. Context 取消
	// =====================================

	t.Run("Context取消", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		_, err := ListProjectConversations(
			cancelCtx, tx, userA, projectA, 1, 20,
		)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"期望 context.Canceled，实际: %v",
				err,
			)
		}
	})

	t.Logf(
		"测试数据: userA=%d, userB=%d, projectA=%d",
		userA, userB, projectA,
	)
}
