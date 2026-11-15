package githubclient

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testBlobSHA(content string) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))))
}

func TestDownloadRevision(t *testing.T) {
	const base = "/repos/org/repo"
	const content = "package example\r\n"
	rev, tree, blob := strings.Repeat("a", 40), strings.Repeat("b", 40), testBlobSHA(content)
	binary := testBlobSHA("x\x00")
	invalidUTF8 := testBlobSHA("\xff")
	var branch, downloads atomic.Int32
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base:
			fmt.Fprintf(w, `{"visibility":"public","default_branch":"branch-%d"}`, branch.Add(1))
		case base + "/git/commits/" + rev:
			fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, rev, tree)
		case base + "/git/trees/" + tree:
			if r.URL.Query().Get("recursive") != "1" {
				t.Error("missing recursive tree request")
			}
			fmt.Fprintf(w, `{"sha":%q,"tree":[
				{"path":"src","type":"tree","mode":"040000"},
				{"path":"src/main.go","sha":%q,"size":%d,"type":"blob","mode":"100755"},
				{"path":"binary.go","sha":%q,"size":2,"type":"blob","mode":"100644"},
				{"path":"broken.txt","sha":%q,"size":1,"type":"blob","mode":"100644"},
				{"path":"link.go","size":5,"type":"blob","mode":"120000"},
				{"path":"vendor","type":"commit","mode":"160000"},
				{"path":"image.png","size":3,"type":"blob","mode":"100644"}]}`,
				tree, blob, len(content), binary, invalidUTF8)
		case base + "/git/blobs/" + blob:
			downloads.Add(1)
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			writeTestBlob(w, blob, len(content), encoded[:4]+"\n"+encoded[4:])
		case base + "/git/blobs/" + binary:
			downloads.Add(1)
			writeTestBlob(w, binary, 2, "eAA=")
		case base + "/git/blobs/" + invalidUTF8:
			downloads.Add(1)
			writeTestBlob(w, invalidUTF8, 1, "/w==")
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	for range 2 {
		got, err := c.DownloadRevision(t.Context(), "org", "repo", rev)
		if err != nil {
			t.Fatal(err)
		}
		if got.CommitSHA != rev || got.TreeSHA != tree || len(got.Files) != 1 {
			t.Fatalf("unexpected snapshot: %+v", got)
		}
		file := got.Files[0]
		if file.Path != "src/main.go" || file.BlobSHA != blob || file.Language != "go" || string(file.Content) != content {
			t.Fatalf("unexpected file: %+v", file)
		}
	}
	if branch.Load() != 2 || downloads.Load() != 6 {
		t.Fatalf("branch reads=%d downloads=%d", branch.Load(), downloads.Load())
	}
}

func TestDownloadRevisionFailures(t *testing.T) {
	const base = "/repos/org/repo"
	rev, tree, blob := strings.Repeat("a", 40), strings.Repeat("b", 40), testBlobSHA("a")
	for _, tc := range []struct {
		name string
		want error
	}{
		{"wrong commit SHA", ErrBadResponse}, {"wrong tree SHA", ErrBadResponse},
		{"wrong blob SHA", ErrBadResponse}, {"wrong content hash", ErrBadResponse},
		{"wrong decoded size", ErrBadResponse}, {"bad base64", ErrBadResponse},
		{"oversize file", ErrTreeBudgetExceeded}, {"aggregate budget", ErrTreeBudgetExceeded},
		{"oversize response", ErrTooLarge}, {"truncated tree", ErrTreeBudgetExceeded},
		{"404", ErrNotFound}, {"429", ErrRateLimited}, {"timeout", ErrTimeout},
		{"traversal", ErrBadResponse}, {"absolute path", ErrBadResponse}, {"windows path", ErrBadResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case base:
					fmt.Fprint(w, `{"visibility":"public","default_branch":"main"}`)
				case base + "/git/commits/" + rev:
					commit := rev
					if tc.name == "wrong commit SHA" {
						commit = tree
					}
					fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, commit, tree)
				case base + "/git/trees/" + tree:
					size, name, returned := 1, "main.go", tree
					switch tc.name {
					case "wrong tree SHA":
						returned = rev
					case "oversize file":
						size = maxSourceFileBytes + 1
					case "aggregate budget":
						size = int(maxRevisionTotalBytes) + 1
					case "traversal":
						name = "../main.go"
					case "absolute path":
						name = "/main.go"
					case "windows path":
						name = `C:\main.go`
					}
					fmt.Fprintf(w, `{"sha":%q,"truncated":%t,"tree":[{"path":%s,"type":"blob","mode":"100644","sha":%q,"size":%d}]}`,
						returned, tc.name == "truncated tree", mustJSONPath(t, name), blob, size)
				case base + "/git/blobs/" + blob:
					switch tc.name {
					case "wrong blob SHA":
						writeTestBlob(w, rev, 1, "YQ==")
					case "wrong content hash":
						writeTestBlob(w, blob, 1, "Yg==")
					case "wrong decoded size":
						writeTestBlob(w, blob, 1, "YWI=")
					case "bad base64":
						writeTestBlob(w, blob, 1, "???")
					case "oversize response":
						fmt.Fprint(w, strings.Repeat(" ", 2*maxSourceFileBytes+1))
					case "404":
						w.WriteHeader(http.StatusNotFound)
					case "429":
						w.Header().Set("Retry-After", "30")
						w.WriteHeader(http.StatusTooManyRequests)
					case "timeout":
						<-r.Context().Done()
					default:
						t.Error("blob requested after invalid tree")
					}
				default:
					http.NotFound(w, r)
				}
			})
			if tc.name == "timeout" {
				c.httpClient.Timeout = 200 * time.Millisecond
			}
			got, err := c.DownloadRevision(t.Context(), "org", "repo", rev)
			if !errors.Is(err, tc.want) || len(got.Files) != 0 {
				t.Fatalf("files=%d, err=%v, want %v", len(got.Files), err, tc.want)
			}
			if tc.name == "429" {
				var limit *RateLimitError
				if !errors.As(err, &limit) || !limit.RetryAt.After(time.Now()) {
					t.Fatalf("missing retry deadline: %v", err)
				}
			}
		})
	}
}

func mustJSONPath(t *testing.T, path string) []byte {
	t.Helper()
	data, err := json.Marshal(path)
	if err != nil {
		t.Error(err)
	}
	return data
}

func writeTestBlob(w http.ResponseWriter, sha string, size int, encoded string) {
	fmt.Fprintf(w, `{"sha":%q,"size":%d,"encoding":"base64","content":%q}`, sha, size, encoded)
}
