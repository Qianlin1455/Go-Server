package worker

import "time"

const (
	retryBaseDelay = 5 * time.Second
	retryMaxDelay  = 2 * time.Minute
)

type retryPolicy struct {
	int64n func(int64) int64
}

// delay uses equal jitter at millisecond precision. Claiming the job already
// increments attemptCount, so the first failed execution uses retryBaseDelay.
func (p retryPolicy) delay(attemptCount uint16) time.Duration {
	ceiling := retryBaseDelay
	for attempt := uint16(1); attempt < attemptCount && ceiling < retryMaxDelay; attempt++ {
		if ceiling > retryMaxDelay/2 {
			ceiling = retryMaxDelay
			break
		}
		ceiling *= 2
	}

	upper := ceiling.Milliseconds()
	lower := upper / 2
	return time.Duration(lower+p.int64n(upper-lower+1)) * time.Millisecond
}
