// Package retry retries an idempotent call the other side asked to try again
// later, such as an HTTP request answered 429, with exponential backoff and
// jitter.
package retry

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Defaults of a Policy.
const (
	DefaultMaxRetries = 4
	DefaultBase       = 500 * time.Millisecond
	// DefaultMaxWait caps one wait, a Retry-After the server names included,
	// so a misbehaving server cannot hold a request for long.
	DefaultMaxWait = 30 * time.Second
)

// Policy is how often and how long to wait between attempts.
type Policy struct {
	MaxRetries int
	// Base is the first backoff; each retry doubles it, up to MaxWait.
	Base    time.Duration
	MaxWait time.Duration
	// jitter returns a number in [0, 1); nil means math/rand.
	jitter func() float64
}

// Default is DefaultMaxRetries retries from DefaultBase, each wait at most
// DefaultMaxWait.
func Default() Policy {
	return Policy{MaxRetries: DefaultMaxRetries, Base: DefaultBase, MaxWait: DefaultMaxWait}
}

// Attempt makes one call. It reports retry when the other side asked to try
// again later, with the wait it named (0 for none).
type Attempt func() (retry bool, after time.Duration, err error)

// Do runs attempt until it succeeds, fails without asking for a retry, or has
// been retried MaxRetries times; it returns the last attempt's error. Between
// attempts it waits the named wait, or else an exponential backoff with equal
// jitter (half fixed, half random), never more than MaxWait. It stops early
// when ctx ends.
func (p Policy) Do(ctx context.Context, attempt Attempt) error {
	backoff := p.Base
	for n := 0; ; n++ {
		retry, after, err := attempt()
		if !retry || n >= p.MaxRetries {
			return err
		}
		wait := after
		if wait <= 0 {
			wait = backoff/2 + time.Duration(p.random()*float64(backoff/2))
		}
		wait = min(wait, p.MaxWait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, p.MaxWait)
	}
}

func (p Policy) random() float64 {
	if p.jitter != nil {
		return p.jitter()
	}
	return rand.Float64()
}

// After reads the Retry-After header as a number of seconds; 0 when it is
// missing or not a positive number.
func After(h http.Header) time.Duration {
	if secs, err := strconv.Atoi(h.Get("Retry-After")); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
