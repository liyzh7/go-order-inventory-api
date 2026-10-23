package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"internshiplab/order-api/internal/order"
)

// A controlled driver exercises a real database/sql Tx and the Store's Commit
// reconciliation branch, without claiming to simulate an actual network fault.
type lifecycleFault struct {
	status        string
	commitStatus  string
	commitErr     error
	queryErr      error
	queryFailures int
	stateRows     int64
	updates       int
	commits       int
	rollbacks     int
	userID        uint64
}

type lifecycleDriver struct{ fault *lifecycleFault }

func (d *lifecycleDriver) Open(string) (driver.Conn, error) {
	return &lifecycleConn{fault: d.fault}, nil
}

type lifecycleConn struct{ fault *lifecycleFault }

func (c *lifecycleConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *lifecycleConn) Close() error                        { return nil }
func (c *lifecycleConn) Begin() (driver.Tx, error)           { return &lifecycleTx{fault: c.fault}, nil }
func (c *lifecycleConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c *lifecycleConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.fault.userID != 0 && (!strings.Contains(query, "user_id = ?") || len(args) != 2 || args[1].Value != int64(c.fault.userID)) {
		return nil, errors.New("query escaped user scope")
	}
	if c.fault.queryFailures > 0 {
		c.fault.queryFailures--
		return nil, &mysqldriver.MySQLError{Number: 1213, Message: "controlled deadlock before state transition"}
	}
	if c.fault.queryErr != nil {
		return nil, c.fault.queryErr
	}
	if strings.Contains(query, "FOR UPDATE") {
		return &queryRows{columns: []string{"status"}, values: [][]driver.Value{{c.fault.status}}}, nil
	}
	return &queryRows{columns: []string{"id", "order_no", "status", "total", "created_at", "product_id", "sku", "name", "price", "quantity"},
		values: [][]driver.Value{{int64(1), "ORD-TEST", c.fault.status, int64(2000), time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), int64(1), "TEST-SKU", "Snapshot", int64(1000), int64(2)}}}, nil
}

func TestCancellationRetriesWholeTransaction(t *testing.T) {
	fault := &lifecycleFault{status: order.StatusCreated, stateRows: 1, queryFailures: 2}
	store := NewOrderStore(openLifecycleDB(t, fault), time.Second)
	store.retryBaseDelay = time.Microsecond
	result, err := store.Cancel(context.Background(), 1, 1)
	if err != nil || result.Order.Status != order.StatusCancelled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fault.rollbacks != 2 || fault.commits != 1 || fault.updates != 1 {
		t.Fatalf("rollbacks=%d commits=%d updates=%d", fault.rollbacks, fault.commits, fault.updates)
	}
	fault = &lifecycleFault{status: order.StatusCreated, stateRows: 1, queryFailures: 4}
	store = NewOrderStore(openLifecycleDB(t, fault), time.Second)
	store.retryBaseDelay = time.Microsecond
	if _, err := store.Cancel(context.Background(), 1, 1); !errors.Is(err, order.ErrDatabaseUnavailable) {
		t.Fatalf("exhausted error=%v", err)
	}
	if fault.rollbacks != 3 || fault.updates != 0 {
		t.Fatalf("retry limit rollbacks=%d updates=%d", fault.rollbacks, fault.updates)
	}
}
func (c *lifecycleConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "UPDATE orders") {
		return driver.RowsAffected(c.fault.stateRows), nil
	}
	c.fault.updates++
	return driver.RowsAffected(1), nil
}

type lifecycleTx struct{ fault *lifecycleFault }

func (tx *lifecycleTx) Commit() error {
	tx.fault.commits++
	if tx.fault.commitStatus != "" {
		tx.fault.status = tx.fault.commitStatus
	}
	return tx.fault.commitErr
}
func (tx *lifecycleTx) Rollback() error { tx.fault.rollbacks++; return nil }
func openLifecycleDB(t *testing.T, fault *lifecycleFault) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("order_lifecycle_%d", driverSequence.Add(1))
	sql.Register(name, &lifecycleDriver{fault: fault})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCancellationCommitReconciliation(t *testing.T) {
	for _, test := range []struct {
		name, status string
		wantErr      error
	}{
		{"committed despite driver error", order.StatusCancelled, nil},
		{"not confirmed", order.StatusCreated, order.ErrCommitUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			fault := &lifecycleFault{status: order.StatusCreated, stateRows: 1, commitStatus: test.status, commitErr: errors.New("controlled lost commit response"), userID: 7}
			result, err := NewOrderStore(openLifecycleDB(t, fault), time.Second).Cancel(context.Background(), 7, 1)
			if test.wantErr == nil {
				if err != nil || result.Order.Status != order.StatusCancelled || !result.Replayed {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("err=%v want=%v", err, test.wantErr)
			}
			if fault.updates != 1 || fault.commits != 1 {
				t.Fatalf("repeated side effects: updates=%d commits=%d", fault.updates, fault.commits)
			}
		})
	}
	if _, err := resolveCancellationCommit(order.Order{}, errors.New("verification failed"), errors.New("commit failed")); !errors.Is(err, order.ErrCommitUncertain) {
		t.Fatal(err)
	}
}

func TestCancellationStateGate(t *testing.T) {
	for _, test := range []struct {
		name, status string
		stateRows    int64
		wantErr      error
	}{
		{"already cancelled", order.StatusCancelled, 1, nil},
		{"unsupported state", "PAID", 1, order.ErrInvalidState},
		{"zero affected state rows", order.StatusCreated, 0, order.ErrInvalidState},
	} {
		t.Run(test.name, func(t *testing.T) {
			fault := &lifecycleFault{status: test.status, stateRows: test.stateRows}
			_, err := NewOrderStore(openLifecycleDB(t, fault), time.Second).Cancel(context.Background(), 1, 1)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("err=%v", err)
			}
			if test.wantErr == nil && err != nil {
				t.Fatal(err)
			}
			if fault.updates != 0 || fault.commits != 0 {
				t.Fatal("inventory changed without state transition")
			}
			if fault.rollbacks != 1 {
				t.Fatal("transaction not ended")
			}
		})
	}
}

func TestLifecycleQueryFailureAndCancelledContext(t *testing.T) {
	failure := errors.New("controlled query failure")
	fault := &lifecycleFault{queryErr: failure}
	store := NewOrderStore(openLifecycleDB(t, fault), time.Second)
	if _, err := store.GetByID(context.Background(), 1, 1); !errors.Is(err, failure) {
		t.Fatalf("Get error=%v", err)
	}
	if _, err := store.Cancel(context.Background(), 1, 1); !errors.Is(err, failure) {
		t.Fatalf("Cancel error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Cancel(ctx, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancel context error=%v", err)
	}
}
