// Package config loads and validates process configuration from environment
// variables. It deliberately does not read .env files; callers can choose how
// environment variables are supplied without coupling the application to a
// particular local-development tool.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr              = "127.0.0.1:8080"
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPReadTimeout       = 10 * time.Second
	defaultHTTPWriteTimeout      = 15 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second
	defaultHTTPShutdownTimeout   = 10 * time.Second

	defaultDBHost            = "127.0.0.1"
	defaultDBPort            = 3306
	defaultDBMaxOpenConns    = 25
	defaultDBMaxIdleConns    = 10
	defaultDBConnMaxLifetime = 5 * time.Minute
	defaultDBConnMaxIdleTime = 2 * time.Minute
	defaultDBConnectTimeout  = 5 * time.Second
	defaultDBReadTimeout     = 5 * time.Second
	defaultDBWriteTimeout    = 5 * time.Second
	defaultDBQueryTimeout    = 3 * time.Second
)

// Config contains all process-level settings needed by the HTTP server and
// database infrastructure.
type Config struct {
	HTTP                          HTTPConfig
	Database                      DatabaseConfig
	DiagnosticsEnabled            bool
	TransactionDiagnosticsEnabled bool
}

// HTTPConfig controls the server lifecycle and protects it from connections
// that keep resources occupied indefinitely.
type HTTPConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

// DatabaseConfig contains connection details, pool limits and operation
// deadlines. Password is intentionally never included in validation errors.
type DatabaseConfig struct {
	Host            string
	Port            int
	Name            string
	User            string
	Password        string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	ConnectTimeout  time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	QueryTimeout    time.Duration
}

// Load reads configuration from the current process environment and rejects
// incomplete or internally inconsistent settings before startup continues.
func Load() (Config, error) {
	httpConfig, err := loadHTTPConfig()
	if err != nil {
		return Config{}, fmt.Errorf("load HTTP configuration: %w", err)
	}

	databaseConfig, err := loadDatabaseConfig()
	if err != nil {
		return Config{}, fmt.Errorf("load database configuration: %w", err)
	}
	diagnosticsEnabled, err := boolEnv("DIAGNOSTICS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	transactionDiagnosticsEnabled, err := boolEnv("TRANSACTION_DIAGNOSTICS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}

	config := Config{
		HTTP:                          httpConfig,
		Database:                      databaseConfig,
		DiagnosticsEnabled:            diagnosticsEnabled,
		TransactionDiagnosticsEnabled: transactionDiagnosticsEnabled,
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}

	return config, nil
}

// Validate allows startup code and tests that construct Config directly to
// enforce the same invariants as Load.
func (c Config) Validate() error {
	if err := c.HTTP.Validate(); err != nil {
		return fmt.Errorf("HTTP: %w", err)
	}
	if err := c.Database.Validate(); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if c.TransactionDiagnosticsEnabled && !c.DiagnosticsEnabled {
		return fmt.Errorf("TRANSACTION_DIAGNOSTICS_ENABLED requires DIAGNOSTICS_ENABLED")
	}
	return nil
}

// Validate checks the HTTP configuration invariants.
func (c HTTPConfig) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("HTTP_ADDR is required")
	}
	// Authentication does not make this a public production service. Binding
	// a literal loopback address remains mandatory without a deployment bypass.
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("HTTP_ADDR must bind a literal loopback IP: order APIs are local-development only")
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("HTTP_ADDR must specify a port between 1 and 65535")
	}
	if c.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("HTTP_READ_HEADER_TIMEOUT must be greater than zero")
	}
	if c.ReadTimeout <= 0 {
		return fmt.Errorf("HTTP_READ_TIMEOUT must be greater than zero")
	}
	if c.WriteTimeout <= 0 {
		return fmt.Errorf("HTTP_WRITE_TIMEOUT must be greater than zero")
	}
	if c.IdleTimeout <= 0 {
		return fmt.Errorf("HTTP_IDLE_TIMEOUT must be greater than zero")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("HTTP_SHUTDOWN_TIMEOUT must be greater than zero")
	}
	return nil
}

