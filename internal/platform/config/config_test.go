package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

var configEnvironmentVariables = []string{
	"HTTP_ADDR",
	"HTTP_READ_HEADER_TIMEOUT",
	"HTTP_READ_TIMEOUT",
	"HTTP_WRITE_TIMEOUT",
	"HTTP_IDLE_TIMEOUT",
	"HTTP_SHUTDOWN_TIMEOUT",
	"DIAGNOSTICS_ENABLED",
	"TRANSACTION_DIAGNOSTICS_ENABLED",
	"DB_HOST",
	"DB_PORT",
	"DB_NAME",
	"DB_USER",
	"DB_PASSWORD",
	"DB_MAX_OPEN_CONNS",
	"DB_MAX_IDLE_CONNS",
	"DB_CONN_MAX_LIFETIME",
	"DB_CONN_MAX_IDLE_TIME",
	"DB_CONNECT_TIMEOUT",
	"DB_READ_TIMEOUT",
	"DB_WRITE_TIMEOUT",
	"DB_QUERY_TIMEOUT",
}

func TestLoadUsesDefaults(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("DB_NAME", "order_api")
	t.Setenv("DB_USER", "order_api")
	t.Setenv("DB_PASSWORD", "local-secret")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.HTTP.Addr != defaultHTTPAddr {
		t.Errorf("HTTP Addr = %q, want %q", got.HTTP.Addr, defaultHTTPAddr)
	}
	if got.HTTP.ReadHeaderTimeout != defaultHTTPReadHeaderTimeout {
		t.Errorf("HTTP ReadHeaderTimeout = %v, want %v", got.HTTP.ReadHeaderTimeout, defaultHTTPReadHeaderTimeout)
	}
	if got.Database.Host != defaultDBHost || got.Database.Port != defaultDBPort {
		t.Errorf("database address = %s:%d, want %s:%d", got.Database.Host, got.Database.Port, defaultDBHost, defaultDBPort)
	}
	if got.Database.MaxOpenConns != defaultDBMaxOpenConns || got.Database.MaxIdleConns != defaultDBMaxIdleConns {
		t.Errorf(
			"database pool = (%d, %d), want (%d, %d)",
			got.Database.MaxOpenConns,
			got.Database.MaxIdleConns,
			defaultDBMaxOpenConns,
			defaultDBMaxIdleConns,
		)
	}
	if got.Database.QueryTimeout != defaultDBQueryTimeout {
		t.Errorf("database QueryTimeout = %v, want %v", got.Database.QueryTimeout, defaultDBQueryTimeout)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	clearConfigEnvironment(t)
	values := map[string]string{
		"HTTP_ADDR":                       "127.0.0.1:18080",
		"HTTP_READ_HEADER_TIMEOUT":        "1s",
		"HTTP_READ_TIMEOUT":               "2s",
		"HTTP_WRITE_TIMEOUT":              "3s",
		"HTTP_IDLE_TIMEOUT":               "4s",
		"HTTP_SHUTDOWN_TIMEOUT":           "5s",
		"DIAGNOSTICS_ENABLED":             "true",
		"TRANSACTION_DIAGNOSTICS_ENABLED": "true",
		"DB_HOST":                         "mysql.internal",
		"DB_PORT":                         "3307",
		"DB_NAME":                         "orders_test",
		"DB_USER":                         "app",
		"DB_PASSWORD":                     " password with spaces ",
		"DB_MAX_OPEN_CONNS":               "12",
		"DB_MAX_IDLE_CONNS":               "4",
		"DB_CONN_MAX_LIFETIME":            "6m",
		"DB_CONN_MAX_IDLE_TIME":           "90s",
		"DB_CONNECT_TIMEOUT":              "6s",
		"DB_READ_TIMEOUT":                 "7s",
		"DB_WRITE_TIMEOUT":                "8s",
		"DB_QUERY_TIMEOUT":                "9s",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.HTTP.Addr != "127.0.0.1:18080" || got.HTTP.ShutdownTimeout != 5*time.Second {
		t.Errorf("HTTP config = %+v", got.HTTP)
	}
	if !got.DiagnosticsEnabled {
		t.Error("DIAGNOSTICS_ENABLED=true was not loaded")
	}
	if !got.TransactionDiagnosticsEnabled {
		t.Error("TRANSACTION_DIAGNOSTICS_ENABLED=true was not loaded")
	}
	if got.Database.Host != "mysql.internal" || got.Database.Port != 3307 {
		t.Errorf("database address = %s:%d", got.Database.Host, got.Database.Port)
	}
	if got.Database.Password != " password with spaces " {
		t.Errorf("database password was unexpectedly normalized")
	}
	if got.Database.MaxOpenConns != 12 || got.Database.MaxIdleConns != 4 {
		t.Errorf("database pool = (%d, %d)", got.Database.MaxOpenConns, got.Database.MaxIdleConns)
	}
	if got.Database.ConnectTimeout != 6*time.Second || got.Database.QueryTimeout != 9*time.Second {
		t.Errorf(
			"database timeouts = (connect %v, query %v), want (6s, 9s)",
			got.Database.ConnectTimeout,
			got.Database.QueryTimeout,
		)
	}
}

func TestLoadRejectsMissingDatabaseCredentials(t *testing.T) {
	tests := []struct {
		name       string
		missingEnv string
	}{
		{name: "database name", missingEnv: "DB_NAME"},
		{name: "database user", missingEnv: "DB_USER"},
		{name: "database password", missingEnv: "DB_PASSWORD"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("DB_NAME", "order_api")
			t.Setenv("DB_USER", "order_api")
			t.Setenv("DB_PASSWORD", "local-secret")
			t.Setenv(tt.missingEnv, "")

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want credential validation error")
			}
			if !strings.Contains(err.Error(), tt.missingEnv) {
				t.Errorf("Load() error = %q, want variable %q", err, tt.missingEnv)
			}
			if strings.Contains(err.Error(), "local-secret") {
				t.Errorf("Load() error leaked a database password: %q", err)
			}
		})
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name      string
		variable  string
		value     string
		wantError string
	}{
		{name: "invalid integer", variable: "DB_PORT", value: "mysql", wantError: "DB_PORT must be an integer"},
		{name: "port out of range", variable: "DB_PORT", value: "70000", wantError: "DB_PORT must be between"},
		{name: "invalid duration", variable: "DB_QUERY_TIMEOUT", value: "soon", wantError: "DB_QUERY_TIMEOUT must be a Go duration"},
		{name: "invalid diagnostics flag", variable: "DIAGNOSTICS_ENABLED", value: "sometimes", wantError: "DIAGNOSTICS_ENABLED must be a boolean"},
		{name: "invalid transaction diagnostics flag", variable: "TRANSACTION_DIAGNOSTICS_ENABLED", value: "sometimes", wantError: "TRANSACTION_DIAGNOSTICS_ENABLED must be a boolean"},
		{name: "zero operation timeout", variable: "DB_QUERY_TIMEOUT", value: "0s", wantError: "DB_QUERY_TIMEOUT must be greater than zero"},
		{name: "idle exceeds open", variable: "DB_MAX_IDLE_CONNS", value: "26", wantError: "must not exceed"},
		{name: "empty HTTP address", variable: "HTTP_ADDR", value: " ", wantError: "HTTP_ADDR is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("DB_NAME", "order_api")
			t.Setenv("DB_USER", "order_api")
			t.Setenv("DB_PASSWORD", "local-secret")
			t.Setenv(tt.variable, tt.value)

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantError)
			}
		})
	}
}

