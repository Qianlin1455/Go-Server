package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"GoServer/internal/midware"
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestSourceHandler(t *testing.T) {
	dsn, err := mysql.ParseDSN(os.Getenv("TEST_MYSQL_DSN"))
	if err != nil || dsn.DBName != "go_server_test" {
		t.Fatal("TEST_MYSQL_DSN must point to go_server_test")
	}
	db := setupMessageAPITestDB(t)
	gin.SetMode(gin.TestMode)
	account, other := createSourceTestUser(t, db, "owner"), createSourceTestUser(t, db, "other")
	project, anotherProject := createSourceTestProject(t, db, account.ID, "main"), createSourceTestProject(t, db, account.ID, "other")
	revision := strings.Repeat("a", 40)
	binding := model.Repository{ProjectID: project.ID, Owner: "org", Repo: "repo", DefaultBranch: "main", LastSHA: revision}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	file := func(path, content string) model.SourceFile {
		return model.SourceFile{Path: path, Content: content, Language: "go", BlobSHA: strings.Repeat("b", 40)}
	}
	older := publishSourceTestFiles(t, db, project, binding.ID, revision, "old", file("old.go", "needle old"))
	multiLine := strings.Repeat("needle first\r\n", 256) + "needle 100%_!last"
	latest := publishSourceTestFiles(t, db, project, binding.ID, revision, "new",
		file("src/zzz.go", "needle end"), file("src/x/main.go", "needle 100Xy"),
		file("src/a/main.go", multiLine), file("literal%_!/main.go", "NEEDLE"))
	differentRevision := strings.Repeat("c", 40)
	publishSourceTestFiles(t, db, project, binding.ID, differentRevision, "other-revision", file("foreign.go", "needle"))
	oversizeJob := publishSourceTestFiles(t, db, project, binding.ID, strings.Repeat("d", 40), "oversize",
		file("oversize.go", strings.Repeat("a", 512<<10+1)))
	var target, oversized model.SourceFile
	if err := db.Where("import_job_id = ? AND path = ?", latest.ID, "src/a/main.go").Take(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("import_job_id = ?", oversizeJob.ID).Take(&oversized).Error; err != nil {
		t.Fatal(err)
	}

	manager, err := security.NewJWTManager(strings.Repeat("a", 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ownerJWT, err := manager.Generate(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherJWT, err := manager.Generate(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	private := router.Group("/api/v1", midware.JWTAuth(manager))
	// Match existing project routes to catch wildcard registration conflicts.
	private.GET("/projects/:id", func(*gin.Context) {})
	NewSourceHandler(service.NewSourceService(db)).RegisterRoutes(private)
	base := fmt.Sprintf("/api/v1/projects/%d", project.ID)
	files, search := base+"/source-files", base+"/source-search"
	detail := fmt.Sprintf("%s/%d", files, target.ID)
	rev := "revision_sha=" + revision

	for _, tc := range []struct {
		name, path, token string
		status            int
		verify            func(t *testing.T, data gin.H)
	}{
		{"list", files + "?" + rev, ownerJWT, 200, func(t *testing.T, data gin.H) {
			items := sourceItems(t, data, 4)
			if data["import_job_id"] != float64(latest.ID) || items[0]["path"] != "literal%_!/main.go" ||
				items[1]["file_id"] != float64(target.ID) || items[1]["created_at"] == nil {
				t.Fatalf("incorrect latest snapshot or sort: %v", data)
			}
			for _, item := range items {
				if _, ok := item["content"]; ok || item["import_job_id"] != float64(latest.ID) {
					t.Fatal("list returned content or mixed snapshots")
				}
			}
		}},
		{"previous snapshot", fmt.Sprintf("%s?%s&import_job_id=%d", files, rev, older.ID), ownerJWT, 200, func(t *testing.T, data gin.H) {
			if sourceItems(t, data, 1)[0]["path"] != "old.go" {
				t.Fatal("explicit snapshot did not pin results")
			}
		}},
		{"exact path", files + "?" + rev + "&path=src/a/main.go", ownerJWT, 200, func(t *testing.T, data gin.H) {
			if sourceItems(t, data, 1)[0]["file_id"] != float64(target.ID) {
				t.Fatal("exact path did not select expected file")
			}
		}},
		{"literal prefix", files + "?" + rev + "&path_prefix=" + url.QueryEscape("literal%_!/"), ownerJWT, 200, func(t *testing.T, data gin.H) {
			if sourceItems(t, data, 1)[0]["path"] != "literal%_!/main.go" {
				t.Fatal("incorrect prefix match")
			}
		}},
		{"file page", files + "?" + rev + "&path_prefix=src/&page=2&page_size=1", ownerJWT, 200, func(t *testing.T, data gin.H) {
			if data["total"] != float64(3) || sourceItems(t, data, 1)[0]["path"] != "src/x/main.go" {
				t.Fatal("file pagination is out of order")
			}
		}},
		{"detail", detail, ownerJWT, 200, func(t *testing.T, data gin.H) {
			if data["content"] != multiLine || data["blob_sha"] != target.BlobSHA || data["revision_sha"] != revision {
				t.Fatal("file detail differs from snapshot")
			}
		}},
		{"spans", detail + "/spans?page=2&page_size=1", ownerJWT, 200, func(t *testing.T, data gin.H) {
			span := sourceItems(t, data, 1)[0]
			if data["total"] != float64(2) || span["span_index"] != float64(1) ||
				span["line_start"] != float64(257) || span["line_end"] != float64(257) || span["content"] != "needle 100%_!last" {
				t.Fatalf("wrong span or line range: %v", span)
			}
		}},
		{"literal search", search + "?" + rev + "&q=" + url.QueryEscape("100%_!"), ownerJWT, 200, func(t *testing.T, data gin.H) {
			match := sourceItems(t, data, 1)[0]
			if match["source_file_id"] != float64(target.ID) || match["source_span_id"] == nil ||
				match["revision_sha"] != revision || match["line_start"] != float64(257) {
				t.Fatalf("wrong literal match or trace fields: %v", match)
			}
		}},
		{"ordered search", search + "?" + rev + "&q=needle&path_prefix=src/&page=2&page_size=1", ownerJWT, 200, func(t *testing.T, data gin.H) {
			match := sourceItems(t, data, 1)[0]
			if data["total"] != float64(4) || match["source_file_id"] != float64(target.ID) || match["line_start"] != float64(257) {
				t.Fatalf("search pagination is out of order: %v", data)
			}
		}},
		{"empty search", search + "?" + rev + "&q=unmatched", ownerJWT, 200, func(t *testing.T, data gin.H) { sourceItems(t, data, 0) }},
		{"out of range page", files + "?" + rev + "&page=999", ownerJWT, 200, func(t *testing.T, data gin.H) { sourceItems(t, data, 0) }},
		{"missing revision", files, ownerJWT, 400, nil},
		{"bad revision", files + "?revision_sha=nope", ownerJWT, 400, nil},
		{"missing keyword", search + "?" + rev, ownerJWT, 400, nil},
		{"keyword too long", search + "?" + rev + "&q=" + strings.Repeat("a", 257), ownerJWT, 400, nil},
		{"path traversal", files + "?" + rev + "&path=src/../main.go", ownerJWT, 400, nil},
		{"invalid page", files + "?" + rev + "&page=1001", ownerJWT, 400, nil},
		{"file page too large", files + "?" + rev + "&page_size=101", ownerJWT, 400, nil},
		{"search page too large", search + "?" + rev + "&q=needle&page_size=21", ownerJWT, 400, nil},
		{"oversize detail", fmt.Sprintf("%s/%d", files, oversized.ID), ownerJWT, 413, nil},
		{"anonymous", files + "?" + rev, "", 401, nil},
		{"foreign project", files + "?" + rev + fmt.Sprintf("&user_id=%d", account.ID), otherJWT, 404, nil},
		{"foreign user detail", detail, otherJWT, 404, nil},
		{"different project detail", fmt.Sprintf("/api/v1/projects/%d/source-files/%d", anotherProject.ID, target.ID), ownerJWT, 404, nil},
		{"different project spans", fmt.Sprintf("/api/v1/projects/%d/source-files/%d/spans", anotherProject.ID, target.ID), ownerJWT, 404, nil},
		{"different revision job", fmt.Sprintf("%s?revision_sha=%s&import_job_id=%d", files, differentRevision, latest.ID), ownerJWT, 404, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("HTTP %d, want %d: %.300s", out.Code, tc.status, out.Body.String())
			}
			if tc.verify != nil {
				var response struct {
					Code string
					Data gin.H
				}
				if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || response.Code != "SUCCESS" {
					t.Fatalf("invalid success response: %v, %q", err, response.Code)
				}
				tc.verify(t, response.Data)
			}
		})
	}
}

func sourceItems(t *testing.T, data gin.H, count int) []gin.H {
	t.Helper()
	values, ok := data["items"].([]any)
	if !ok || len(values) != count {
		t.Fatalf("want %d items, got %v", count, data["items"])
	}
	items := make([]gin.H, 0, len(values))
	for _, value := range values {
		items = append(items, value.(map[string]any))
	}
	return items
}

func createSourceTestUser(t *testing.T, db *gorm.DB, name string) model.Users {
	t.Helper()
	name = fmt.Sprintf("source_%s_%d", name, time.Now().UnixNano())
	user := model.Users{UserName: name, Email: name + "@example.test", Password: "test"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func createSourceTestProject(t *testing.T, db *gorm.DB, userID uint64, name string) model.Project {
	t.Helper()
	project := model.Project{UserID: userID, Name: name, Status: model.ProjectStatusActive}
	if err := db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	return project
}

func publishSourceTestFiles(t *testing.T, db *gorm.DB, project model.Project, repositoryID uint64, revision, key string, files ...model.SourceFile) model.ImportJob {
	t.Helper()
	ctx := t.Context()
	store := repository.NewImportJobStore(db)
	job := model.ImportJob{
		UserID: project.UserID, ProjectID: project.ID, RepositoryID: repositoryID,
		RevisionSHA: revision, Endpoint: service.ImportCreateEndpoint, IdempotencyKey: key,
		RequestFingerprint: strings.Repeat("a", 64), Status: model.ImportJobQueued,
	}
	if err := store.Create(ctx, &job); err != nil {
		t.Fatal(err)
	}
	token, claimed, err := store.ClaimQueuedJob(ctx, job.ID, "source-handler-test", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %t, %v", claimed, err)
	}
	result := model.ImportJobResult{
		TreeSHA: revision, FileCount: uint32(len(files)),
		Source: &model.SourceSnapshot{RevisionSHA: revision, Files: files},
	}
	for _, file := range files {
		result.TotalBytes += uint64(len(file.Content))
	}
	if ok, err := store.CompleteJobWithResult(ctx, job.ID, token, result); !ok || err != nil {
		t.Fatalf("publish snapshot: %t, %v", ok, err)
	}
	return job
}
