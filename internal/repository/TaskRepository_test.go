package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"GoServer/internal/model"
)

func TestCreateTask(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建用户 A 和用户 B
	userA := &model.Users{
		UserName: "task_a_" + suffix,
		Email:    "task_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "task_b_" + suffix,
		Email:    "task_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
	}

	// 2. 分别创建 A、B 的项目
	projectA := &model.Project{
		UserID: userA.ID,
		Name:   "task_project_a_" + suffix,
	}
	projectB := &model.Project{
		UserID: userB.ID,
		Name:   "task_project_b_" + suffix,
	}

	for _, project := range []*model.Project{projectA, projectB} {
		if err := CreateProject(ctx, tx, project); err != nil {
			t.Fatalf("创建测试项目失败: %v", err)
		}
	}

	// 3. 正常创建：A 在自己的项目下创建 Task
	t.Run("正常创建", func(t *testing.T) {
		task := &model.Task{
			UserID:    userA.ID,
			ProjectID: projectA.ID,
			Title:     "实现 JWT 中间件",
		}

		err := CreateTask(ctx, tx, task)
		if err != nil {
			t.Fatalf("创建 Task 失败: %v", err)
		}

		// 验证自增主键回填
		if task.ID == 0 {
			t.Fatal("Task ID 未回填")
		}

		// 重新查询数据库，验证 INSERT 真正执行
		var stored model.Task

		err = tx.WithContext(ctx).
			Where(
				"id = ? AND user_id = ? AND project_id = ?",
				task.ID, userA.ID, projectA.ID,
			).
			First(&stored).Error

		if err != nil {
			t.Fatalf("数据库中未找到创建的 Task: %v", err)
		}

		if stored.Title != task.Title {
			t.Fatalf(
				"Task 标题不正确: want=%s, got=%s",
				task.Title, stored.Title,
			)
		}

		// 验证默认状态
		if stored.Status != model.TaskStatusTodo {
			t.Fatalf(
				"默认状态错误: want=todo, got=%s",
				stored.Status,
			)
		}

		// 验证初始版本
		if stored.Version != 1 {
			t.Fatalf(
				"初始版本错误: want=1, got=%d",
				stored.Version,
			)
		}
	})

	// 4. A 尝试向 B 的项目添加 Task
	t.Run("跨用户项目越权", func(t *testing.T) {
		task := &model.Task{
			UserID:    userA.ID,
			ProjectID: projectB.ID,
			Title:     "非法跨用户任务",
		}

		err := CreateTask(ctx, tx, task)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"期望 ErrInvalidReference，实际: %v",
				err,
			)
		}
	})

	// 5. 使用确定不存在的 ProjectID
	t.Run("项目不存在", func(t *testing.T) {
		// 先创建项目，再删除，确保其 ID 不存在
		ghostProject := &model.Project{
			UserID: userA.ID,
			Name:   "ghost_project_" + suffix,
		}

		if err := CreateProject(ctx, tx, ghostProject); err != nil {
			t.Fatalf("创建临时项目失败: %v", err)
		}

		result := tx.WithContext(ctx).
			Delete(&model.Project{}, ghostProject.ID)

		if result.Error != nil {
			t.Fatalf("删除临时项目失败: %v", result.Error)
		}
		if result.RowsAffected != 1 {
			t.Fatalf(
				"期望删除 1 个项目，实际删除 %d 个",
				result.RowsAffected,
			)
		}

		task := &model.Task{
			UserID:    userA.ID,
			ProjectID: ghostProject.ID,
			Title:     "引用不存在项目的任务",
		}

		err := CreateTask(ctx, tx, task)

		if !errors.Is(err, ErrInvalidReference) {
			t.Fatalf(
				"期望 ErrInvalidReference，实际: %v",
				err,
			)
		}
	})
}

