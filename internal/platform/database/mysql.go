// Package database creates and verifies the application's shared SQL
// connection pool.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"internshiplab/order-api/internal/platform/config"
)

const mysqlConnectionCollation = "utf8mb4_0900_ai_ci"

// OpenMySQL constructs a MySQL pool, applies its limits, and verifies the
// connection before returning. The caller owns the returned pool and must
// close it during application shutdown.
func OpenMySQL(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	driverConfig, err := newMySQLConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build MySQL configuration: %w", err)
	}

	db, err := sql.Open("mysql", driverConfig.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open MySQL pool: %w", err)
	}
	configurePool(db, cfg)

	pingContext, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := db.PingContext(pingContext); err != nil {
		// A pool that did not pass startup verification must not leave idle
		// resources behind. Ownership transfers to the caller only on success.
		closeErr := db.Close()
		return nil, fmt.Errorf("ping MySQL: %w", errors.Join(err, closeErr))
	}

	return db, nil
}

func newMySQLConfig(cfg config.DatabaseConfig) (*mysql.Config, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate database configuration: %w", err)
	}

	driverConfig := mysql.NewConfig()
	driverConfig.User = cfg.User
	driverConfig.Passwd = cfg.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	driverConfig.DBName = cfg.Name
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	driverConfig.Timeout = cfg.ConnectTimeout
	driverConfig.ReadTimeout = cfg.ReadTimeout
	driverConfig.WriteTimeout = cfg.WriteTimeout

	// MySQL 8.4 supports utf8mb4_0900_ai_ci. Setting both character set and
	// collation makes connection behavior independent from server defaults.
	if err := driverConfig.Apply(mysql.Charset("utf8mb4", mysqlConnectionCollation)); err != nil {
		return nil, fmt.Errorf("set MySQL connection character set: %w", err)
	}
	// Loc controls how DATETIME values are decoded; time_zone controls MySQL
	// functions such as CURRENT_TIMESTAMP for every pooled connection.
	if driverConfig.Params == nil {
		driverConfig.Params = make(map[string]string)
	}
	driverConfig.Params["time_zone"] = "'+00:00'"

	return driverConfig, nil
}

func configurePool(db *sql.DB, cfg config.DatabaseConfig) {
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
}
