package consumer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"GoServer/events"
	"GoServer/internal/githubclient"
	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/worker"

	"github.com/IBM/sarama"
	"gorm.io/gorm"
)

type retryObservingStore struct {
	JobStore
	onRetry func(*model.ImportJob)
}

func (s retryObservingStore) GetByID(ctx context.Context, id uint64) (*model.ImportJob, error) {
	job, err := s.JobStore.GetByID(ctx, id)
	if err == nil && job.Status == model.ImportJobQueued && job.AttemptCount == 1 {
		s.onRetry(job)
	}
	return job, err
}

func TestHandler_GitHubHTTPFaults(t *testing.T) {
	for _, tc := range []struct {
		name, code string
	}{
		{"client timeout", "GITHUB_TIMEOUT"},
		{"rate limiting", "GITHUB_RATE_LIMITED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupConsumerTestDB(t)
			job := createConsumerTestJob(t, db)
			store := repository.NewImportJobStore(db)
			tree := strings.Repeat("a", 40)
			reset := time.Now().UTC().Truncate(time.Second).Add(3 * time.Minute)
			var requests, metadataCalls atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch r.URL.Path {
				case "/repos/golang/go":
					if metadataCalls.Add(1) == 1 {
						if tc.code == "GITHUB_TIMEOUT" {
							<-r.Context().Done()
							return
						}
						w.Header().Set("Retry-After", "30")
						w.Header().Set("X-RateLimit-Remaining", "0")
						w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					fmt.Fprint(w, `{"visibility":"public","default_branch":"main"}`)
				case "/repos/golang/go/git/commits/" + job.RevisionSHA:
					fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, job.RevisionSHA, tree)
				case "/repos/golang/go/git/trees/" + tree:
					fmt.Fprintf(w, `{"sha":%q,"tree":[{"type":"blob","size":7}]}`, tree)
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			client := githubClientForTest(t, upstream)
			runner, err := worker.NewRunner(store, worker.NewGitHubExecutor(db, client), "http-fault-test")
			if err != nil {
				t.Fatal(err)
			}
			session := newTestConsumerSession()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			session.ctx = ctx
			observedRetry := false
			observing := retryObservingStore{JobStore: store, onRetry: func(got *model.ImportJob) {
				if observedRetry {
					t.Fatal("test did not release scheduled retry")
				}
				observedRetry = true
				if len(session.marked) != 0 || session.commitCount != 0 || requests.Load() != 1 ||
					got.LastErrorCode == nil || *got.LastErrorCode != tc.code {
					t.Fatalf("unexpected queued state: job=%+v requests=%d", got, requests.Load())
				}
				var earliest, latest time.Time
				if tc.code == "GITHUB_TIMEOUT" {
					var clocks struct{ Earliest, Latest time.Time }
					if err := db.Raw(`SELECT DATE_ADD(last_attempt_at, INTERVAL 7500000 MICROSECOND) AS earliest,
						DATE_ADD(NOW(3), INTERVAL 5 SECOND) AS latest FROM import_jobs WHERE id = ?`, job.ID).
						Scan(&clocks).Error; err != nil {
						t.Fatal(err)
					}
					earliest, latest = clocks.Earliest, clocks.Latest
				} else {
					var resetSession time.Time
					if err := db.Raw("SELECT FROM_UNIXTIME(?)", reset.Unix()).Row().Scan(&resetSession); err != nil {
						t.Fatal(err)
					}
					earliest, latest = resetSession.Add(2500*time.Millisecond), resetSession.Add(5*time.Second)
				}
				if got.AvailableAt.Before(earliest) || got.AvailableAt.After(latest) {
					t.Fatalf("available_at=%v, want [%v,%v]", got.AvailableAt, earliest, latest)
				}
				claimed, err := runner.ProcessJob(ctx, job.ID)
				if err != nil || claimed || requests.Load() != 1 {
					t.Fatalf("early retry: claimed=%t requests=%d err=%v", claimed, requests.Load(), err)
				}
				// Advance the schedule with the database clock to keep the test fast.
				if err := db.Model(&model.ImportJob{}).Where("id = ?", job.ID).
					UpdateColumn("available_at", gorm.Expr("NOW(3)")).Error; err != nil {
					t.Fatal(err)
				}
			}}
			handler, err := NewHandler(observing, runner, testDeadLetterProducer(func(*sarama.ProducerMessage) (int32, int64, error) {
				t.Fatal("retryable business error sent to DLQ")
				return 0, 0, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(events.ImportRequestedV1{
				EventID: "http-fault-test", EventType: events.ImportRequestedType, SchemaVersion: events.ImportRequestedVersion,
				OccurredAt: time.Now(), JobID: job.ID, UserID: job.UserID, ProjectID: job.ProjectID,
				RepositoryID: job.RepositoryID, RevisionSHA: job.RevisionSHA,
			})
			if err != nil {
				t.Fatal(err)
			}
			msg := &sarama.ConsumerMessage{Topic: ImportsRequestedTopic, Value: body}
			if err := handler.ConsumeClaim(session, singleMessageClaim(msg)); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetByID(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !observedRetry || got.Status != model.ImportJobSucceeded || got.AttemptCount != 2 ||
				got.ResultTreeSHA == nil || *got.ResultTreeSHA != tree || len(session.marked) != 1 ||
				session.commitCount != 1 || requests.Load() != 4 {
				t.Fatalf("unexpected terminal state: job=%+v, requests=%d, commits=%d", got, requests.Load(), session.commitCount)
			}
		})
	}
}

func githubClientForTest(t *testing.T, upstream *httptest.Server) *githubclient.Client {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "api.github.com:443" {
				return nil, fmt.Errorf("unexpected upstream address %q", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
		},
	}
	// NewClient clones the default transport. Restore it before sending requests.
	original := http.DefaultTransport
	http.DefaultTransport = transport
	client := githubclient.NewClient()
	http.DefaultTransport = original
	return client
}
