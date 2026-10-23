//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"

	platformconfig "internshiplab/order-api/internal/platform/config"
	platformdatabase "internshiplab/order-api/internal/platform/database"
)

const testDatabasePrefix = "order_api_it_"

func TestMigrations(t *testing.T) {
	adminDB, cfg := openAdminDatabase(t)
	assertMySQL84OrNewer(t, adminDB)
	databaseName := uniqueDatabaseName(t)

	createIsolatedDatabase(t, adminDB, databaseName)
	t.Cleanup(func() {
		dropIsolatedDatabase(t, adminDB, databaseName)
		_ = adminDB.Close()
	})

	cfg.DBName = databaseName
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open isolated test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping isolated test database: %v", err)
	}

	migrationsDir := migrationDirectory(t)
	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatalf("configure goose MySQL dialect: %v", err)
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		t.Fatalf("migrate isolated database up: %v", err)
	}
	assertSchemaVersion(t, db, 6)
	assertTables(t, db, databaseName, []string{"inventories", "order_items", "orders", "products"})

	t.Run("SKU normalization and uniqueness", func(t *testing.T) {
		result, err := db.ExecContext(ctx, `
			INSERT INTO products (sku, name, price_cents, status)
			VALUES (?, ?, ?, ?)`, "SKU-001", "Integration product", 1999, "ACTIVE")
		if err != nil {
			t.Fatalf("insert valid product: %v", err)
		}

		productID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read inserted product ID: %v", err)
		}

		if _, err := db.ExecContext(ctx, `
			INSERT INTO products (sku, name, price_cents, status)
			VALUES (?, ?, ?, ?)`, "SKU-001", "Duplicate product", 2999, "ACTIVE"); err == nil {
			t.Fatal("duplicate SKU unexpectedly succeeded")
		}

		if _, err := db.ExecContext(ctx, `
			INSERT INTO products (sku, name, price_cents, status)
			VALUES (?, ?, ?, ?)`, "sku-002", "Lowercase SKU", 2999, "ACTIVE"); err == nil {
			t.Fatal("lowercase SKU unexpectedly succeeded")
		}

		if _, err := db.ExecContext(ctx, `
			INSERT INTO inventories (product_id, available_qty)
			VALUES (?, ?)`, productID, 10); err != nil {
			t.Fatalf("insert valid inventory: %v", err)
		}

		if _, err := db.ExecContext(ctx, `
			UPDATE inventories SET available_qty = ? WHERE product_id = ?`, -1, productID); err == nil {
			t.Fatal("negative inventory unexpectedly succeeded")
		}
	})

	t.Run("foreign keys", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO inventories (product_id, available_qty)
			VALUES (?, ?)`, uint64(9_999_999_999), 1); err == nil {
			t.Fatal("inventory without a product unexpectedly succeeded")
		}

		productResult, err := db.ExecContext(ctx, `
			INSERT INTO products (sku, name, price_cents, status)
			VALUES (?, ?, ?, ?)`, "SKU-FK", "Foreign key product", 1099, "ACTIVE")
		if err != nil {
			t.Fatalf("insert product for foreign-key tests: %v", err)
		}
		productID, err := productResult.LastInsertId()
		if err != nil {
			t.Fatalf("read foreign-key product ID: %v", err)
		}

		requestHash := make([]byte, 32)
		orderResult, err := db.ExecContext(ctx, `
			INSERT INTO orders (order_no, idempotency_key, request_hash, status, total_amount_cents)
			VALUES (?, ?, ?, ?, ?)`, "ORDER-FK", []byte("fk-test"), requestHash, "CREATED", 1099)
		if err != nil {
			t.Fatalf("insert order for foreign-key tests: %v", err)
		}
		orderID, err := orderResult.LastInsertId()
		if err != nil {
			t.Fatalf("read foreign-key order ID: %v", err)
		}

		if _, err := db.ExecContext(ctx, `
			INSERT INTO order_items
			    (order_id, product_id, sku_snapshot, name_snapshot, unit_price_cents, quantity)
			VALUES (?, ?, ?, ?, ?, ?)`, uint64(9_999_999_999), productID, "SKU-FK", "Foreign key product", 1099, 1); err == nil {
			t.Fatal("order item without an order unexpectedly succeeded")
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO order_items
			    (order_id, product_id, sku_snapshot, name_snapshot, unit_price_cents, quantity)
			VALUES (?, ?, ?, ?, ?, ?)`, orderID, uint64(9_999_999_999), "SKU-FK", "Foreign key product", 1099, 1); err == nil {
			t.Fatal("order item without a product unexpectedly succeeded")
		}
	})

	t.Run("binary idempotency keys", func(t *testing.T) {
		requestHash := make([]byte, 32)
		for index := range requestHash {
			requestHash[index] = byte(index)
		}

		if _, err := db.ExecContext(ctx, `
			INSERT INTO orders (order_no, idempotency_key, request_hash, status, total_amount_cents)
			VALUES (?, ?, ?, ?, ?)`, "ORDER-001", []byte("Case-Key"), requestHash, "CREATED", 1999); err != nil {
			t.Fatalf("insert order with first idempotency key: %v", err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO orders (order_no, idempotency_key, request_hash, status, total_amount_cents)
			VALUES (?, ?, ?, ?, ?)`, "ORDER-002", []byte("case-key"), requestHash, "CREATED", 1999); err != nil {
			t.Fatalf("case-distinct idempotency key was rejected: %v", err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO orders (order_no, idempotency_key, request_hash, status, total_amount_cents)
			VALUES (?, ?, ?, ?, ?)`, "ORDER-003", []byte("Case-Key"), requestHash, "CREATED", 1999); err == nil {
			t.Fatal("duplicate idempotency key unexpectedly succeeded")
		}
	})

	if err := goose.DownTo(db, migrationsDir, 0); err != nil {
		t.Fatalf("roll isolated database back to version 0: %v", err)
	}
	assertSchemaVersion(t, db, 0)
	assertTables(t, db, databaseName, nil)

	if err := goose.Up(db, migrationsDir); err != nil {
		t.Fatalf("re-migrate isolated database: %v", err)
	}
	assertSchemaVersion(t, db, 6)
	assertTables(t, db, databaseName, []string{"inventories", "order_items", "orders", "products"})
}

