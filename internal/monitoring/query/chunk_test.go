package query

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("id-%05d", i)
	}
	return out
}

func TestChunksBoundaries(t *testing.T) {
	for _, tc := range []struct {
		n, max int
		sizes  []int
	}{
		{0, 255, nil},
		{255, 255, []int{255}},
		{256, 255, []int{255, 1}},
		{3000, 255, []int{255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 195}},
	} {
		chunks, err := Chunks(ids(tc.n), tc.max)
		if err != nil {
			t.Fatalf("Chunks(%d, %d): %v", tc.n, tc.max, err)
		}
		var sizes []int
		for _, c := range chunks {
			sizes = append(sizes, len(c))
		}
		if !reflect.DeepEqual(sizes, tc.sizes) {
			t.Errorf("Chunks(%d, %d) sizes = %v, want %v", tc.n, tc.max, sizes, tc.sizes)
		}
	}

	if _, err := Chunks(ids(3), 0); err == nil {
		t.Fatal("Chunks with size 0 succeeded")
	}
}

func TestChunksAreStable(t *testing.T) {
	a, _ := Chunks([]string{"c", "a", "b", "a"}, 2)
	b, _ := Chunks([]string{"b", "c", "a"}, 2)
	want := [][]string{{"a", "b"}, {"c"}}
	if !reflect.DeepEqual(a, want) || !reflect.DeepEqual(b, want) {
		t.Fatalf("Chunks = %v and %v, want %v for the same set in any order", a, b, want)
	}
}

func TestRunChunksOrderAndConcurrency(t *testing.T) {
	var active, peak atomic.Int32
	got, err := RunChunks(context.Background(), ids(3000), ChunkLimits{MaxIDs: 255, MaxConcurrent: 4},
		func(_ context.Context, chunk []string) (string, error) {
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			return chunk[0], nil
		})
	if err != nil {
		t.Fatalf("RunChunks: %v", err)
	}
	if len(got) != 12 || got[0] != "id-00000" || got[11] != "id-02805" {
		t.Fatalf("results = %v, want 12 in chunk order", got)
	}
	if p := peak.Load(); p > 4 {
		t.Fatalf("peak concurrency = %d, want at most 4", p)
	}
}

func TestRunChunksStopsOnError(t *testing.T) {
	var calls atomic.Int32
	boom := errors.New("backend refused")
	_, err := RunChunks(context.Background(), ids(3000), ChunkLimits{MaxIDs: 255, MaxConcurrent: 1},
		func(ctx context.Context, chunk []string) (int, error) {
			if calls.Add(1) == 2 {
				return 0, boom
			}
			return len(chunk), ctx.Err()
		})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "chunk 2 of 12") {
		t.Fatalf("err = %v, want the second chunk's error", err)
	}
	if n := calls.Load(); n > 3 {
		t.Fatalf("%d chunks ran after the error, want the rest skipped", n)
	}
}

func TestRunChunksHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunChunks(ctx, ids(10), ChunkLimits{MaxIDs: 2, MaxConcurrent: 2},
		func(context.Context, []string) (int, error) { return 0, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
