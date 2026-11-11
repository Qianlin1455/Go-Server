package githubclient

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RateLimitKind string

const (
	PrimaryRateLimit   RateLimitKind = "primary"
	SecondaryRateLimit RateLimitKind = "secondary"
)

// RateLimitError contains only parsed quota information, never response bodies.
type RateLimitError struct {
	Kind      RateLimitKind
	Limit     int64
	Remaining int64
	Reset     time.Time
	Resource  string
	RetryAt   time.Time
}

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }
func (e *RateLimitError) RetryAfterSeconds(now time.Time) int64 {
	d := e.RetryAt.Sub(now)
	if d <= 0 {
		return 1
	}
	return int64(d/time.Second) + 1
}
func quota(h http.Header, key string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(h.Get(key)), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}
func classifyRateLimit(resp *http.Response) error {
	now := time.Now()
	e := &RateLimitError{Kind: SecondaryRateLimit, Limit: quota(resp.Header, "X-RateLimit-Limit"), Remaining: quota(resp.Header, "X-RateLimit-Remaining")}
	switch resp.Header.Get("X-RateLimit-Resource") {
	case "core", "search", "graphql", "integration_manifest", "code_search":
		e.Resource = resp.Header.Get("X-RateLimit-Resource")
	}
	if n := quota(resp.Header, "X-RateLimit-Reset"); n >= 0 {
		e.Reset = time.Unix(n, 0)
	}
	retry := resp.Header.Get("Retry-After")
	if n, err := strconv.ParseInt(retry, 10, 32); err == nil && n >= 0 {
		e.RetryAt = now.Add(time.Duration(n) * time.Second)
	} else if d, err := http.ParseTime(retry); err == nil {
		e.RetryAt = d
	}
	limited := resp.StatusCode == 429 || retry != "" || e.Remaining == 0
	if e.Remaining == 0 {
		e.Kind = PrimaryRateLimit
		if e.Reset.After(e.RetryAt) {
			e.RetryAt = e.Reset
		}
	}
	if !limited {
		// Some secondary limits have no quota/retry headers. Inspect only a bounded message.
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
		if err != nil {
			return classifyRequestError(err)
		}
		if len(body) > maxMetadata {
			return ErrTooLarge
		}
		var msg struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &msg) == nil {
			message := strings.ToLower(msg.Message)
			limited = strings.Contains(message, "secondary rate limit") || strings.Contains(message, "abuse detection")
		}
	}
	if !limited {
		return ErrForbidden
	}
	if !e.RetryAt.After(now) {
		e.RetryAt = now.Add(time.Minute)
	}
	return e
}
