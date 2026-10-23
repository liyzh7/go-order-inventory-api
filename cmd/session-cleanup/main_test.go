package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"internshiplab/order-api/internal/sessioncleanup"
)

func TestParseOptions(t *testing.T) {
	opts, timeout, err := parseOptions(nil)
	if err != nil || opts != sessioncleanup.DefaultOptions() || timeout != 30*time.Second {
		t.Fatalf("options=%+v timeout=%v err=%v", opts, timeout, err)
	}
	opts, timeout, err = parseOptions([]string{"-retention=48h", "-batch-size=2", "-max-batches=3", "-timeout=5s"})
	if err != nil || opts.Retention != 48*time.Hour || opts.BatchSize != 2 || opts.MaxBatches != 3 || timeout != 5*time.Second {
		t.Fatalf("options=%+v timeout=%v err=%v", opts, timeout, err)
	}
	for _, args := range [][]string{{"-retention=0"}, {"-batch-size=1001"}, {"-max-batches=0"}, {"-timeout=0"}, {"unexpected"}, {"-password=never-log-this-secret"}} {
		_, _, err := parseOptions(args)
		if !errors.Is(err, sessioncleanup.ErrInvalidOptions) || strings.Contains(err.Error(), "never-log-this-secret") {
			t.Fatalf("unsafe error=%v", err)
		}
	}
}

func TestErrorCategoriesContainNoDatabaseDetails(t *testing.T) {
	for _, test := range []struct {
		err      error
		category string
	}{
		{sessioncleanup.ErrInvalidOptions, "invalid_argument"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "timeout"}, {errors.New("db-password=never-log-this-secret"), "database_error"},
	} {
		if got := errorCategory(test.err); got != test.category || strings.Contains(got, "never-log-this-secret") {
			t.Fatalf("category=%q", got)
		}
	}
}
