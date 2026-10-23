// Package sessioncleanup removes expired authentication material after an
// explicit retention period. It never schedules itself or alters user data.
package sessioncleanup

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultRetention  = 7 * 24 * time.Hour
	DefaultBatchSize  = 500
	DefaultMaxBatches = 100
	MaxBatchSize      = 1000
	MaxBatches        = 10000
)

var ErrInvalidOptions = errors.New("invalid session cleanup options")

// Store must apply the eligibility predicate atomically with each bounded
// delete. In particular, selecting IDs then deleting them unconditionally
// would allow concurrent changes to bypass the retention policy.
type Store interface {
	DeleteBatch(context.Context, time.Time, int) (int64, error)
}

type Options struct {
	Retention  time.Duration
	BatchSize  int
	MaxBatches int
}

func DefaultOptions() Options {
	return Options{Retention: DefaultRetention, BatchSize: DefaultBatchSize, MaxBatches: DefaultMaxBatches}
}

func (o Options) Validate() error {
	if o.Retention <= 0 || o.BatchSize < 1 || o.BatchSize > MaxBatchSize || o.MaxBatches < 1 || o.MaxBatches > MaxBatches {
		return fmt.Errorf("%w: retention must be positive, batch-size 1..%d, max-batches 1..%d", ErrInvalidOptions, MaxBatchSize, MaxBatches)
	}
	return nil
}

type Result struct {
	Deleted      int64         `json:"deleted_count"`
	Batches      int           `json:"batches"`
	LimitReached bool          `json:"limit_reached"`
	Cutoff       time.Time     `json:"cutoff"`
	Duration     time.Duration `json:"-"`
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// Run holds the cutoff fixed for the whole invocation. Each batch commits
// independently; an error returns the count of already committed batches.
func (s *Service) Run(ctx context.Context, options Options) (result Result, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	if err := options.Validate(); err != nil {
		return result, err
	}
	result.Cutoff = s.now().UTC().Add(-options.Retention).Truncate(time.Microsecond)
	for result.Batches < options.MaxBatches {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		deleted, err := s.store.DeleteBatch(ctx, result.Cutoff, options.BatchSize)
		if err != nil {
			return result, fmt.Errorf("delete session cleanup batch: %w", err)
		}
		if deleted < 0 || deleted > int64(options.BatchSize) {
			return result, errors.New("session cleanup store returned an invalid batch count")
		}
		result.Batches++
		result.Deleted += deleted
		if deleted < int64(options.BatchSize) {
			return result, nil
		}
	}
	// A full final batch means more work may remain. A later manual/scheduled
	// invocation can safely resume; this run never loops without a bound.
	result.LimitReached = true
	return result, nil
}
