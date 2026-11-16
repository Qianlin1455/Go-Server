package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const normalCompletion = `{"choices":[{"message":{"role":"assistant","content":"  raw answer\n"},"finish_reason":"stop"}]}`

func testClient(t *testing.T, upstream *httptest.Server) *Client {
	t.Helper()
	setModelEnv(t, upstream.URL+"/v1/", "server-model", "fixture-secret")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.http.CloseIdleConnections)
	return client
}

func TestCompleteRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Error("unexpected HTTP request")
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("incorrect Authorization header")
		}
		var fields map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			t.Error(err)
			return
		}
		if len(fields) != 3 || string(fields["model"]) != `"server-model"` || string(fields["stream"]) != "false" {
			t.Error("unexpected configuration, streaming or tool options")
		}
		var messages []struct{ Role, Content string }
		if err := json.Unmarshal(fields["messages"], &messages); err != nil || len(messages) != 2 ||
			messages[0].Role != "system" || messages[0].Content != "system" ||
			messages[1].Role != "user" || messages[1].Content != "text\n源代码" {
			t.Error("unexpected message payload")
		}
		fmt.Fprint(w, normalCompletion)
	}))
	defer upstream.Close()
	var client Completer = testClient(t, upstream)
	t.Setenv("AI_MODEL", "different-after-load")
	got, err := client.Complete(t.Context(), Request{SystemPrompt: "system", UserPrompt: "text\n源代码"})
	if err != nil || got.Content != "  raw answer\n" {
		t.Fatalf("completion failed or normalized content: %v", err)
	}
}

func TestCompleteUpstreamStatus(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				fmt.Fprint(w, "upstream-private-body fixture-secret")
			}))
			defer upstream.Close()
			got, err := testClient(t, upstream).Complete(t.Context(), Request{})
			var statusErr *HTTPStatusError
			if got.Content != "" || !errors.Is(err, ErrModelUpstream) || !errors.As(err, &statusErr) || statusErr.StatusCode != status || calls.Load() != 1 ||
				strings.Contains(err.Error(), "upstream-private-body") || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("incorrect status classification, result, retry or redaction, calls=%d", calls.Load())
			}
		})
	}
}

func TestCompleteRejectsRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		fmt.Fprint(w, normalCompletion)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	_, err := testClient(t, upstream).Complete(t.Context(), Request{})
	var statusErr *HTTPStatusError
	if !errors.Is(err, ErrModelUpstream) || !errors.As(err, &statusErr) ||
		statusErr.StatusCode != http.StatusTemporaryRedirect || redirected.Load() {
		t.Fatal("redirect did not return the original status without a second request")
	}
}

func TestCompleteContextAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		clientTime time.Duration
		parentTime time.Duration
		cancel     bool
		sendHeader bool
		want       error
	}{
		{"client timeout before headers", 80 * time.Millisecond, 0, false, false, ErrModelTimeout},
		{"client timeout reading body", 80 * time.Millisecond, 0, false, true, ErrModelTimeout},
		{"parent cancellation", defaultTimeout, 0, true, false, context.Canceled},
		{"parent deadline", defaultTimeout, 80 * time.Millisecond, false, true, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.parentTime > 0 {
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithTimeout(ctx, tc.parentTime)
				defer cancelDeadline()
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Finish reading the POST so the HTTP/1 server can detect
				// the connection closing while it is waiting to respond.
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
					return
				}
				if tc.sendHeader {
					fmt.Fprint(w, `{"choices":`)
					http.NewResponseController(w).Flush()
				}
				if tc.cancel {
					cancel()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
					t.Error("request context was not canceled at the server")
				}
			}))
			defer upstream.Close()
			client := testClient(t, upstream)
			if client.http.Timeout != 8*time.Second {
				t.Fatal("default client timeout is not 8s")
			}
			client.http.Timeout = tc.clientTime
			_, err := client.Complete(ctx, Request{})
			if !errors.Is(err, tc.want) || (tc.want == ErrModelTimeout && errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCompleteRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"malformed JSON", `{"choices":[`, ErrModelInvalidResponse},
		{"trailing JSON", normalCompletion + ` {}`, ErrModelInvalidResponse},
		{"empty choices", `{"choices":[]}`, ErrModelInvalidResponse},
		{"blank content", `{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`, ErrModelInvalidResponse},
		{"null content", `{"choices":[{"message":{"content":null},"finish_reason":"stop"}]}`, ErrModelInvalidResponse},
		{"missing finish reason", `{"choices":[{"message":{"content":"text"}}]}`, ErrModelInvalidResponse},
		{"length", `{"choices":[{"message":{"content":"text"},"finish_reason":"length"}]}`, ErrModelInvalidResponse},
		{"tool calls", `{"choices":[{"message":{"content":"text","tool_calls":[{"id":"call-1"}]},"finish_reason":"stop"}]}`, ErrModelInvalidResponse},
		{"null tool calls", `{"choices":[{"message":{"content":"text","tool_calls":null},"finish_reason":"stop"}]}`, ErrModelInvalidResponse},
		{"function calls", `{"choices":[{"message":{"content":"text","function_call":{"name":"test"}},"finish_reason":"stop"}]}`, ErrModelInvalidResponse},
		{"large body", normalCompletion + strings.Repeat(" ", maxResponseBytes), ErrModelResponseTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer upstream.Close()
			got, err := testClient(t, upstream).Complete(t.Context(), Request{})
			if got.Content != "" || !errors.Is(err, tc.want) {
				t.Fatalf("got content length %d, err=%v, want %v", len(got.Content), err, tc.want)
			}
		})
	}
}

func TestCompleteBoundsAndRedacts(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"private-upstream-response":`)
	}))
	defer upstream.Close()
	client := testClient(t, upstream)
	for _, prompt := range []string{
		strings.Repeat("x", maxRequestBytes+1),
		strings.Repeat("\n", maxRequestBytes/2), // raw bytes fit, escaped JSON bytes do not
	} {
		if _, err := client.Complete(t.Context(), Request{UserPrompt: prompt}); !errors.Is(err, ErrModelRequestTooLarge) || calls.Load() != 0 {
			t.Fatal("oversized request reached the upstream")
		}
	}

	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(original)
	_, err := client.Complete(t.Context(), Request{SystemPrompt: "private-system-prompt", UserPrompt: "private-source"})
	if !errors.Is(err, ErrModelInvalidResponse) || calls.Load() != 1 {
		t.Fatalf("unexpected malformed response classification: %v", err)
	}
	diagnostics := fmt.Sprintf("%v %+v %#v %v %+v %#v %v %s", client, client, client,
		client.config, client.config, client.config, err, logs.String())
	for _, sensitive := range []string{"fixture-secret", "private-system-prompt", "private-source", "private-upstream-response"} {
		if strings.Contains(diagnostics, sensitive) {
			t.Fatal("diagnostics contained sensitive data")
		}
	}
}