func TestGetTaskByID(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建用户 A、B
	userA := &model.Users{
		UserName: "get_task_a_" + suffix,
		Email:    "get_task_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "get_task_b_" + suffix,
		Email:    "get_task_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	// 2. 创建三个项目
	projectA := &model.Project{
		UserID: userA.ID,
		Name:   "get_project_a_" + suffix,
	}
	projectA2 := &model.Project{
		UserID: userA.ID,
		Name:   "get_project_a2_" + suffix,
	}
	projectB := &model.Project{
		UserID: userB.ID,
		Name:   "get_project_b_" + suffix,
	}

	for _, p := range []*model.Project{
		projectA, projectA2, projectB,
	} {
		if err := CreateProject(ctx, tx, p); err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
	}

	// 3. 创建用户 A、B 的任务
	taskA := &model.Task{
		UserID:    userA.ID,
		ProjectID: projectA.ID,
		Title:     "实现 JWT",
	}
	taskB := &model.Task{
		UserID:    userB.ID,
		ProjectID: projectB.ID,
		Title:     "实现 HNSW",
	}

	for _, task := range []*model.Task{taskA, taskB} {
		if err := CreateTask(ctx, tx, task); err != nil {
			t.Fatalf("创建 Task 失败: %v", err)
		}
	}

	// 4. 正常查询
	t.Run("正常查询", func(t *testing.T) {
		got, err := GetTaskByID(
			ctx, tx, userA.ID, projectA.ID, taskA.ID,
		)

		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}
		if got == nil {
			t.Fatal("查询结果不应为 nil")
		}
		if got.ID != taskA.ID {
			t.Fatalf("Task ID 错误")
		}
		if got.UserID != userA.ID {
			t.Fatalf("Task 用户归属错误")
		}
		if got.ProjectID != projectA.ID {
			t.Fatalf("Task 项目归属错误")
		}
		if got.Title != taskA.Title {
			t.Fatalf("Task 标题错误")
		}
	})

	// 5. 跨用户查询
	t.Run("跨用户查询", func(t *testing.T) {
		got, err := GetTaskByID(
			ctx, tx, userA.ID, projectB.ID, taskB.ID,
		)

		if !errors.Is(err, ErrNotFound) {
			t.Fatalf(
				"期望 ErrNotFound，实际: %v", err,
			)
		}
		if got != nil {
			t.Fatal("跨用户查询不应返回 Task")
		}
	})

	// 6. 错误的 ProjectID
	t.Run("项目归属不匹配", func(t *testing.T) {
		got, err := GetTaskByID(
			ctx, tx, userA.ID, projectA2.ID, taskA.ID,
		)

		if !errors.Is(err, ErrNotFound) {
			t.Fatalf(
				"期望 ErrNotFound，实际: %v", err,
			)
		}
		if got != nil {
			t.Fatal("项目归属不匹配不应返回 Task")
		}
	})

	// 7. 不存在的 Task
	t.Run("任务不存在", func(t *testing.T) {
		ghostTask := &model.Task{
			UserID:    userA.ID,
			ProjectID: projectA.ID,
			Title:     "临时任务",
		}

		if err := CreateTask(ctx, tx, ghostTask); err != nil {
			t.Fatalf("创建临时 Task 失败: %v", err)
		}

		// 删除后再查询，保证该 ID 不存在
		result := tx.WithContext(ctx).
			Delete(&model.Task{}, ghostTask.ID)

		if result.Error != nil {
			t.Fatalf("删除临时 Task 失败: %v", result.Error)
		}
		if result.RowsAffected != 1 {
			t.Fatalf(
				"期望删除 1 条，实际 %d 条",
				result.RowsAffected,
			)
		}

		got, err := GetTaskByID(
			ctx, tx,
			userA.ID, projectA.ID, ghostTask.ID,
		)

		if !errors.Is(err, ErrNotFound) {
			t.Fatalf(
				"期望 ErrNotFound，实际: %v", err,
			)
		}
		if got != nil {
			t.Fatal("不存在的 Task 不应返回对象")
		}
	})
}

