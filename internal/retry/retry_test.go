package retry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDoRetriesThenSucceeds(t *testing.T) {
	p := Policy{MaxRetries: 4, Base: time.Millisecond, MaxWait: 10 * time.Millisecond}
	calls := 0
	err := p.Do(context.Background(), func() (bool, time.Duration, error) {
		calls++
		if calls < 3 {
			return true, 0, errors.New("429")
		}
		return false, 0, nil
	})
	if err != nil || calls != 3 {
		t.Errorf("err = %v after %d calls, want success on the third", err, calls)
	}
}

func TestDoGivesUpWithLastError(t *testing.T) {
	p := Policy{MaxRetries: 2, Base: time.Millisecond, MaxWait: time.Millisecond}
	calls := 0
	err := p.Do(context.Background(), func() (bool, time.Duration, error) {
		calls++
		return true, 0, errors.New("still 429")
	})
	if err == nil || err.Error() != "still 429" || calls != 3 {
		t.Errorf("err = %v after %d calls, want the last error after 1+2 calls", err, calls)
	}
}

func TestDoDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	_ = Default().Do(context.Background(), func() (bool, time.Duration, error) {
		calls++
		return false, 0, errors.New("500")
	})
	if calls != 1 {
		t.Errorf("calls = %d", calls)
	}
}

// TestWaits checks the backoff doubles with equal jitter and that a
// server-named wait is capped.
func TestWaits(t *testing.T) {
	var waits []time.Duration
	p := Policy{MaxRetries: 3, Base: 100 * time.Millisecond, MaxWait: 300 * time.Millisecond, jitter: func() float64 { return 0.5 }}
	last := time.Now()
	afters := []time.Duration{0, 0, time.Hour}
	n := 0
	_ = p.Do(context.Background(), func() (bool, time.Duration, error) {
		now := time.Now()
		if n > 0 {
			waits = append(waits, now.Sub(last))
		}
		last = now
		if n >= len(afters) {
			return false, 0, nil
		}
		n++
		return true, afters[n-1], nil
	})
	// 100ms: 50 + 0.5*50 = 75ms; 200ms: 100 + 50 = 150ms; Retry-After 1h capped to 300ms.
	want := []time.Duration{75 * time.Millisecond, 150 * time.Millisecond, 300 * time.Millisecond}
	for i, w := range want {
		if waits[i] < w || waits[i] > w+60*time.Millisecond {
			t.Errorf("wait %d = %v, want about %v", i, waits[i], w)
		}
	}
}

func TestDoStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Policy{MaxRetries: 4, Base: time.Hour, MaxWait: time.Hour}.Do(ctx, func() (bool, time.Duration, error) {
		return true, 0, errors.New("429")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestAfter(t *testing.T) {
	for v, want := range map[string]time.Duration{"3": 3 * time.Second, "": 0, "-1": 0, "soon": 0} {
		h := http.Header{}
		h.Set("Retry-After", v)
		if got := After(h); got != want {
			t.Errorf("After(%q) = %v", v, got)
		}
	}
}
