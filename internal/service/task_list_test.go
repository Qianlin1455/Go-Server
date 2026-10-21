package service

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"GoServer/internal/model"
	"GoServer/internal/repository"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestListTasks_Integration verifies Service-level ownership, pagination,
// ordering, archived-project reads, empty results, and argument validation.
// All fixtures are created inside a transaction that is rolled back afterward.
func TestListTasks_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("需要设置 TEST_MYSQL_DSN，且必须指向 go_server_test")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Fail closed: never write fixtures into go_db or another database.
	var databaseName string
	if err := db.Raw("SELECT DATABASE()").Scan(&databaseName).Error; err != nil {
		t.Fatalf("确认数据库名称失败: %v", err)
	}
	if databaseName != "go_server_test" {
		t.Fatalf("拒绝在 %q 上运行集成测试；仅允许 go_server_test", databaseName)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("开启测试事务失败: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("回滚测试事务失败: %v", err)
		}
	})

	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	userA := &model.Users{
		UserName: "list_service_a_" + suffix,
		Email:    "list_service_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "list_service_b_" + suffix,
		Email:    "list_service_b_" + suffix + "@example.com",
		Password: "test-hash",
	}
	for _, u := range []*model.Users{userA, userB} {
		if err := repository.CreateUser(ctx, tx, u); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	projectA := &model.Project{UserID: userA.ID, Name: "list_main_" + suffix}
	projectA2 := &model.Project{UserID: userA.ID, Name: "list_other_" + suffix}
	emptyProject := &model.Project{UserID: userA.ID, Name: "list_empty_" + suffix}
	archivedProject := &model.Project{UserID: userA.ID, Name: "list_archived_" + suffix}
	projectB := &model.Project{UserID: userB.ID, Name: "list_b_" + suffix}

	for _, p := range []*model.Project{
		projectA, projectA2, emptyProject, archivedProject, projectB,
	} {
		if err := repository.CreateProject(ctx, tx, p); err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
	}

	// Give two tasks an identical created_at to test the id DESC tie-breaker.
	base := time.Date(2026, time.October, 20, 10, 0, 0, 0, time.UTC)
	task1 := &model.Task{UserID: userA.ID, ProjectID: projectA.ID, Title: "task1", CreatedAt: base}
	task2 := &model.Task{UserID: userA.ID, ProjectID: projectA.ID, Title: "task2", CreatedAt: base.Add(time.Minute)}
	task3 := &model.Task{UserID: userA.ID, ProjectID: projectA.ID, Title: "task3", CreatedAt: base.Add(time.Minute)}
	task4 := &model.Task{UserID: userA.ID, ProjectID: projectA.ID, Title: "task4", CreatedAt: base.Add(-time.Minute)}
	taskA2 := &model.Task{UserID: userA.ID, ProjectID: projectA2.ID, Title: "other project"}
	taskB := &model.Task{UserID: userB.ID, ProjectID: projectB.ID, Title: "user B"}
	archivedTask := &model.Task{UserID: userA.ID, ProjectID: archivedProject.ID, Title: "archived task"}

	for _, task := range []*model.Task{task1, task2, task3, task4, taskA2, taskB, archivedTask} {
		if err := repository.CreateTask(ctx, tx, task); err != nil {
			t.Fatalf("创建 Task 失败: %v", err)
		}
	}

	// Archive only after the task exists: reading archived projects is allowed.
	update := tx.Model(&model.Project{}).
		Where("id = ? AND user_id = ?", archivedProject.ID, userA.ID).
		Update("status", model.ProjectStatusArchived)
	if update.Error != nil || update.RowsAffected != 1 {
		t.Fatalf("归档项目失败: rows=%d err=%v", update.RowsAffected, update.Error)
	}

	// Service is backed by the same test transaction so it can read fixtures.
	svc := NewTaskService(tx)

	// Repository uses created_at DESC, id DESC.
	pagination := []struct {
		name    string
		page    int
		size    int
		wantIDs []uint64
	}{
		{"第一页", 1, 2, []uint64{task3.ID, task2.ID}},
		{"第二页", 2, 2, []uint64{task1.ID, task4.ID}},
		{"超出末页", 3, 2, []uint64{}},
		{"自定义分页", 1, 3, []uint64{task3.ID, task2.ID, task1.ID}},
	}
	for _, tc := range pagination {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ListTasks(ctx, userA.ID, projectA.ID, tc.page, tc.size)
			if err != nil {
				t.Fatalf("ListTasks 失败: %v", err)
			}
			if got == nil {
				t.Fatal("合法查询应该返回非 nil 切片")
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("数量不正确: want=%d got=%d", len(tc.wantIDs), len(got))
			}
			for i, task := range got {
				if task.ID != tc.wantIDs[i] {
					t.Errorf("顺序错误 index=%d: want=%d got=%d", i, tc.wantIDs[i], task.ID)
				}
				if task.ProjectID != projectA.ID || task.Status != model.TaskStatusTodo || task.Version != 1 {
					t.Errorf("DTO 内容不正确: %+v", task)
				}
			}
		})
	}

	t.Run("空项目返回非nil空列表", func(t *testing.T) {
		got, err := svc.ListTasks(ctx, userA.ID, emptyProject.ID, 1, 20)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("期望非nil空列表: got=%+v err=%v", got, err)
		}
	})

	t.Run("同用户不同项目隔离", func(t *testing.T) {
		got, err := svc.ListTasks(ctx, userA.ID, projectA2.ID, 1, 20)
		if err != nil || len(got) != 1 || got[0].ID != taskA2.ID {
			t.Fatalf("同用户项目隔离失败: got=%+v err=%v", got, err)
		}
	})

	t.Run("另一个用户查询自己项目", func(t *testing.T) {
		got, err := svc.ListTasks(ctx, userB.ID, projectB.ID, 1, 20)
		if err != nil || len(got) != 1 || got[0].ID != taskB.ID {
			t.Fatalf("用户B查询失败: got=%+v err=%v", got, err)
		}
	})

	t.Run("归档项目仍允许查询", func(t *testing.T) {
		got, err := svc.ListTasks(ctx, userA.ID, archivedProject.ID, 1, 20)
		if err != nil || len(got) != 1 || got[0].ID != archivedTask.ID {
			t.Fatalf("归档项目应该可读: got=%+v err=%v", got, err)
		}
	})

	// Create and delete a project to obtain a definitely non-existent ID.
	ghostProject := &model.Project{UserID: userA.ID, Name: "list_ghost_" + suffix}
	if err := repository.CreateProject(ctx, tx, ghostProject); err != nil {
		t.Fatalf("创建临时项目失败: %v", err)
	}
	if err := tx.Delete(&model.Project{}, ghostProject.ID).Error; err != nil {
		t.Fatalf("删除临时项目失败: %v", err)
	}

	t.Run("项目不存在", func(t *testing.T) {
		got, err := svc.ListTasks(ctx, userA.ID, ghostProject.ID, 1, 20)
		if !errors.Is(err, ErrProjectNotFound) || got != nil {
			t.Fatalf("期望 ErrProjectNotFound: got=%+v err=%v", got, err)
		}
	})

	t.Run("跨用户项目拒绝访问", func(t *testing.T) {
		got, err := svc.ListTasks(ctx, userA.ID, projectB.ID, 1, 20)
		if !errors.Is(err, ErrProjectNotFound) || got != nil {
			t.Fatalf("期望 ErrProjectNotFound: got=%+v err=%v", got, err)
		}
	})

	invalid := []struct {
		name      string
		userID    uint64
		projectID uint64
		page      int
		size      int
		want      error
	}{
		{"用户ID为零", 0, projectA.ID, 1, 20, ErrInvalidTaskInput},
		{"项目ID为零", userA.ID, 0, 1, 20, ErrInvalidTaskInput},
		{"page为零", userA.ID, projectA.ID, 0, 20, ErrInvalidPagination},
		{"page为负数", userA.ID, projectA.ID, -1, 20, ErrInvalidPagination},
		{"pageSize为零", userA.ID, projectA.ID, 1, 0, ErrInvalidPagination},
		{"pageSize为负数", userA.ID, projectA.ID, 1, -1, ErrInvalidPagination},
		{"pageSize超过100", userA.ID, projectA.ID, 1, 101, ErrInvalidPagination},
		{"OFFSET整数溢出", userA.ID, projectA.ID, int(^uint(0) >> 1), 100, ErrInvalidPagination},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ListTasks(ctx, tc.userID, tc.projectID, tc.page, tc.size)
			if !errors.Is(err, tc.want) || got != nil {
				t.Fatalf("期望 %v: got=%+v err=%v", tc.want, got, err)
			}
		})
	}
}
