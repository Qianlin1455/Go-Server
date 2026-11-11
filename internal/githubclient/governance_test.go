package githubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenAndSanitization(t *testing.T) {
	for _, token := range []string{"", "test-secret", "bad\nsecret"} {
		t.Run(fmt.Sprintf("length%d", len(token)), func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", token)
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			var calls atomic.Int32
			c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				want := ""
				if token != "" {
					want = "Bearer " + token
				}
				if r.Header.Get("Authorization") != want {
					t.Error("incorrect authorization")
				}
				w.WriteHeader(401)
				fmt.Fprint(w, token)
			})
			if strings.Contains(fmt.Sprintf("%v %+v %#v", c, c, c), "secret") {
				t.Fatal("credential in debug output")
			}

			var dst any
			err := c.doJSON(context.Background(), "/repos/o/r", maxMetadata, &dst)
			if err == nil || strings.Contains(err.Error()+logs.String(), "secret") {
				t.Fatalf("unsafe error/log")
			}
			if calls.Load() > 1 {
				t.Fatal("authentication retried")
			}
			if token != "bad\nsecret" && !errors.Is(err, ErrForbidden) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	c := NewClient()
	c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("test-secret internal-host") })
	var dst any
	if err := c.doJSON(context.Background(), "/repos/o/r", maxMetadata, &dst); err != ErrUpstream {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestStructuredRateLimitsAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		status                 int
		remaining, retry, body string
		want                   error
		kind                   RateLimitKind
		calls                  int
	}{
		{"primary", 403, "0", "", "", ErrRateLimited, PrimaryRateLimit, 1},
		{"429", 429, "", "", "", ErrRateLimited, SecondaryRateLimit, 1},
		{"secondary", 403, "4", "120", "", ErrRateLimited, SecondaryRateLimit, 1},
		{"date", 403, "4", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "", ErrRateLimited, SecondaryRateLimit, 1},
		{"body", 403, "4", "", `{"message":"You have exceeded a secondary rate limit."}`, ErrRateLimited, SecondaryRateLimit, 1},
		{"forbidden", 403, "4", "", "", ErrForbidden, "", 1},
		{"missing", 404, "", "", "", ErrNotFound, "", 1},
		{"upstream", 503, "", "", "", ErrUpstream, "", 2},
		{"upstreamRetryAfter", 503, "", "120", "", ErrUpstream, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			reset := time.Now().Add(time.Hour).Unix()
			c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("X-RateLimit-Limit", "5000")
				w.Header().Set("X-RateLimit-Remaining", tc.remaining)
				w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset))
				w.Header().Set("X-RateLimit-Resource", "core")
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			var dst any
			err := c.doJSON(context.Background(), "/repos/o/r", maxMetadata, &dst)
			if !errors.Is(err, tc.want) || int(calls.Load()) != tc.calls {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			if tc.kind != "" {
				var limit *RateLimitError
				if !errors.As(err, &limit) || limit.Kind != tc.kind || limit.Reset.Unix() != reset || limit.Limit != 5000 || limit.Resource != "core" || limit.RetryAfterSeconds(time.Now()) < 1 {
					t.Fatalf("bad quota: %+v", limit)
				}
			}
		})
	}
}

type memoryCache struct {
	mu   sync.Mutex
	data map[string][]byte
	fail bool
}

