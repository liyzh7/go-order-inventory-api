package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"internshiplab/order-api/internal/order"
)

func TestCommitLookupIsUserScoped(t *testing.T) {
	db := openQueryDB(t, func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "WHERE user_id = ? AND idempotency_key = ?") {
			t.Fatal("commit lookup missing owner predicate")
		}
		if len(args) != 2 || args[0].Value != int64(7) || string(args[1].Value.([]byte)) != "shared-key" {
			t.Fatal("commit lookup changed user or key")
		}
		return &queryRows{columns: []string{"id", "order_no", "request_hash", "status", "amount", "created_at"}}, nil
	})
	_, found, err := NewOrderStore(db, time.Second).lookupCommitted(context.Background(), 7, []byte("shared-key"))
	if err != nil || found {
		t.Fatalf("lookup found=%v err=%v", found, err)
	}
}

var _ order.Store = (*OrderStore)(nil)

func TestCheckedSubtotal(t *testing.T) {
	got, err := checkedSubtotal(order.MaxOrderAmount/1000, 1000)
	if err != nil || got != order.MaxOrderAmount {
		t.Fatalf("boundary subtotal = %d, %v; want %d, nil", got, err, order.MaxOrderAmount)
	}
	if _, err := checkedSubtotal(order.MaxOrderAmount/1000+1, 1000); !errors.Is(err, order.ErrAmountOverflow) {
		t.Fatalf("overflow error = %v, want ErrAmountOverflow", err)
	}
}

func TestRunWithRetryRetriesDeadlockAndLockTimeout(t *testing.T) {
	for _, number := range []uint16{1213, 1205} {
		calls := 0
		result, err := runWithRetry(context.Background(), 3, time.Microsecond, func() (order.CreateResult, error) {
			calls++
			if calls < 3 {
				return order.CreateResult{}, &mysqldriver.MySQLError{Number: number, Message: "controlled transient error"}
			}
			return order.CreateResult{Order: order.Order{ID: 99}}, nil
		})
		if err != nil || result.Order.ID != 99 || calls != 3 {
			t.Errorf("error %d: result=%#v err=%v calls=%d", number, result, err, calls)
		}
	}
}

func TestRunWithRetryStopsAtLimit(t *testing.T) {
	calls := 0
	_, err := runWithRetry(context.Background(), 3, time.Microsecond, func() (order.CreateResult, error) {
		calls++
		return order.CreateResult{}, &mysqldriver.MySQLError{Number: 1213, Message: "controlled deadlock"}
	})
	if !isRetryableTransactionError(err) || calls != 3 {
		t.Fatalf("err=%v calls=%d, want retryable error after 3 attempts", err, calls)
	}
}

func TestRunWithRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := runWithRetry(ctx, 3, time.Second, func() (order.CreateResult, error) {
		calls++
		cancel()
		return order.CreateResult{}, &mysqldriver.MySQLError{Number: 1213, Message: "controlled deadlock"}
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want cancellation after first attempt", err, calls)
	}
}

func TestMySQLErrorClassificationUsesCodes(t *testing.T) {
	if !isDuplicateKey(&mysqldriver.MySQLError{Number: 1062}) {
		t.Error("1062 was not classified as duplicate key")
	}
	if isDuplicateKey(&mysqldriver.MySQLError{Number: 1213, Message: "duplicate words do not matter"}) {
		t.Error("non-1062 error was classified from message text")
	}
	for _, err := range []error{
		&mysqldriver.MySQLError{Number: 1053},
		&mysqldriver.MySQLError{Number: 2006},
		driver.ErrBadConn,
	} {
		if classified := classifyInfrastructureError("test", err); !errors.Is(classified, order.ErrDatabaseUnavailable) {
			t.Errorf("error %v classified as %v", err, classified)
		}
	}
}

func TestRollbackFailurePreservesTypedConnectionEvidence(t *testing.T) {
	opaque := errors.New("controlled private driver failure")
	joined := preserveRollbackFailure(opaque, mysqldriver.ErrInvalidConn)
	if !errors.Is(joined, opaque) || !errors.Is(joined, mysqldriver.ErrInvalidConn) {
		t.Fatal("original or rollback failure lost")
	}
	if !errors.Is(classifyInfrastructureError("rollback", joined), order.ErrDatabaseUnavailable) {
		t.Fatal("broken transaction cleanup was not classified as unavailable")
	}
	if preserveRollbackFailure(opaque, sql.ErrTxDone) != opaque || preserveRollbackFailure(opaque, nil) != opaque {
		t.Fatal("normal cleanup altered the transaction outcome")
	}
}

func TestLostDatabaseConnectionClassification(t *testing.T) {
	for _, err := range []error{
		mysqldriver.ErrInvalidConn, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed,
		&net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE},
	} {
		if got := classifyInfrastructureError("controlled connection interruption", fmt.Errorf("wrapped: %w", err)); !errors.Is(got, order.ErrDatabaseUnavailable) {
			t.Fatalf("transport error %T not classified as unavailable", err)
		}
		if isRetryableTransactionError(err) {
			t.Fatal("transport error must not enable automatic transaction re-execution")
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := classifyInfrastructureError("cancelled", err); !errors.Is(got, err) || errors.Is(got, order.ErrDatabaseUnavailable) {
			t.Fatal("context cancellation classification changed")
		}
	}
	if errors.Is(classifyInfrastructureError("trigger failure", &mysqldriver.MySQLError{Number: 1644}), order.ErrDatabaseUnavailable) {
		t.Fatal("ordinary SQL error was misclassified as a transport failure")
	}
}
