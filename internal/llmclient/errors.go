package llmclient

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var (
	ErrDisabled              = errors.New("model client is disabled")
	ErrInvalidConfig         = errors.New("invalid model configuration")
	ErrModelTimeout          = errors.New("model request timed out")
	ErrModelUpstream         = errors.New("model upstream failure")
	ErrModelInvalidResponse  = errors.New("invalid model response")
	ErrModelResponseTooLarge = errors.New("model response exceeds size limit")
	ErrModelRequestTooLarge  = errors.New("model request exceeds size limit")
)

// HTTPStatusError exposes only the status code, without the upstream body or URL.
type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", ErrModelUpstream, e.StatusCode)
}

func (*HTTPStatusError) Unwrap() error { return ErrModelUpstream }

func classifyHTTPError(ctx context.Context, err error) error {
	if parentErr := ctx.Err(); parentErr != nil {
		return parentErr
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return ErrModelTimeout
	}
	return ErrModelUpstream
}