func TestTransactionDiagnosticsRequireLocalDiagnostics(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("DB_NAME", "order_api")
	t.Setenv("DB_USER", "app")
	t.Setenv("DB_PASSWORD", "test-placeholder")
	t.Setenv("TRANSACTION_DIAGNOSTICS_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "requires DIAGNOSTICS_ENABLED") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestDatabaseConfigAllowsDisabledConnectionAging(t *testing.T) {
	config := validDatabaseConfig()
	config.ConnMaxLifetime = 0
	config.ConnMaxIdleTime = 0

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want zero connection aging values to be allowed", err)
	}
}

func TestHTTPRequiresLoopback(t *testing.T) {
	for _, address := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.168.1.3:8080", "localhost:8080", "127.0.0.1:0", "127.0.0.1:70000"} {
		clearConfigEnvironment(t)
		t.Setenv("DB_NAME", "order_api")
		t.Setenv("DB_USER", "app")
		t.Setenv("DB_PASSWORD", "test-placeholder")
		t.Setenv("HTTP_ADDR", address)
		if _, err := Load(); err == nil {
			t.Errorf("unsafe address %s accepted", address)
		}
	}
	clearConfigEnvironment(t)
	t.Setenv("DB_NAME", "order_api")
	t.Setenv("DB_USER", "app")
	t.Setenv("DB_PASSWORD", "test-placeholder")
	t.Setenv("HTTP_ADDR", "[::1]:18080")
	if _, err := Load(); err != nil {
		t.Fatalf("IPv6 loopback rejected: %v", err)
	}
}

func validDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
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

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range configEnvironmentVariables {
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
}
