//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/httpapi"
	"internshiplab/order-api/internal/order"
	"internshiplab/order-api/internal/product"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

// Only the Commit return value is injected: all SQL, unique indexes, stock
// changes and verification reads use real isolated MySQL. This is not a claim
// that a real TCP packet was lost. No injection hook is added to production.
type commitFaultConnector struct {
	driver.Connector
	mode  string
	fired atomic.Bool
}

func (c *commitFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &commitFaultConn{Conn: conn, fault: c}, nil
}

type commitFaultConn struct {
	driver.Conn
	fault *commitFaultConnector
}

func (c *commitFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, fault: c.fault}, nil
}
func (c *commitFaultConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, a)
}
func (c *commitFaultConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if c.fault.mode == "verification_unavailable" && c.fault.fired.Load() {
		return nil, driver.ErrBadConn
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, a)
}

type commitFaultTx struct {
	driver.Tx
	fault *commitFaultConnector
}

func (tx *commitFaultTx) Commit() error {
	if tx.fault.fired.Swap(true) {
		return tx.Tx.Commit()
	}
	var err error
	if tx.fault.mode == "not_committed" {
		err = tx.Tx.Rollback()
	} else {
		err = tx.Tx.Commit()
	}
	if err != nil {
		return err
	}
	return errors.New("test-only commit acknowledgement unavailable")
}

func TestCreateCommitRecoveryWithMySQL(t *testing.T) {
	for _, mode := range []string{"committed", "not_committed", "verification_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			db := openMigratedTestDatabase(t)
			normal := newAuthenticatedRouter(t, db)
			a := registerClient(t, normal)
			b := registerClient(t, normal)
			productID := insertProductWithInventory(t, context.Background(), db, "RECOVERY-SKU", "Recovery snapshot", 1250, "ACTIVE", 20)
			admin, cfg := openAdminDatabase(t)
			admin.Close()
			if err := db.QueryRow("SELECT DATABASE()").Scan(&cfg.DBName); err != nil {
				t.Fatal(err)
			}
			connector, err := mysql.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			fault := &commitFaultConnector{Connector: connector, mode: mode}
			faultDB := sql.OpenDB(fault)
			defer faultDB.Close()
			authService, err := auth.NewService(mysqlstore.NewAuthStore(db, time.Second))
			if err != nil {
				t.Fatal(err)
			}
			router := httpapi.NewRouter(product.NewHandler(product.NewService(mysqlstore.NewProductStore(db, time.Second))), order.NewHandler(order.NewService(mysqlstore.NewOrderStore(faultDB, 2*time.Second))), authService)
			body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, productID)
			first := postOrder(authorizedTransport{router, a.token}, "uncertain-key", body)
			if mode == "committed" {
				if first.Code != 200 || first.Header().Get("Idempotency-Replayed") != "true" {
					t.Fatalf("resolved commit status=%d", first.Code)
				}
			} else if first.Code < 500 {
				t.Fatalf("unconfirmed outcome reported success: %d", first.Code)
			}
			wantCount := 1
			wantStock := uint32(18)
			if mode == "not_committed" {
				wantCount = 0
				wantStock = 20
			}
			assertOrderState(t, db, "uncertain-key", wantCount, wantCount, uint64(wantCount)*2500)
			assertInventory(t, db, productID, wantStock)
			retry := postOrder(a.handler(), "uncertain-key", body)
			wantStatus := http.StatusOK
			if mode == "not_committed" {
				wantStatus = http.StatusCreated
			}
			if retry.Code != wantStatus {
				t.Fatalf("safe retry status=%d, want %d", retry.Code, wantStatus)
			}
			assertOrderState(t, db, "uncertain-key", 1, 1, 2500)
			assertInventory(t, db, productID, 18)
			original := decodeOrderResponse(t, retry)
			cross := postOrder(b.handler(), "uncertain-key", body)
			if cross.Code != 201 || decodeOrderResponse(t, cross).ID == original.ID {
				t.Fatal("commit reconciliation crossed user scope")
			}
			denied := httptestOrderGet(b.handler(), original.ID)
			if denied != 404 {
				t.Fatalf("cross-user read status=%d", denied)
			}
			assertOrderState(t, db, "uncertain-key", 2, 2, 5000)
			assertInventory(t, db, productID, 16)
			if faultDB.Stats().InUse != 0 {
				t.Fatal("injected commit leaked a connection")
			}
			t.Logf("injected=%s initial_status=%d retry_status=%d final_orders=2 final_items=2 stock=16 (one order per user)", mode, first.Code, retry.Code)
		})
	}
}
func httptestOrderGet(h http.Handler, id uint64) int {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/orders/%d", id), strings.NewReader(""))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response.Code
}