func TestApplicationDatabaseCredentials(t *testing.T) {
	loaded, err := platformconfig.Load()
	if err != nil {
		t.Fatalf("load application configuration: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := platformdatabase.OpenMySQL(ctx, loaded.Database)
	if err != nil {
		t.Fatalf("open application database connection: %v", err)
	}
	defer db.Close()

	var one int
	var sessionTimeZone string
	if err := db.QueryRowContext(ctx, "SELECT 1, @@session.time_zone").Scan(&one, &sessionTimeZone); err != nil {
		t.Fatalf("query with application database credentials: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", one)
	}
	if sessionTimeZone != "+00:00" {
		t.Fatalf("session time_zone = %q, want +00:00", sessionTimeZone)
	}
}

func openAdminDatabase(t *testing.T) (*sql.DB, *mysql.Config) {
	t.Helper()

	rootPassword := os.Getenv("MYSQL_ROOT_PASSWORD")
	if rootPassword == "" {
		t.Fatal("MYSQL_ROOT_PASSWORD must be set for integration tests")
	}

	host := envOrDefault("TEST_MYSQL_HOST", "127.0.0.1")
	port := envOrDefault("TEST_MYSQL_PORT", envOrDefault("DB_PORT", "13307"))
	cfg := mysql.NewConfig()
	cfg.User = "root"
	cfg.Passwd = rootPassword
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open MySQL admin connection: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("connect to MySQL admin account at %s: %v", cfg.Addr, err)
	}

	return db, cfg
}

func assertMySQL84OrNewer(t *testing.T, db *sql.DB) {
	t.Helper()
	var version string
	if err := db.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		t.Fatalf("read MySQL version: %v", err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil {
		t.Fatalf("parse MySQL version %q: %v", version, err)
	}
	if major < 8 || (major == 8 && minor < 4) {
		t.Fatalf("MySQL version = %s, require at least 8.4", version)
	}
}

func uniqueDatabaseName(t *testing.T) string {
	t.Helper()

	randomBytes := make([]byte, 6)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("generate test database suffix: %v", err)
	}
	return fmt.Sprintf("%s%d_%s", testDatabasePrefix, time.Now().UTC().Unix(), hex.EncodeToString(randomBytes))
}

func createIsolatedDatabase(t *testing.T, db *sql.DB, databaseName string) {
	t.Helper()
	validateTestDatabaseName(t, databaseName)
	if _, err := db.Exec("CREATE DATABASE `" + databaseName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatalf("create isolated database %q: %v", databaseName, err)
	}
}

func dropIsolatedDatabase(t *testing.T, db *sql.DB, databaseName string) {
	t.Helper()
	validateTestDatabaseName(t, databaseName)
	if _, err := db.Exec("DROP DATABASE IF EXISTS `" + databaseName + "`"); err != nil {
		t.Errorf("drop isolated database %q: %v", databaseName, err)
	}
}

func validateTestDatabaseName(t *testing.T, databaseName string) {
	t.Helper()
	if !strings.HasPrefix(databaseName, testDatabasePrefix) {
		t.Fatalf("refusing to alter non-test database %q", databaseName)
	}
	for _, r := range databaseName {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			t.Fatalf("unsafe test database name %q", databaseName)
		}
	}
}

func migrationDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "migrations"))
}

func assertSchemaVersion(t *testing.T, db *sql.DB, want int64) {
	t.Helper()
	got, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read goose schema version: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}

func assertTables(t *testing.T, db *sql.DB, databaseName string, want []string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = ?
		  AND table_name IN ('products', 'inventories', 'orders', 'order_items')
		ORDER BY table_name`, databaseName)
	if err != nil {
		t.Fatalf("query migrated tables: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			t.Fatalf("scan migrated table: %v", err)
		}
		got = append(got, tableName)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated tables: %v", err)
	}

	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("migrated tables = %v, want %v", got, want)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
