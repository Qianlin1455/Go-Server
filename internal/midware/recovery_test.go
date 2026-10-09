package midware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecovery_(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Recovery_())

	r.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	req := httptest.NewRequest(
		http.MethodGet,
		"/panic",
		nil,
	)

	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusInternalServerError,
			w.Code,
		)
	}
}