func TestListTasks(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建两个测试用户
	userA := &model.Users{
		UserName: "list_task_a_" + suffix,
		Email:    "list_task_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "list_task_b_" + suffix,
		Email:    "list_task_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	// 2. 创建项目
	// A 拥有三个项目，B 拥有一个项目
	projectA := &model.Project{
		UserID: userA.ID,
		Name:   "task_list_a_" + suffix,
	}
	projectA2 := &model.Project{
		UserID: userA.ID,
		Name:   "task_list_a2_" + suffix,
	}
	emptyProject := &model.Project{
		UserID: userA.ID,
		Name:   "task_list_empty_" + suffix,
	}
	projectB := &model.Project{
		UserID: userB.ID,
		Name:   "task_list_b_" + suffix,
	}

	for _, p := range []*model.Project{
		projectA, projectA2, emptyProject, projectB,
	} {
		if err := CreateProject(ctx, tx, p); err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
	}

	// 3. 创建任务
	// 故意让部分任务具有相同创建时间
	base := time.Date(
		2026, 10, 20, 10, 0, 0, 0, time.UTC,
	)

	task1 := &model.Task{
		UserID:    userA.ID,
		ProjectID: projectA.ID,
		Title:     "task1",
		CreatedAt: base,
	}

	task2 := &model.Task{
		UserID:    userA.ID,
		ProjectID: projectA.ID,
		Title:     "task2",
		CreatedAt: base.Add(time.Minute),
	}

	task3 := &model.Task{
		UserID:    userA.ID,
		ProjectID: projectA.ID,
		Title:     "task3",
		CreatedAt: base.Add(time.Minute),
	}

	task4 := &model.Task{
		UserID:    userA.ID,
		ProjectID: projectA.ID,
		Title:     "task4",
		CreatedAt: base.Add(-time.Minute),
	}

	// A 的另一个项目
	taskA2 := &model.Task{
		UserID:    userA.ID,
		ProjectID: projectA2.ID,
		Title:     "task_other_project",
	}

	// B 的项目
	taskB := &model.Task{
		UserID:    userB.ID,
		ProjectID: projectB.ID,
		Title:     "task_user_b",
	}

	for _, task := range []*model.Task{
		task1, task2, task3, task4, taskA2, taskB,
	} {
		if err := CreateTask(ctx, tx, task); err != nil {
			t.Fatalf("创建 Task 失败: %v", err)
		}
	}

	// 4. 验证稳定排序和分页
	// 正确顺序：task3, task2, task1, task4
	cases := []struct {
		name    string
		page    int
		size    int
		wantIDs []uint64
	}{
		{
			name:    "第一页",
			page:    1,
			size:    2,
			wantIDs: []uint64{task3.ID, task2.ID},
		},
		{
			name:    "第二页",
			page:    2,
			size:    2,
			wantIDs: []uint64{task1.ID, task4.ID},
		},
		{
			name:    "超出最后一页",
			page:    3,
			size:    2,
			wantIDs: []uint64{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListTasks(
				ctx, tx,
				userA.ID, projectA.ID,
				tc.page, tc.size,
			)

			if err != nil {
				t.Fatalf("查询失败: %v", err)
			}

			if got == nil {
				t.Fatal("合法查询不应返回 nil 切片")
			}

			if len(got) != len(tc.wantIDs) {
				t.Fatalf(
					"期望 %d 条，实际 %d 条",
					len(tc.wantIDs), len(got),
				)
			}

			for i, task := range got {
				if task.ID != tc.wantIDs[i] {
					t.Fatalf(
						"排序错误: index=%d, want=%d, got=%d",
						i, tc.wantIDs[i], task.ID,
					)
				}

				if task.UserID != userA.ID {
					t.Fatal("查询到了其他用户的 Task")
				}

				if task.ProjectID != projectA.ID {
					t.Fatal("查询到了其他项目的 Task")
				}
			}
		})
	}

	// 5. 同一用户不同项目隔离
	t.Run("同用户不同项目隔离", func(t *testing.T) {
		got, err := ListTasks(
			ctx, tx,
			userA.ID, projectA2.ID, 1, 10,
		)

		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}

		if len(got) != 1 || got[0].ID != taskA2.ID {
			t.Fatalf("项目隔离失败: %+v", got)
		}
	})

	// 6. 跨用户查询
	t.Run("跨用户查询", func(t *testing.T) {
		got, err := ListTasks(
			ctx, tx,
			userA.ID, projectB.ID, 1, 10,
		)

		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}

		if got == nil || len(got) != 0 {
			t.Fatalf("跨用户查询应返回空切片: %+v", got)
		}
	})

	// 7. B 查询自己的项目
	t.Run("用户B正常查询", func(t *testing.T) {
		got, err := ListTasks(
			ctx, tx,
			userB.ID, projectB.ID, 1, 10,
		)

		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}

		if len(got) != 1 || got[0].ID != taskB.ID {
			t.Fatalf("用户 B 查询结果错误: %+v", got)
		}
	})

	// 8. 空项目查询
	t.Run("空项目", func(t *testing.T) {
		got, err := ListTasks(
			ctx, tx,
			userA.ID, emptyProject.ID, 1, 10,
		)

		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}

		if got == nil {
			t.Fatal("空项目应该返回非 nil 空切片")
		}

		if len(got) != 0 {
			t.Fatalf("空项目应该返回 0 条: %+v", got)
		}
	})

	// 9. 非法分页参数
	maxInt := int(^uint(0) >> 1)

	invalidCases := []struct {
		name string
		page int
		size int
	}{
		{"page为0", 0, 10},
		{"page为负数", -1, 10},
		{"pageSize为0", 1, 0},
		{"pageSize为负数", 1, -1},
		{"pageSize超过100", 1, 101},
		{"OFFSET整数溢出", maxInt, 100},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListTasks(
				ctx, tx,
				userA.ID, projectA.ID,
				tc.page, tc.size,
			)

			if !errors.Is(err, ErrInvalidPagination) {
				t.Fatalf(
					"期望 ErrInvalidPagination，实际: %v",
					err,
				)
			}

			if got != nil {
				t.Fatal("非法分页不应该返回 Task")
			}
		})
	}
}
