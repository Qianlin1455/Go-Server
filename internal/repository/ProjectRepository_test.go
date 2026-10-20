package repository

import (
	"context"
	"errors"
	"testing"

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
