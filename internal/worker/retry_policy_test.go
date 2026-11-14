package worker

import (
	"testing"
	"time"
)

func TestRetryPolicyEqualJitter(t *testing.T) {
	for _, tt := range []struct {
		attempt uint16
		ceiling time.Duration
	}{
		{0, 5 * time.Second},
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 80 * time.Second},
		{6, 2 * time.Minute},
		{65535, 2 * time.Minute},
	} {
		minimum := retryPolicy{int64n: func(int64) int64 { return 0 }}
		maximum := retryPolicy{int64n: func(n int64) int64 { return n - 1 }}
		if got := minimum.delay(tt.attempt); got != tt.ceiling/2 {
			t.Errorf("attempt %d: minimum delay=%v, want %v", tt.attempt, got, tt.ceiling/2)
		}
		if got := maximum.delay(tt.attempt); got != tt.ceiling {
			t.Errorf("attempt %d: maximum delay=%v, want %v", tt.attempt, got, tt.ceiling)
		}
	}
}
