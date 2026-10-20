package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"GoServer/internal/model"
)

func TestCreateProject(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 创建两个测试用户。
	userA := &model.Users{
		UserName: "project_a_" + suffix,
		Email:    "project_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "project_b_" + suffix,
		Email:    "project_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
	}

	name := "project_" + suffix

	// 1. 正常创建。
	projectA := &model.Project{
		UserID: userA.ID,
		Name:   name,
	}

	if err := CreateProject(ctx, tx, projectA); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}

	if projectA.ID == 0 {
		t.Fatal("创建成功后项目 ID 未回填")
	}

	// 2. 同一用户创建同名项目应失败。
	duplicate := &model.Project{
		UserID: userA.ID,
		Name:   name,
	}

	err := CreateProject(ctx, tx, duplicate)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf(
			"期望 ErrAlreadyExists，实际: %v",
			err,
		)
	}

	// 3. 不同用户可以创建同名项目。
	projectB := &model.Project{
		UserID: userB.ID,
		Name:   name,
	}

	if err := CreateProject(ctx, tx, projectB); err != nil {
		t.Fatalf(
			"不同用户创建同名项目不应失败: %v",
			err,
		)
	}

	if projectB.ID == 0 {
		t.Fatal("用户 B 的项目 ID 未回填")
	}

	if projectA.ID == projectB.ID {
		t.Fatal("两个不同项目不应该有相同 ID")
	}
}

func TestGetProjectByID(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建用户 A 和用户 B
	userA := &model.Users{
		UserName: "project_get_a_" + suffix,
		Email:    "project_get_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "project_get_b_" + suffix,
		Email:    "project_get_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
	}

	projectA := &model.Project{
		UserID: userA.ID,
		Name:   "project_get_a_" + suffix,
	}
	projectB := &model.Project{
		UserID: userB.ID,
		Name:   "project_get_b_" + suffix,
	}

	if err := CreateProject(ctx, tx, projectA); err != nil {
		t.Fatalf("创建用户 A 的项目失败: %v", err)
	}
	if err := CreateProject(ctx, tx, projectB); err != nil {
		t.Fatalf("创建用户 B 的项目失败: %v", err)
	}

	// 2. A 查询自己的项目，应成功
	got, err := GetProjectByID(ctx, tx, userA.ID, projectA.ID)
	if err != nil {
		t.Fatalf("A 查询自己的项目失败: %v", err)
	}
	if got == nil {
		t.Fatal("A 查询自己的项目返回 nil")
	}
	if got.ID != projectA.ID {
		t.Fatalf("查询到的项目 ID 不正确，期望 %d，实际 %d", projectA.ID, got.ID)
	}
	if got.UserID != userA.ID {
		t.Fatalf(
			"项目归属错误: expected=%d, got=%d",
			userA.ID,
			got.UserID,
		)
	}

	// 3. A 查询 B 的项目，应返回 ErrNotFound
	got, err = GetProjectByID(ctx, tx, userA.ID, projectB.ID)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	if got != nil {
		t.Fatal("跨用户查询不应该返回项目")
	}
}

func TestListProjects(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()
	suffix := testSuffix()

	// 1. 创建两个测试用户
	userA := &model.Users{
		UserName: "list_project_a_" + suffix,
		Email:    "list_project_a_" + suffix + "@example.com",
		Password: "test-hash",
	}
	userB := &model.Users{
		UserName: "list_project_b_" + suffix,
		Email:    "list_project_b_" + suffix + "@example.com",
		Password: "test-hash",
	}

	for _, user := range []*model.Users{userA, userB} {
		if err := CreateUser(ctx, tx, user); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	// 2. 创建项目，故意设置相同和不同的时间
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

	p1 := &model.Project{
		UserID:    userA.ID,
		Name:      "p1_" + suffix,
		CreatedAt: base,
	}
	p2 := &model.Project{
		UserID:    userA.ID,
		Name:      "p2_" + suffix,
		CreatedAt: base.Add(time.Minute),
	}
	p3 := &model.Project{
		UserID:    userA.ID,
		Name:      "p3_" + suffix,
		CreatedAt: base.Add(time.Minute),
	}
	p4 := &model.Project{
		UserID:    userA.ID,
		Name:      "p4_" + suffix,
		CreatedAt: base.Add(-time.Minute),
	}
	pB := &model.Project{
		UserID: userB.ID,
		Name:   "other_" + suffix,
	}

	for _, p := range []*model.Project{p1, p2, p3, p4, pB} {
		if err := CreateProject(ctx, tx, p); err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
	}

	// 3. 验证第一页和第二页的稳定排序
	// 正确顺序：p3, p2, p1, p4
	cases := []struct {
		name    string
		page    int
		size    int
		wantIDs []uint64
	}{
		{"第一页", 1, 2, []uint64{p3.ID, p2.ID}},
		{"第二页", 2, 2, []uint64{p1.ID, p4.ID}},
		{"超出最后一页", 3, 2, []uint64{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListProjects(
				ctx, tx, userA.ID, tc.page, tc.size,
			)
			if err != nil {
				t.Fatalf("查询失败: %v", err)
			}

			if len(got) != len(tc.wantIDs) {
				t.Fatalf(
					"期望 %d 条，实际 %d 条",
					len(tc.wantIDs), len(got),
				)
			}

			for i, p := range got {
				if p.ID != tc.wantIDs[i] {
					t.Fatalf(
						"第 %d 条排序错误: want=%d, got=%d",
						i, tc.wantIDs[i], p.ID,
					)
				}
				if p.UserID != userA.ID {
					t.Fatalf("查询到了其他用户的项目")
				}
			}
		})
	}

	// 4. 验证用户 B 只能查询自己的项目
	gotB, err := ListProjects(ctx, tx, userB.ID, 1, 10)
	if err != nil {
		t.Fatalf("用户 B 查询失败: %v", err)
	}
	if len(gotB) != 1 || gotB[0].ID != pB.ID {
		t.Fatalf("用户 B 的项目隔离失败: %+v", gotB)
	}

	// 5. 验证非法分页参数
	maxInt := int(^uint(0) >> 1)

	invalidCases := []struct {
		name string
		page int
		size int
	}{
		{"page为0", 0, 10},
		{"page为负数", -1, 10},
		{"pageSize为0", 1, 0},
		{"pageSize超过100", 1, 101},
		{"OFFSET整数溢出", maxInt, 100},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListProjects(
				ctx, tx, userA.ID, tc.page, tc.size,
			)

			if !errors.Is(err, ErrInvalidPagination) {
				t.Fatalf(
					"期望 ErrInvalidPagination，实际: %v",
					err,
				)
			}
			if got != nil {
				t.Fatalf("非法分页不应返回项目")
			}
		})
	}
}
