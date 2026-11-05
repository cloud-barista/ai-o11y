// Package query plans and runs metric queries over a backend: it splits large
// target sets into chunks a backend serves well, runs them in parallel and
// merges what they return.
package query

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// ChunkLimits bound how a backend is queried for a large set of targets.
type ChunkLimits struct {
	// MaxIDs is the most entity ids one query names. Prometheus matches up to
	// 255 alternatives in a regex through a set lookup and scans every label
	// value beyond that.
	MaxIDs int
	// MaxConcurrent is the most chunk queries in flight at once. The backend's
	// own query concurrency is shared with every other user, so this stays
	// well under it.
	MaxConcurrent int
}

// Chunks splits ids into chunks of at most maxIDs, after sorting them and
// dropping duplicates. The same set of ids always yields the same chunks, so
// a backend results cache keyed by query text keeps hitting.
func Chunks(ids []string, maxIDs int) ([][]string, error) {
	if maxIDs <= 0 {
		return nil, fmt.Errorf("chunk size must be positive, got %d", maxIDs)
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)

	var chunks [][]string
	for chunk := range slices.Chunk(sorted, maxIDs) {
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

// RunChunks splits ids with Chunks and calls run once per chunk, at most
// limits.MaxConcurrent at a time. Results come back in chunk order. The first
// error cancels the chunks still running and is returned.
func RunChunks[T any](ctx context.Context, ids []string, limits ChunkLimits, run func(ctx context.Context, chunk []string) (T, error)) ([]T, error) {
	chunks, err := Chunks(ids, limits.MaxIDs)
	if err != nil {
		return nil, err
	}
	concurrent := max(limits.MaxConcurrent, 1)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]T, len(chunks))
	slots := make(chan struct{}, concurrent)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for i, chunk := range chunks {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			result, err := run(ctx, chunk)
			if err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
					cancel()
				})
				return
			}
			results[i] = result
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
