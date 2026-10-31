package service

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const testCommitSHA = "0123456789abcdef0123456789abcdef01234567"

// Fake GitHub Client：不发送真实 HTTP 请求。
type fakeBindingGitHubClient struct {
	info  *githubclient.RepositoryInfo
	err   error
	calls int
}

func (f *fakeBindingGitHubClient) GetPublicRepository(
	ctx context.Context,
	owner, repo string,
) (*githubclient.RepositoryInfo, error) {
	f.calls++

	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func newBindingFake() *fakeBindingGitHubClient {
	return &fakeBindingGitHubClient{
		info: &githubclient.RepositoryInfo{
			Owner:         "golang",
			Repo:          "go",
			DefaultBranch: "master",
			LastSHA:       testCommitSHA,
		},
	}
}

// 独立测试事务，结束后回滚。
// 强制使用 go_server_test，避免误操作开发库。
func newGitHubBindingTestTx(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN 环境变量未设置")
	}

	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("解析测试 DSN 失败: %v", err)
	}
	if cfg.DBName != "go_server_test" {
		t.Fatalf("禁止使用非测试数据库: %s", cfg.DBName)
	}

	db, err := gorm.Open(
		gormmysql.Open(dsn),
		&gorm.Config{TranslateError: true},
	)
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}

	tx := db.Begin()
	if tx.Error != nil {
		_ = sqlDB.Close()
		t.Fatalf("开启测试事务失败: %v", tx.Error)
	}

	t.Cleanup(func() {
		_ = tx.Rollback().Error
		_ = sqlDB.Close()
	})

	return tx
}

// 准备测试用户及项目。
func createBindingTestProject(
	t *testing.T,
	tx *gorm.DB,
) (*model.Users, *model.Project) {
	t.Helper()

	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	user := &model.Users{
		UserName: "ghb_" + suffix,
		Email:    "ghb_" + suffix + "@example.com",
		Password: "test-hash",
	}
	if err := repository.CreateUser(ctx, tx, user); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	project := &model.Project{
		UserID: user.ID,
		Name:   "ghb_project_" + suffix,
	}
	if err := repository.CreateProject(ctx, tx, project); err != nil {
		t.Fatalf("创建测试项目失败: %v", err)
	}

	return user, project
}

func assertBindingCount(
	t *testing.T,
	tx *gorm.DB,
	projectID uint64,
	want int64,
) {
	t.Helper()

	var count int64
	err := tx.Model(&model.Repository{}).
		Where("project_id = ?", projectID).
		Count(&count).Error

	if err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("绑定数量: want=%d got=%d", want, count)
	}
}

// 1. 正常绑定：GitHub 成功后写入 MySQL。
func TestGitHubBinding_Success(t *testing.T) {
	tx := newGitHubBindingTestTx(t)
	user, project := createBindingTestProject(t, tx)

	fake := newBindingFake()
	svc := NewGitHubBindingService(tx, fake)

	got, err := svc.BindRepository(
		context.Background(),
		user.ID, project.ID,
		"golang", "go",
	)
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	if got == nil || got.ID == 0 {
		t.Fatal("绑定成功后 ID 未回填")
	}

	if got.ProjectID != project.ID ||
		got.DefaultBranch != "master" ||
		got.LastSHA != testCommitSHA {
		t.Fatalf("仓库信息错误: %+v", got)
	}
	if got.Owner != "golang" || got.Repo != "go" {
		t.Fatalf(
			"仓库标识错误: owner=%s repo=%s",
			got.Owner,
			got.Repo,
		)
	}

	if fake.calls != 1 {
		t.Fatalf("GitHub 调用次数应为 1，实际 %d", fake.calls)
	}

	assertBindingCount(t, tx, project.ID, 1)

	t.Log("PASS: GitHub 查询并持久化成功")
}

// 2. 项目归属错误：绝对不能调用 GitHub。
func TestGitHubBinding_ForeignProject(t *testing.T) {
	tx := newGitHubBindingTestTx(t)

	_, projectA := createBindingTestProject(t, tx)
	userB, _ := createBindingTestProject(t, tx)

	fake := newBindingFake()
	svc := NewGitHubBindingService(tx, fake)

	got, err := svc.BindRepository(
		context.Background(),
		userB.ID, projectA.ID,
		"golang", "go",
	)

	if !errors.Is(err, ErrGitHubBindingProjectNotFound) {
		t.Fatalf("期望项目不存在错误，实际: %v", err)
	}
	if got != nil {
		t.Fatal("未授权绑定不应返回数据")
	}
	if fake.calls != 0 {
		t.Fatal("未授权请求不应调用 GitHub")
	}

	assertBindingCount(t, tx, projectA.ID, 0)

	t.Log("PASS: 项目归属校验先于外部调用")
}

// 3. GitHub 失败时，不能写入数据库。
func TestGitHubBinding_GitHubErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"not_found", githubclient.ErrNotFound},
		{"rate_limited", githubclient.ErrRateLimited},
		{"timeout", githubclient.ErrTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := newGitHubBindingTestTx(t)
			user, project := createBindingTestProject(t, tx)

			fake := newBindingFake()
			fake.err = tc.err

			svc := NewGitHubBindingService(tx, fake)

			got, err := svc.BindRepository(
				context.Background(),
				user.ID, project.ID,
				"golang", "go",
			)

			if !errors.Is(err, tc.err) {
				t.Fatalf("want=%v got=%v", tc.err, err)
			}
			if got != nil {
				t.Fatal("GitHub 失败后不应返回绑定记录")
			}
			if fake.calls != 1 {
				t.Fatalf("GitHub 调用次数错误: %d", fake.calls)
			}

			assertBindingCount(t, tx, project.ID, 0)
		})
	}
}

// 4. 重复绑定转换为明确的领域错误。
func TestGitHubBinding_Duplicate(t *testing.T) {
	tx := newGitHubBindingTestTx(t)
	user, project := createBindingTestProject(t, tx)

	fake := newBindingFake()
	svc := NewGitHubBindingService(tx, fake)

	ctx := context.Background()

	first, err := svc.BindRepository(
		ctx, user.ID, project.ID, "golang", "go",
	)
	if err != nil {
		t.Fatalf("首次绑定失败: %v", err)
	}
	if first == nil {
		t.Fatal("首次绑定结果不能为空")
	}

	second, err := svc.BindRepository(
		ctx, user.ID, project.ID, "golang", "go",
	)

	if !errors.Is(err, ErrGitHubRepositoryAlreadyBound) {
		t.Fatalf("期望重复绑定领域错误，实际: %v", err)
	}
	if second != nil {
		t.Fatal("重复绑定不应返回新记录")
	}

	assertBindingCount(t, tx, project.ID, 1)

	t.Log("PASS: 唯一约束冲突转换为 Service 领域错误")
}

// 5. 无效 ID：直接拒绝。
func TestGitHubBinding_InvalidIDs(t *testing.T) {
	tx := newGitHubBindingTestTx(t)
	fake := newBindingFake()
	svc := NewGitHubBindingService(tx, fake)

	_, err := svc.BindRepository(
		context.Background(),
		0, 0, "golang", "go",
	)

	if !errors.Is(err, ErrGitHubBindingInvalidInput) {
		t.Fatalf("期望非法参数错误，实际: %v", err)
	}
	if fake.calls != 0 {
		t.Fatal("非法参数不应触发 GitHub 请求")
	}
}
