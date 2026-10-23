package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/httpapi"
	"internshiplab/order-api/internal/order"
	"internshiplab/order-api/internal/platform/config"
	"internshiplab/order-api/internal/platform/database"
	"internshiplab/order-api/internal/platform/diagnostics"
	"internshiplab/order-api/internal/platform/securitylog"
	"internshiplab/order-api/internal/product"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

func main() {
	slog.SetDefault(securitylog.NewLogger(os.Stdout))
	if err := run(); err != nil {
		slog.Error("order-api stopped", "error_category", "startup_or_runtime_error")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	processContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := database.OpenMySQL(processContext, cfg.Database)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("close MySQL pool failed", "error_category", "database_error")
		}
	}()

	productStore := mysqlstore.NewProductStore(db, cfg.Database.QueryTimeout)
	productService := product.NewService(productStore)
	transactionMetrics := diagnostics.NewTransactionCollector(cfg.TransactionDiagnosticsEnabled)
	orderStore := mysqlstore.NewOrderStoreWithRecorder(db, cfg.Database.QueryTimeout, transactionMetrics)
	orderService := order.NewServiceWithRecorder(orderStore, transactionMetrics)
	authService, err := auth.NewService(mysqlstore.NewAuthStore(db, cfg.Database.QueryTimeout))
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	router := httpapi.NewRouter(product.NewHandler(productService), order.NewHandlerWithRecorder(orderService, transactionMetrics), authService)
	if cfg.DiagnosticsEnabled {
		diagnostics.Register(router, db, transactionMetrics)
	}

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	slog.Info("order-api listening", "addr", cfg.HTTP.Addr)
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-processContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serverErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("finish HTTP server shutdown: %w", err)
		}
		return nil
	}
}
