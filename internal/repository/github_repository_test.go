package repository

import (
	"context"
	"errors"
	"testing"

	"GoServer/internal/model"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// 创建测试用户及所属项目。
// 使用现有的 setupTestTx，避免污染正式数据库。
func setupGitHubTestProject(
	t *testing.T,
	ctx context.Context,
	tx *gorm.DB,
	prefix string,
) (*model.Users, *model.Project) {
	t.Helper()

	suffix := testSuffix()

	user := &model.Users{
		UserName: prefix + "_user_" + suffix,
		Email:    prefix + "_user_" + suffix + "@example.com",
		Password: "test-hash",
	}

	if err := CreateUser(ctx, tx, user); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	project := &model.Project{
		UserID: user.ID,
		Name:   prefix + "_project_" + suffix,
	}

	if err := CreateProject(ctx, tx, project); err != nil {
		t.Fatalf("创建测试项目失败: %v", err)
	}

	return user, project
}

// 测试 Model 对应的真实表名。
func TestGitHubRepository_ModelTableName(t *testing.T) {
	repo := model.Repository{}

	if got := repo.TableName(); got != "repositories" {
		t.Fatalf(
			"expected repositories, got %s",
			got,
		)
	}
}

// 测试 1：创建仓库绑定并查询。
func TestGitHubRepository_Create(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	user, project := setupGitHubTestProject(
		t, ctx, tx, "gh_create",
	)

	store := NewGitHubRepositoryStore(tx)

	repo := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	// 1. 创建仓库
	if err := store.Create(ctx, repo); err != nil {
		t.Fatalf("创建仓库绑定失败: %v", err)
	}

	if repo.ID == 0 {
		t.Fatal("创建成功后仓库 ID 未回填")
	}

	// 2. 根据用户、项目、仓库 ID 查询
	got, err := store.GetByProject(
		ctx, user.ID, project.ID, repo.ID,
	)
	if err != nil {
		t.Fatalf("查询仓库绑定失败: %v", err)
	}

	if got == nil {
		t.Fatal("查询结果不能为 nil")
	}

	// 3. 验证读取的数据与写入一致
	if got.ID != repo.ID ||
		got.ProjectID != project.ID ||
		got.Owner != repo.Owner ||
		got.Repo != repo.Repo ||
		got.DefaultBranch != repo.DefaultBranch ||
		got.LastSHA != repo.LastSHA {
		t.Fatalf("仓库数据不一致: got=%+v", got)
	}

	t.Log("PASS: 仓库绑定创建和查询成功")
}

// 测试 2：同一个项目不能重复绑定同一个仓库。
func TestGitHubRepository_Duplicate(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	_, project := setupGitHubTestProject(
		t, ctx, tx, "gh_dup",
	)

	store := NewGitHubRepositoryStore(tx)

	first := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("第一次绑定失败: %v", err)
	}

	// 1. 相同项目重复绑定相同仓库
	duplicate := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	err := store.Create(ctx, duplicate)

	if err == nil {
		t.Fatal("重复绑定应该被唯一约束拒绝")
	}

	// 兼容 GORM TranslateError 和原始 MySQL 1062 错误。
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		var mysqlErr *mysql.MySQLError

		if !errors.As(err, &mysqlErr) ||
			mysqlErr.Number != 1062 {
			t.Fatalf("期望唯一约束冲突，实际: %v", err)
		}
	}

	// 2. 大小写不同仍应被判定为重复
	caseDuplicate := &model.Repository{
		ProjectID:     project.ID,
		Owner:         "GoLang",
		Repo:          "GO",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	if err := store.Create(ctx, caseDuplicate); err == nil {
		t.Fatal("仓库名称大小写变化不应绕过唯一约束")
	}

	t.Log("PASS: 重复仓库绑定被数据库拒绝")
}

// 测试 3：跨用户、跨项目访问隔离。
func TestGitHubRepository_Isolation(t *testing.T) {
	tx := setupTestTx(t)
	ctx := context.Background()

	userA, projectA := setupGitHubTestProject(
		t, ctx, tx, "gh_iso_a",
	)
	userB, projectB := setupGitHubTestProject(
		t, ctx, tx, "gh_iso_b",
	)

	store := NewGitHubRepositoryStore(tx)

	repoA := &model.Repository{
		ProjectID:     projectA.ID,
		Owner:         "golang",
		Repo:          "go",
		DefaultBranch: "master",
		LastSHA:       "0123456789abcdef0123456789abcdef01234567",
	}

	if err := store.Create(ctx, repoA); err != nil {
		t.Fatalf("创建用户 A 仓库失败: %v", err)
	}

	// 1. 用户 A 读取自己的仓库，应成功
	got, err := store.GetByProject(
		ctx, userA.ID, projectA.ID, repoA.ID,
	)
	if err != nil || got == nil {
		t.Fatalf("A 查询自己的仓库失败: %v", err)
	}

	// 2. 用户 B 读取用户 A 的仓库，应失败
	got, err = store.GetByProject(
		ctx, userB.ID, projectA.ID, repoA.ID,
	)

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("跨用户访问应返回 NotFound，实际: %v", err)
	}
	if got != nil {
		t.Fatal("跨用户查询不应该返回仓库数据")
	}

	// 3. 用户 A 使用错误的 projectID 查询自己的仓库
	got, err = store.GetByProject(
		ctx, userA.ID, projectB.ID, repoA.ID,
	)

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("跨项目访问应返回 NotFound，实际: %v", err)
	}
	if got != nil {
		t.Fatal("跨项目查询不应该返回仓库数据")
	}

	t.Log("PASS: 跨用户和跨项目访问均被拒绝")
}
