package sessioncleanup

import (
	"context"
	"errors"
	"testing"
	"time"
)

type batchFunc func(context.Context, time.Time, int) (int64, error)

func (f batchFunc) DeleteBatch(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return f(ctx, cutoff, limit)
}

func TestRunUsesFixedUTCCutoffAndMultipleBatches(t *testing.T) {
	now := time.Date(2026, 10, 16, 15, 12, 30, 123456789, time.FixedZone("CST", 8*60*60))
	wantCutoff := now.UTC().Add(-DefaultRetention).Truncate(time.Microsecond)
	calls := 0
	s := NewService(batchFunc(func(_ context.Context, cutoff time.Time, limit int) (int64, error) {
		calls++
		if !cutoff.Equal(wantCutoff) || cutoff.Location() != time.UTC || limit != 2 {
			t.Fatalf("cutoff=%s limit=%d", cutoff, limit)
		}
		if calls == 3 {
			return 1, nil
		}
		return 2, nil
	}))
	s.now = func() time.Time { now = now.Add(time.Hour); return now.Add(-time.Hour) }
	options := DefaultOptions()
	options.BatchSize = 2
	result, err := s.Run(context.Background(), options)
	if err != nil || result.Deleted != 5 || result.Batches != 3 || result.LimitReached || result.Duration < 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunBoundedAndStopsOnErrors(t *testing.T) {
	wanted := errors.New("controlled database failure")
	for _, test := range []struct {
		name        string
		counts      []int64
		err         error
		max         int
		wantDeleted int64
		wantCalls   int
		limited     bool
	}{
		{"bounded", []int64{2, 2}, nil, 2, 4, 2, true},
		{"partial failure", []int64{2, 0}, wanted, 10, 2, 2, false},
		{"invalid batch count", []int64{3}, nil, 10, 0, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			s := NewService(batchFunc(func(context.Context, time.Time, int) (int64, error) {
				index := calls
				calls++
				if test.err != nil && index == 1 {
					return 0, test.err
				}
				return test.counts[index], nil
			}))
			opts := DefaultOptions()
			opts.BatchSize = 2
			opts.MaxBatches = test.max
			result, err := s.Run(context.Background(), opts)
			if result.Deleted != test.wantDeleted || calls != test.wantCalls || result.LimitReached != test.limited {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("error=%v", err)
			}
			if test.name == "invalid batch count" && err == nil {
				t.Fatal("invalid count accepted")
			}
		})
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	s := NewService(batchFunc(func(context.Context, time.Time, int) (int64, error) { calls++; cancel(); return 500, nil }))
	result, err := s.Run(ctx, DefaultOptions())
	if !errors.Is(err, context.Canceled) || calls != 1 || result.Deleted != 500 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
	_, err = s.Run(ctx, DefaultOptions())
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled request reached store: calls=%d error=%v", calls, err)
	}
}

func TestOptionsRejectUnsafeValues(t *testing.T) {
	for _, opts := range []Options{
		{0, 500, 100}, {-time.Second, 500, 100}, {time.Hour, 0, 100}, {time.Hour, 1001, 100}, {time.Hour, 1, 0}, {time.Hour, 1, 10001},
	} {
		if !errors.Is(opts.Validate(), ErrInvalidOptions) {
			t.Fatalf("accepted %+v", opts)
		}
	}
}