func (m *memoryCache) Get(_ context.Context, k string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return nil, false, errors.New("offline")
	}
	v, ok := m.data[k]
	return append([]byte(nil), v...), ok, nil
}
func (m *memoryCache) Set(_ context.Context, k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("offline")
	}
	m.data[k] = append([]byte(nil), v...)
	return nil
}
func TestRepositoryCache(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(429)
			return
		}
		if strings.Contains(r.URL.Path, "/git/ref/") {
			fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
		} else {
			fmt.Fprint(w, `{"private":false,"visibility":"public","default_branch":"main"}`)
		}
	})
	cache := &memoryCache{data: map[string][]byte{}}
	c.cache = cache
	get := func(repo string) {
		t.Helper()
		info, err := c.GetPublicRepository(context.Background(), "owner", repo)
		if err != nil || info == nil || info.Repo != repo || info.LastSHA != strings.Repeat("a", 40) {
			t.Fatalf("info=%+v err=%v", info, err)
		}
	}
	get("r")
	get("r")
	if calls.Load() != 2 {
		t.Fatal("cache missed")
	}
	get("other")
	if calls.Load() != 4 {
		t.Fatal("keys collided")
	}
	key := repositoryKey("owner", "r")
	for _, corrupt := range []string{"expired", "sha", "owner", "branch", "public", "json"} {
		var snap repositorySnapshot
		_ = json.Unmarshal(cache.data[key], &snap)
		switch corrupt {
		case "expired":
			snap.ExpiresAt = time.Now().Add(-time.Second)
		case "sha":
			snap.Info.LastSHA = "bad"
		case "owner":
			snap.Info.Owner = "another"
		case "branch":
			snap.Info.DefaultBranch = ""
		case "public":
			snap.Public = false
		}
		data, _ := json.Marshal(snap)
		if corrupt == "json" {
			data = []byte("invalid")
		}
		cache.data[key] = data
		before := calls.Load()
		get("r")
		if calls.Load() != before+2 {
			t.Fatalf("accepted corrupt %s", corrupt)
		}
	}
	cache.fail = true
	before := calls.Load()
	get("r")
	get("r")
	if calls.Load() != before+4 {
		t.Fatal("offline cache did not fall back")
	}
	cache.fail = false
	fail.Store(true)
	for i := 0; i < 2; i++ {
		_, err := c.GetPublicRepository(context.Background(), "owner", "failure")
		if !errors.Is(err, ErrRateLimited) {
			t.Fatal(err)
		}
	}
	if _, ok := cache.data[repositoryKey("owner", "failure")]; ok {
		t.Fatal("cached failure")
	}
	fail.Store(false)
	get("failure")
}
func TestSingleflight(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		if strings.Contains(r.URL.Path, "/git/ref/") {
			fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
		} else {
			fmt.Fprint(w, `{"visibility":"public","default_branch":"main"}`)
		}
	})
	c.cache = &memoryCache{data: map[string][]byte{}}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := c.GetPublicRepository(context.Background(), "o", "r")
			if err != nil || info == nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
}

func TestFailedLookupsNeverCached(t *testing.T) {
	for _, mode := range []string{"timeout", "503", "private", "invalid-sha"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "timeout":
					<-r.Context().Done()
					return
				case "503":
					w.WriteHeader(503)
					return
				case "private":
					fmt.Fprint(w, `{"private":true,"visibility":"private","default_branch":"main"}`)
					return
				}
				if strings.Contains(r.URL.Path, "/git/ref/") {
					fmt.Fprint(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":"bad"}}`)
				} else {
					fmt.Fprint(w, `{"visibility":"public","default_branch":"main"}`)
				}
			})
			c.httpClient.Timeout = 20 * time.Millisecond
			m := &memoryCache{data: map[string][]byte{}}
			c.cache = m
			for i := 0; i < 2; i++ {
				if _, err := c.GetPublicRepository(context.Background(), "o", "r"); err == nil {
					t.Fatal("accepted failure")
				}
			}
			if len(m.data) != 0 || calls.Load() < 2 {
				t.Fatalf("cached failure: calls=%d", calls.Load())
			}
		})
	}
}
func TestRetryRecovery(t *testing.T) {
	var calls atomic.Int32
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	var dst any
	if err := c.doJSON(context.Background(), "/repos/o/r", maxMetadata, &dst); err != nil || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
func TestCanceledCacheWaiter(t *testing.T) {
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c.httpClient.Timeout = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetPublicRepository(ctx, "o", "r"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
