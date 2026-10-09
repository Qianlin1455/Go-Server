package midware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
}
