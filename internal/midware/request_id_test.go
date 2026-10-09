package midware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		requestID     string
		wantRequestID string
		wantGenerated bool
	}{
		{
			name:          "no request id",
			requestID:     "",
			wantGenerated: true,
		},
		{
			name:          "with request id",
			requestID:     "123456",
			wantRequestID: "123456",
			wantGenerated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(RequestID())

			var contextRequestID string

			r.GET("/test", func(c *gin.Context) {
				value, exists := c.Get("request_id")
				if !exists {
					t.Fatal("request_id not found in gin context")
				}

				contextRequestID, _ = value.(string)

				c.Status(http.StatusOK)
			})

			req, err := http.NewRequest(http.MethodGet, "/test", nil)
			if err != nil {
				t.Fatal(err)
			}

			if tt.requestID != "" {
				req.Header.Set("X-Request-ID", tt.requestID)
			}

			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			responseRequestID := w.Header().Get("X-Request-ID")

			if tt.wantGenerated {
				if responseRequestID == "" {
					t.Error("expected generated request id, got empty string")
				}

				if contextRequestID == "" {
					t.Error("expected request id in gin context, got empty string")
				}
			} else {
				if responseRequestID != tt.wantRequestID {
					t.Errorf(
						"response request id = %q, want %q",
						responseRequestID,
						tt.wantRequestID,
					)
				}

				if contextRequestID != tt.wantRequestID {
					t.Errorf(
						"context request id = %q, want %q",
						contextRequestID,
						tt.wantRequestID,
					)
				}
			}

			if responseRequestID != contextRequestID {
				t.Errorf(
					"response request id %q != context request id %q",
					responseRequestID,
					contextRequestID,
				)
			}
		})
	}
}
