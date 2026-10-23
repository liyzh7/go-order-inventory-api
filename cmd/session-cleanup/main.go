package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"internshiplab/order-api/internal/platform/config"
	"internshiplab/order-api/internal/platform/database"
	"internshiplab/order-api/internal/platform/securitylog"
	"internshiplab/order-api/internal/sessioncleanup"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

func main() {
	slog.SetDefault(securitylog.NewLogger(os.Stderr))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, err := securitylog.NewContext(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session cleanup failed: internal_error")
		os.Exit(1)
	}
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		// Database/driver errors can contain sensitive values. Only a fixed
		// category reaches this process output; detailed secrets are never logged.
		fmt.Fprintln(os.Stderr, "session cleanup failed:", errorCategory(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	options, timeout, err := parseOptions(arguments)
	if err != nil {
		securitylog.LogCleanup(ctx, "failure", errorCategory(err), 0, 0)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		securitylog.LogCleanup(ctx, "failure", "invalid_argument", 0, 0)
		return fmt.Errorf("%w: invalid environment configuration", sessioncleanup.ErrInvalidOptions)
	}
	db, err := database.OpenMySQL(ctx, cfg.Database)
	if err != nil {
		securitylog.LogCleanup(ctx, "failure", errorCategory(err), 0, 0)
		return err
	}
	defer db.Close()
	result, cleanupErr := sessioncleanup.NewService(mysqlstore.NewSessionCleanupStore(db, cfg.Database.QueryTimeout)).Run(ctx, options)
	outcome, category := "success", "none"
	if cleanupErr != nil {
		outcome, category = "failure", errorCategory(cleanupErr)
	}
	securitylog.LogCleanup(ctx, outcome, category, result.Deleted, result.Duration)
	if err := json.NewEncoder(output).Encode(struct {
		sessioncleanup.Result
		DurationMS int64  `json:"duration_ms"`
		Outcome    string `json:"outcome"`
	}{Result: result, DurationMS: result.Duration.Milliseconds(), Outcome: outcome}); err != nil {
		return errors.New("could not write cleanup summary")
	}
	return cleanupErr
}

func parseOptions(arguments []string) (sessioncleanup.Options, time.Duration, error) {
	options := sessioncleanup.DefaultOptions()
	flags := flag.NewFlagSet("session-cleanup", flag.ContinueOnError)
	// Do not echo arbitrary flag values: operators could accidentally paste
	// secrets into an argument. The caller receives only a safe category.
	flags.SetOutput(io.Discard)
	flags.DurationVar(&options.Retention, "retention", options.Retention, "retention after expiration or revocation (default 168h)")
	flags.IntVar(&options.BatchSize, "batch-size", options.BatchSize, "maximum rows per committed delete (1..1000)")
	flags.IntVar(&options.MaxBatches, "max-batches", options.MaxBatches, "maximum committed deletes in this invocation")
	timeout := flags.Duration("timeout", 30*time.Second, "overall cleanup timeout")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return options, 0, fmt.Errorf("%w: invalid flags", sessioncleanup.ErrInvalidOptions)
	}
	if err := options.Validate(); err != nil {
		return options, 0, err
	}
	if *timeout <= 0 {
		return options, 0, fmt.Errorf("%w: timeout must be positive", sessioncleanup.ErrInvalidOptions)
	}
	return options, *timeout, nil
}

func errorCategory(err error) string {
	switch {
	case errors.Is(err, sessioncleanup.ErrInvalidOptions):
		return "invalid_argument"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "database_error"
	}
}
