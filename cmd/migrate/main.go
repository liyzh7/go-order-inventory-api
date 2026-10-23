package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/pressly/goose/v3"

	"internshiplab/order-api/internal/platform/config"
	"internshiplab/order-api/internal/platform/database"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	directory := flags.String("dir", "migrations", "directory containing Goose SQL migrations")
	allowDestructiveDown := flags.Bool("allow-destructive-down", false, "acknowledge that down removes schema objects")
	expectedDatabase := flags.String("expected-database", "", "database name that must match DB_NAME for down")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: go run ./cmd/migrate [-dir migrations] <up|down|status|version>")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	openContext, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()
	db, err := database.OpenMySQL(openContext, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("select MySQL migration dialect: %w", err)
	}

	switch flags.Arg(0) {
	case "up":
		if err := goose.Up(db, *directory); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
	case "down":
		// A rollback can drop tables and data. Requiring both an explicit
		// acknowledgement and the exact database name prevents an accidental
		// rollback caused by pointing local environment variables at the wrong
		// database. Down still rolls back only one version per invocation.
		if !*allowDestructiveDown || *expectedDatabase == "" || *expectedDatabase != cfg.Database.Name {
			return errors.New("down requires -allow-destructive-down and -expected-database matching DB_NAME")
		}
		if !isDisposableDatabase(cfg.Database.Name) {
			return fmt.Errorf("down is restricted to disposable *_test or order_api_it_* databases; refusing %q", cfg.Database.Name)
		}
		if err := goose.Down(db, *directory); err != nil {
			return fmt.Errorf("roll back one migration: %w", err)
		}
	case "status":
		if err := goose.Status(db, *directory); err != nil {
			return fmt.Errorf("read migration status: %w", err)
		}
	case "version":
		version, err := goose.GetDBVersion(db)
		if err != nil {
			return fmt.Errorf("read migration version: %w", err)
		}
		fmt.Printf("database migration version: %d\n", version)
	default:
		return fmt.Errorf("unknown action %q: expected up, down, status, or version", flags.Arg(0))
	}

	return nil
}

func isDisposableDatabase(databaseName string) bool {
	return strings.HasPrefix(databaseName, "order_api_it_") || strings.HasSuffix(databaseName, "_test")
}
