package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"internshiplab/order-api/internal/product"
)

var _ product.Store = (*ProductStore)(nil)

func TestProductStoreListActive(t *testing.T) {
	db := openQueryDB(t, func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "INNER JOIN inventories") || !strings.Contains(query, "ORDER BY p.id ASC") {
			t.Errorf("query does not contain inventory JOIN and stable ordering: %s", query)
		}
		assertNamedValues(t, args, product.StatusActive, int64(7), int64(2))
		return &queryRows{
			columns: []string{"id", "sku", "name", "price_cents", "available_qty"},
			values: [][]driver.Value{
				{int64(8), "BOOK-001", "Book", int64(2999), int64(5)},
				{int64(9), "MOUSE-001", "Mouse", int64(8900), int64(3)},
			},
		}, nil
	})

	items, err := NewProductStore(db, time.Second).ListActive(context.Background(), 7, 2)
	if err != nil {
		t.Fatalf("ListActive() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("product count = %d, want 2", len(items))
	}
	if items[0].ID != 8 || items[0].SKU != "BOOK-001" || items[0].AvailableQuantity != 5 {
		t.Errorf("first product = %+v, want BOOK-001 with inventory", items[0])
	}
}

func TestProductStoreListActiveQueryFailure(t *testing.T) {
	wanted := errors.New("database unavailable")
	db := openQueryDB(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return nil, wanted
	})

	_, err := NewProductStore(db, time.Second).ListActive(context.Background(), 0, 20)
	if !errors.Is(err, wanted) {
		t.Fatalf("error = %v, want error matching %v", err, wanted)
	}
}

func TestProductStoreGetActiveByID(t *testing.T) {
	tests := []struct {
		name     string
		rows     driver.Rows
		queryErr error
		want     product.Product
		wantErr  error
	}{
		{
			name: "returns active product with inventory",
			rows: &queryRows{
				columns: []string{"id", "sku", "name", "price_cents", "available_qty"},
				values:  [][]driver.Value{{int64(5), "MOUSE-001", "Mouse", int64(8900), int64(4)}},
			},
			want: product.Product{ID: 5, SKU: "MOUSE-001", Name: "Mouse", PriceCents: 8900, AvailableQuantity: 4},
		},
		{
			name:    "classifies missing row",
			rows:    &queryRows{columns: []string{"id", "sku", "name", "price_cents", "available_qty"}},
			wantErr: product.ErrNotFound,
		},
		{
			name:     "wraps query failure",
			queryErr: errTestDatabaseUnavailable,
			wantErr:  errTestDatabaseUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openQueryDB(t, func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				if !strings.Contains(query, "INNER JOIN inventories") || !strings.Contains(query, "p.status = ?") {
					t.Errorf("query does not constrain active joined product: %s", query)
				}
				assertNamedValues(t, args, int64(5), product.StatusActive)
				return tt.rows, tt.queryErr
			})

			got, err := NewProductStore(db, time.Second).GetActiveByID(context.Background(), 5)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want error matching %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetActiveByID() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("product = %+v, want %+v", got, tt.want)
			}
		})
	}
}

var (
	driverSequence             atomic.Uint64
	errTestDatabaseUnavailable = errors.New("database unavailable")
)

type queryFunc func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error)

type queryDriver struct {
	query queryFunc
}

func (d *queryDriver) Open(string) (driver.Conn, error) {
	return &queryConn{query: d.query}, nil
}

type queryConn struct {
	query queryFunc
}

func (c *queryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported by test driver")
}

func (c *queryConn) Close() error { return nil }

func (c *queryConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported by test driver")
}

func (c *queryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, query, args)
}

var _ driver.QueryerContext = (*queryConn)(nil)

type queryRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *queryRows) Columns() []string { return r.columns }
func (r *queryRows) Close() error      { return nil }

func (r *queryRows) Next(destination []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(destination, r.values[r.index])
	r.index++
	return nil
}

func openQueryDB(t *testing.T, query queryFunc) *sql.DB {
	t.Helper()
	driverName := fmt.Sprintf("product_query_driver_%d", driverSequence.Add(1))
	sql.Register(driverName, &queryDriver{query: query})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	return db
}

func assertNamedValues(t *testing.T, got []driver.NamedValue, want ...any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argument count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Value != want[i] {
			t.Errorf("argument[%d] = %#v, want %#v", i, got[i].Value, want[i])
		}
	}
}