// Validate checks database credentials, pool limits and timeout boundaries.
func (c DatabaseConfig) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("DB_HOST is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("DB_PORT must be between 1 and 65535")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("DB_NAME is required")
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("DB_USER is required")
	}
	if c.Password == "" {
		return fmt.Errorf("DB_PASSWORD is required")
	}
	if c.MaxOpenConns <= 0 {
		return fmt.Errorf("DB_MAX_OPEN_CONNS must be greater than zero")
	}
	if c.MaxIdleConns < 0 {
		return fmt.Errorf("DB_MAX_IDLE_CONNS must not be negative")
	}
	if c.MaxIdleConns > c.MaxOpenConns {
		return fmt.Errorf("DB_MAX_IDLE_CONNS must not exceed DB_MAX_OPEN_CONNS")
	}
	if c.ConnMaxLifetime < 0 {
		return fmt.Errorf("DB_CONN_MAX_LIFETIME must not be negative")
	}
	if c.ConnMaxIdleTime < 0 {
		return fmt.Errorf("DB_CONN_MAX_IDLE_TIME must not be negative")
	}
	if c.ConnectTimeout <= 0 {
		return fmt.Errorf("DB_CONNECT_TIMEOUT must be greater than zero")
	}
	if c.ReadTimeout <= 0 {
		return fmt.Errorf("DB_READ_TIMEOUT must be greater than zero")
	}
	if c.WriteTimeout <= 0 {
		return fmt.Errorf("DB_WRITE_TIMEOUT must be greater than zero")
	}
	if c.QueryTimeout <= 0 {
		return fmt.Errorf("DB_QUERY_TIMEOUT must be greater than zero")
	}
	return nil
}

func loadHTTPConfig() (HTTPConfig, error) {
	readHeaderTimeout, err := durationEnv("HTTP_READ_HEADER_TIMEOUT", defaultHTTPReadHeaderTimeout)
	if err != nil {
		return HTTPConfig{}, err
	}
	readTimeout, err := durationEnv("HTTP_READ_TIMEOUT", defaultHTTPReadTimeout)
	if err != nil {
		return HTTPConfig{}, err
	}
	writeTimeout, err := durationEnv("HTTP_WRITE_TIMEOUT", defaultHTTPWriteTimeout)
	if err != nil {
		return HTTPConfig{}, err
	}
	idleTimeout, err := durationEnv("HTTP_IDLE_TIMEOUT", defaultHTTPIdleTimeout)
	if err != nil {
		return HTTPConfig{}, err
	}
	shutdownTimeout, err := durationEnv("HTTP_SHUTDOWN_TIMEOUT", defaultHTTPShutdownTimeout)
	if err != nil {
		return HTTPConfig{}, err
	}

	return HTTPConfig{
		Addr:              stringEnv("HTTP_ADDR", defaultHTTPAddr),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ShutdownTimeout:   shutdownTimeout,
	}, nil
}

func loadDatabaseConfig() (DatabaseConfig, error) {
	port, err := intEnv("DB_PORT", defaultDBPort)
	if err != nil {
		return DatabaseConfig{}, err
	}
	maxOpenConns, err := intEnv("DB_MAX_OPEN_CONNS", defaultDBMaxOpenConns)
	if err != nil {
		return DatabaseConfig{}, err
	}
	maxIdleConns, err := intEnv("DB_MAX_IDLE_CONNS", defaultDBMaxIdleConns)
	if err != nil {
		return DatabaseConfig{}, err
	}
	connMaxLifetime, err := durationEnv("DB_CONN_MAX_LIFETIME", defaultDBConnMaxLifetime)
	if err != nil {
		return DatabaseConfig{}, err
	}
	connMaxIdleTime, err := durationEnv("DB_CONN_MAX_IDLE_TIME", defaultDBConnMaxIdleTime)
	if err != nil {
		return DatabaseConfig{}, err
	}
	connectTimeout, err := durationEnv("DB_CONNECT_TIMEOUT", defaultDBConnectTimeout)
	if err != nil {
		return DatabaseConfig{}, err
	}
	readTimeout, err := durationEnv("DB_READ_TIMEOUT", defaultDBReadTimeout)
	if err != nil {
		return DatabaseConfig{}, err
	}
	writeTimeout, err := durationEnv("DB_WRITE_TIMEOUT", defaultDBWriteTimeout)
	if err != nil {
		return DatabaseConfig{}, err
	}
	queryTimeout, err := durationEnv("DB_QUERY_TIMEOUT", defaultDBQueryTimeout)
	if err != nil {
		return DatabaseConfig{}, err
	}

	return DatabaseConfig{
		Host:            stringEnv("DB_HOST", defaultDBHost),
		Port:            port,
		Name:            requiredStringEnv("DB_NAME"),
		User:            requiredStringEnv("DB_USER"),
		Password:        requiredSecretEnv("DB_PASSWORD"),
		MaxOpenConns:    maxOpenConns,
		MaxIdleConns:    maxIdleConns,
		ConnMaxLifetime: connMaxLifetime,
		ConnMaxIdleTime: connMaxIdleTime,
		ConnectTimeout:  connectTimeout,
		ReadTimeout:     readTimeout,
		WriteTimeout:    writeTimeout,
		QueryTimeout:    queryTimeout,
	}, nil
}

func stringEnv(name, fallback string) string {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(value)
}

func requiredStringEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// Secrets must not be normalized because spaces may be intentional password
// characters. Validation still rejects an actually empty value.
func requiredSecretEnv(name string) string {
	return os.Getenv(name)
}

func intEnv(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration: %w", name, err)
	}
	return parsed, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}
