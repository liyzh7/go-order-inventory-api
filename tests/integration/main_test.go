//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Compare schema names before and after the entire run, including ordinary test
// failures. Never remove a schema that this process did not create. Each test's
// existing Cleanup hook owns its random database and reports DROP failures.
func TestMain(m *testing.M) {
	if os.Getenv("MYSQL_ROOT_PASSWORD") == "" {
		os.Exit(m.Run())
	}
	cfg := mysql.NewConfig()
	cfg.User = "root"
	cfg.Passwd = os.Getenv("MYSQL_ROOT_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(envOrDefault("TEST_MYSQL_HOST", "127.0.0.1"), envOrDefault("TEST_MYSQL_PORT", envOrDefault("DB_PORT", "13307")))
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration database preflight failed")
		os.Exit(1)
	}
	before, err := isolatedSchemaNames(db)
	if err != nil {
		db.Close()
		fmt.Fprintln(os.Stderr, "integration database preflight connection/query failed")
		os.Exit(1)
	}
	code := m.Run()
	after, err := isolatedSchemaNames(db)
	db.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration database cleanup verification failed")
		code = 1
	} else {
		for name := range after {
			if !before[name] {
				fmt.Fprintln(os.Stderr, "integration tests left a newly-created isolated database")
				code = 1
			}
		}
	}
	os.Exit(code)
}
func isolatedSchemaNames(db *sql.DB) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT schema_name FROM information_schema.schemata WHERE LEFT(schema_name,13) = 'order_api_it_'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = true
	}
	return names, rows.Err()
}

// The Linux CI script invokes this again from its EXIT trap, so failures or a
// panic in the main test process cannot silently bypass the residual check.
func TestCINoResidualDatabases(t *testing.T) {
	if os.Getenv("CI_EXPECT_CLEAN_DATABASES") != "1" {
		t.Skip("strict whole-server residual check is only for disposable CI MySQL")
	}
	if !strings.HasSuffix(os.Getenv("DB_NAME"), "_ci") {
		t.Fatal("strict CI residual check requires a disposable *_ci database")
	}
	db, _ := openAdminDatabase(t)
	defer db.Close()
	names, err := isolatedSchemaNames(db)
	if err != nil {
		t.Fatal("cannot verify CI database cleanup")
	}
	if len(names) != 0 {
		t.Fatalf("CI MySQL contains %d residual isolated test databases", len(names))
	}
}
