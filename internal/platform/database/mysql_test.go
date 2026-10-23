package database

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"internshiplab/order-api/internal/platform/config"
)

func TestNewMySQLConfig(t *testing.T) {
	cfg := validDatabaseConfig()
	cfg.Password = "p@ss:word/with?special&characters"

	got, err := newMySQLConfig(cfg)
	if err != nil {
		t.Fatalf("newMySQLConfig() error = %v", err)
	}

	if got.User != cfg.User || got.Passwd != cfg.Password {
		t.Errorf("driver credentials were not preserved")
	}
	if got.Net != "tcp" || got.Addr != "127.0.0.1:3306" || got.DBName != cfg.Name {
		t.Errorf("driver address = %s(%s)/%s", got.Net, got.Addr, got.DBName)
	}
	if !got.ParseTime {
		t.Error("ParseTime = false, want true")
	}
	if got.Loc != time.UTC {
		t.Errorf("Loc = %v, want UTC", got.Loc)
	}
	if got.Collation != mysqlConnectionCollation {
		t.Errorf("Collation = %q, want %q", got.Collation, mysqlConnectionCollation)
	}
	if got.Params["time_zone"] != "'+00:00'" {
		t.Errorf("time_zone = %q, want UTC offset", got.Params["time_zone"])
	}
	if got.Timeout != cfg.ConnectTimeout || got.ReadTimeout != cfg.ReadTimeout || got.WriteTimeout != cfg.WriteTimeout {
		t.Errorf("driver timeouts = (%v, %v, %v)", got.Timeout, got.ReadTimeout, got.WriteTimeout)
	}

	// Parsing the generated DSN proves that reserved password characters are
	// represented through the driver's canonical formatter rather than manual
	// string concatenation.
	parsed, err := mysql.ParseDSN(got.FormatDSN())
	if err != nil {
		t.Fatalf("ParseDSN(FormatDSN()) error = %v", err)
	}
	if parsed.Passwd != cfg.Password {
		t.Errorf("round-tripped password changed")
	}
	if !strings.Contains(got.FormatDSN(), "charset=utf8mb4") {
		t.Errorf("FormatDSN() does not request utf8mb4")
	}
}

func TestNewMySQLConfigRejectsInvalidSettings(t *testing.T) {
	cfg := validDatabaseConfig()
	cfg.Password = ""

	_, err := newMySQLConfig(cfg)
	if err == nil {
		t.Fatal("newMySQLConfig() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "DB_PASSWORD is required") {
		t.Errorf("newMySQLConfig() error = %q", err)
	}
}

func TestConfigurePoolSetsMaximumOpenConnections(t *testing.T) {
	db, err := sql.Open("mysql", "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := validDatabaseConfig()
	configurePool(db, cfg)

	if got := db.Stats().MaxOpenConnections; got != cfg.MaxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, cfg.MaxOpenConns)
	}
}

func TestOpenMySQLHonorsCanceledContextWithoutLeakingPassword(t *testing.T) {
	cfg := validDatabaseConfig()
	cfg.Password = "must-not-appear-in-errors"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := OpenMySQL(ctx, cfg)
	if db != nil {
		_ = db.Close()
		t.Fatal("OpenMySQL() db is non-nil after failed ping")
	}
	if err == nil {
		t.Fatal("OpenMySQL() error = nil, want canceled ping error")
	}
	if !strings.Contains(err.Error(), "ping MySQL") || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("OpenMySQL() error = %q, want wrapped canceled ping error", err)
	}
	if strings.Contains(err.Error(), cfg.Password) {
		t.Errorf("OpenMySQL() error leaked the database password: %q", err)
	}
}

func validDatabaseConfig() config.DatabaseConfig {
	return config.DatabaseConfig{
		Host:            "127.0.0.1",
		Port:            3306,
		Name:            "order_api",
		User:            "order_api",
		Password:        "local-secret",
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 2 * time.Minute,
		ConnectTimeout:  5 * time.Second,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		QueryTimeout:    3 * time.Second,
	}
}
