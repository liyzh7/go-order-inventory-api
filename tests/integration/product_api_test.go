//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pressly/goose/v3"

	"internshiplab/order-api/internal/product"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

func TestProductAPIWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	activeID := insertProductWithInventory(t, ctx, db, "BOOK-001", "Go in Practice", 5999, "ACTIVE", 8)
	inactiveID := insertProductWithInventory(t, ctx, db, "BOOK-002", "Hidden product", 2999, "INACTIVE", 4)

	store := mysqlstore.NewProductStore(db, 2*time.Second)
	service := product.NewService(store)
	productHandler := product.NewHandler(service)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	productHandler.RegisterRoutes(router)

	t.Run("list reads active product and inventory in MySQL", func(t *testing.T) {
		response := performRequest(router, http.MethodGet, "/products?limit=10")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}

		var payload struct {
			Data struct {
				Items []product.Product `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		if len(payload.Data.Items) != 1 {
			t.Fatalf("items = %#v, want exactly one active product", payload.Data.Items)
		}
		if got := payload.Data.Items[0]; got.ID != activeID || got.SKU != "BOOK-001" || got.AvailableQuantity != 8 {
			t.Errorf("item = %#v, want active product with inventory", got)
		}
	})

	t.Run("detail distinguishes active and inactive products", func(t *testing.T) {
		found := performRequest(router, http.MethodGet, "/products/"+strconv.FormatUint(activeID, 10))
		if found.Code != http.StatusOK {
			t.Fatalf("active status = %d, want %d; body=%s", found.Code, http.StatusOK, found.Body.String())
		}

		missing := performRequest(router, http.MethodGet, "/products/999999")
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing status = %d, want %d; body=%s", missing.Code, http.StatusNotFound, missing.Body.String())
		}

		inactive := performRequest(router, http.MethodGet, "/products/"+strconv.FormatUint(inactiveID, 10))
		if inactive.Code != http.StatusNotFound {
			t.Fatalf("inactive status = %d, want %d; body=%s", inactive.Code, http.StatusNotFound, inactive.Body.String())
		}
	})
}

func openMigratedTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	adminDB, cfg := openAdminDatabase(t)
	databaseName := uniqueDatabaseName(t)
	createIsolatedDatabase(t, adminDB, databaseName)
	t.Cleanup(func() {
		dropIsolatedDatabase(t, adminDB, databaseName)
		_ = adminDB.Close()
	})

	cfg.DBName = databaseName
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open product API test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatalf("configure goose dialect: %v", err)
	}
	if err := goose.Up(db, migrationDirectory(t)); err != nil {
		t.Fatalf("migrate product API test database: %v", err)
	}
	return db
}

func insertProductWithInventory(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	sku string,
	name string,
	priceCents uint64,
	status string,
	availableQuantity uint32,
) uint64 {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO products (sku, name, price_cents, status)
		VALUES (?, ?, ?, ?)`, sku, name, priceCents, status)
	if err != nil {
		t.Fatalf("insert product %s: %v", sku, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read product %s ID: %v", sku, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO inventories (product_id, available_qty)
		VALUES (?, ?)`, id, availableQuantity); err != nil {
		t.Fatalf("insert inventory for %s: %v", sku, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit product %s: %v", sku, err)
	}
	return uint64(id)
}

func performRequest(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, nil)
	handler.ServeHTTP(recorder, request)
	return recorder
}
