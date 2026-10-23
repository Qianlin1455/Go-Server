package midware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()

	r.Use(Timeout(time.Millisecond * 50))

	var gotErr error
	r.GET("/timeout", func(c *gin.Context) {
		select {
		case <-time.After(100 * time.Millisecond):
			t.Error("handler finished normally,expect timeout")

		case <-c.Request.Context().Done():
			gotErr = c.Request.Context().Err()
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/timeout", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Errorf("got err =%v,want context.DeadlineExceeded", gotErr)
	}
	if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), "DEADLINE_EXCEEDED") {
		t.Fatalf("response = %d %s, want 504 timeout JSON", w.Code, w.Body.String())
	}
}

func TestTimeoutResponseBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name   string
		handle gin.HandlerFunc
		status int
		body   string
	}{
		{"fast response", func(c *gin.Context) { c.String(http.StatusOK, "ok") }, http.StatusOK, "ok"},
		{"pending status", func(c *gin.Context) { <-c.Request.Context().Done(); c.Status(http.StatusNoContent) }, http.StatusGatewayTimeout, "DEADLINE_EXCEEDED"},
		{"committed body", func(c *gin.Context) { c.String(http.StatusAccepted, "accepted"); <-c.Request.Context().Done() }, http.StatusAccepted, "accepted"},
		{"flushed headers", func(c *gin.Context) { c.Status(http.StatusAccepted); c.Writer.Flush(); <-c.Request.Context().Done() }, http.StatusAccepted, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(Timeout(10 * time.Millisecond))
			r.GET("/", tt.handle)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.body) {
				t.Fatalf("response = %d %q, want %d containing %q", w.Code, w.Body.String(), tt.status, tt.body)
			}
			if tt.status == http.StatusAccepted && w.Body.String() != tt.body {
				t.Fatal("timeout appended to committed response")
			}
		})
	}
}

func TestTimeoutParentCancellationIsNotDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timeout(time.Second))
	r.GET("/", func(c *gin.Context) {
		if !errors.Is(c.Request.Context().Err(), context.Canceled) {
			t.Error("parent cancellation lost")
		}
		c.Status(http.StatusNoContent)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("cancellation mapped to timeout: %d %s", w.Code, w.Body.String())
	}
}
