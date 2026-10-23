package mysql

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"internshiplab/order-api/internal/order"
)

const (
	defaultTransactionAttempts = 3
	defaultRetryBaseDelay      = 25 * time.Millisecond
)

var (
	errDuplicateKey = errors.New("duplicate database key")
	errCommitFailed = errors.New("transaction commit failed")
)

type OrderStore struct {
	db             *sql.DB
	operationLimit time.Duration
	maxAttempts    int
	retryBaseDelay time.Duration
	newOrderNumber func() (string, error)
	recorder       order.StageRecorder
}

func NewOrderStore(db *sql.DB, operationLimit time.Duration) *OrderStore {
	return NewOrderStoreWithRecorder(db, operationLimit, nil)
}

func NewOrderStoreWithRecorder(db *sql.DB, operationLimit time.Duration, recorder order.StageRecorder) *OrderStore {
	return &OrderStore{
		db:             db,
		operationLimit: operationLimit,
		maxAttempts:    defaultTransactionAttempts,
		retryBaseDelay: defaultRetryBaseDelay,
		newOrderNumber: generateOrderNumber,
		recorder:       recorder,
	}
}

func (s *OrderStore) Create(
	ctx context.Context,
	userID uint64,
	idempotencyKey []byte,
	requestHash [sha256.Size]byte,
	items []order.NormalizedItem,
) (order.CreateResult, error) {
	operationContext, cancel := context.WithTimeout(ctx, s.operationLimit)
	defer cancel()

	result, err := runWithRetry(operationContext, s.maxAttempts, s.retryBaseDelay, func() (order.CreateResult, error) {
		return s.createOnce(operationContext, userID, idempotencyKey, requestHash, items)
	}, func(duration time.Duration) {
		s.observe(order.StageTransactionRetryDelay, order.StageOutcomeRetryableFailure, duration)
	})
	if err == nil {
		return result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return order.CreateResult{}, err
	}

	// A duplicate insert or failed Commit is resolved only after createOnce has
	// ended its transaction. The independent read can therefore observe the
	// unique-key winner without reading through an aborted transaction.
	if errors.Is(err, errDuplicateKey) || errors.Is(err, errCommitFailed) {
		lookupContext, lookupCancel := context.WithTimeout(ctx, s.operationLimit)
		defer lookupCancel()
		existing, found, lookupErr := s.lookupCommitted(lookupContext, userID, idempotencyKey)
		if lookupErr != nil {
			return order.CreateResult{}, classifyInfrastructureError("resolve transaction outcome", lookupErr)
		}
		if found {
			if !bytes.Equal(existing.requestHash, requestHash[:]) {
				return order.CreateResult{}, order.ErrIdempotencyConflict
			}
			return order.CreateResult{Order: existing.order, Replayed: true}, nil
		}
		if errors.Is(err, errCommitFailed) {
			return order.CreateResult{}, fmt.Errorf("%w: %v", order.ErrCommitUncertain, err)
		}
		return order.CreateResult{}, fmt.Errorf("duplicate key was not the idempotency key: %w", err)
	}

	return order.CreateResult{}, classifyInfrastructureError("create order transaction", err)
}

func (s *OrderStore) createOnce(
	ctx context.Context,
	userID uint64,
	idempotencyKey []byte,
	requestHash [sha256.Size]byte,
	items []order.NormalizedItem,
) (result order.CreateResult, returnErr error) {
	attemptStarted := time.Now()
	defer func() {
		s.observe(order.StageTransactionAttempt, createStageOutcome(returnErr, result), time.Since(attemptStarted))
	}()

	connectionStarted := time.Now()
	connection, err := s.db.Conn(ctx)
	s.observe(order.StageAcquireConnection, errorStageOutcome(err), time.Since(connectionStarted))
	if err != nil {
		return order.CreateResult{}, fmt.Errorf("acquire order database connection: %w", err)
	}
	defer connection.Close()

	beginStarted := time.Now()
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	s.observe(order.StageBeginTransaction, errorStageOutcome(err), time.Since(beginStarted))
	if err != nil {
		return order.CreateResult{}, fmt.Errorf("begin order transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			returnErr = preserveRollbackFailure(returnErr, tx.Rollback())
		}
	}()

	idempotencyStarted := time.Now()
	if existing, found, err := lookupOrder(ctx, tx, userID, idempotencyKey); err != nil {
		s.observe(order.StageIdempotencyLookup, errorStageOutcome(err), time.Since(idempotencyStarted))
		return order.CreateResult{}, fmt.Errorf("look up idempotency key: %w", err)
	} else if found {
		if !bytes.Equal(existing.requestHash, requestHash[:]) {
			s.observe(order.StageIdempotencyLookup, order.StageOutcomeBusinessRejected, time.Since(idempotencyStarted))
			return order.CreateResult{}, order.ErrIdempotencyConflict
		}
		s.observe(order.StageIdempotencyLookup, order.StageOutcomeReplay, time.Since(idempotencyStarted))
		return order.CreateResult{Order: existing.order, Replayed: true}, nil
	}
	s.observe(order.StageIdempotencyLookup, order.StageOutcomeSuccess, time.Since(idempotencyStarted))

	products := make([]lockedProduct, 0, len(items))
	var total uint64
	for _, item := range items {
		productStarted := time.Now()
		product, err := lockProduct(ctx, tx, item.ProductID)
		if errors.Is(err, sql.ErrNoRows) {
			s.observe(order.StageProductRead, order.StageOutcomeBusinessRejected, time.Since(productStarted))
			return order.CreateResult{}, fmt.Errorf("product %d: %w", item.ProductID, order.ErrProductNotFound)
		}
		if err != nil {
			s.observe(order.StageProductRead, errorStageOutcome(err), time.Since(productStarted))
			return order.CreateResult{}, fmt.Errorf("lock product %d: %w", item.ProductID, err)
		}
		if product.status != "ACTIVE" {
			s.observe(order.StageProductRead, order.StageOutcomeBusinessRejected, time.Since(productStarted))
			return order.CreateResult{}, fmt.Errorf("product %d: %w", item.ProductID, order.ErrProductInactive)
		}
		s.observe(order.StageProductRead, order.StageOutcomeSuccess, time.Since(productStarted))

		subtotal, err := checkedSubtotal(product.priceCents, item.Quantity)
		if err != nil {
			return order.CreateResult{}, fmt.Errorf("product %d: %w", item.ProductID, err)
		}
		if total > order.MaxOrderAmount-subtotal {
			return order.CreateResult{}, order.ErrAmountOverflow
		}
		total += subtotal
		product.quantity = item.Quantity
		product.subtotal = subtotal
		products = append(products, product)
	}

	orderNumber, err := s.newOrderNumber()
	if err != nil {
		return order.CreateResult{}, fmt.Errorf("generate order number: %w", err)
	}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	headerStarted := time.Now()
	execResult, err := tx.ExecContext(ctx, `
		INSERT INTO orders
			(user_id,order_no, idempotency_key, request_hash, status, total_amount_cents, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, orderNumber, idempotencyKey, requestHash[:], order.StatusCreated, total, createdAt, createdAt)
	if isDuplicateKey(err) {
		s.observe(order.StageOrderHeaderInsert, order.StageOutcomeBusinessRejected, time.Since(headerStarted))
		return order.CreateResult{}, fmt.Errorf("insert order header: %w: %w", errDuplicateKey, err)
	}
	if err != nil {
		s.observe(order.StageOrderHeaderInsert, errorStageOutcome(err), time.Since(headerStarted))
		return order.CreateResult{}, fmt.Errorf("insert order header: %w", err)
	}
	insertedID, err := execResult.LastInsertId()
	if err != nil {
		s.observe(order.StageOrderHeaderInsert, order.StageOutcomeFailure, time.Since(headerStarted))
		return order.CreateResult{}, fmt.Errorf("read inserted order ID: %w", err)
	}
	s.observe(order.StageOrderHeaderInsert, order.StageOutcomeSuccess, time.Since(headerStarted))
	orderID := uint64(insertedID)

	// Persist immutable item snapshots before taking inventory row locks. These
	// inserts remain uncommitted and roll back with the order if any later stock
	// check fails. Moving them earlier shortens the interval between the first
	// inventory UPDATE and Commit without weakening atomicity or lock ordering.
	responseItems := make([]order.Item, 0, len(products))
	for _, product := range products {
		itemStarted := time.Now()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO order_items
				(order_id, product_id, sku_snapshot, name_snapshot, unit_price_cents, quantity)
			VALUES (?, ?, ?, ?, ?, ?)`,
			orderID, product.id, product.sku, product.name, product.priceCents, product.quantity)
		if err != nil {
			s.observe(order.StageOrderItemInsert, errorStageOutcome(err), time.Since(itemStarted))
			return order.CreateResult{}, fmt.Errorf("insert order item for product %d: %w", product.id, err)
		}
		s.observe(order.StageOrderItemInsert, order.StageOutcomeSuccess, time.Since(itemStarted))

		responseItems = append(responseItems, order.Item{
			ProductID:      product.id,
			SKU:            product.sku,
			Name:           product.name,
			UnitPriceCents: product.priceCents,
			Quantity:       product.quantity,
			SubtotalCents:  product.subtotal,
		})
	}

	// Products were normalized by ID in the service and retained in that order,
	// so every transaction acquires inventory row locks consistently.
	for _, product := range products {
		inventoryStarted := time.Now()
		updateResult, err := tx.ExecContext(ctx, `
			UPDATE inventories
			SET available_qty = available_qty - ?
			WHERE product_id = ? AND available_qty >= ?`, product.quantity, product.id, product.quantity)
		if err != nil {
			s.observe(order.StageInventoryUpdate, errorStageOutcome(err), time.Since(inventoryStarted))
			return order.CreateResult{}, fmt.Errorf("deduct inventory for product %d: %w", product.id, err)
		}
		affected, err := updateResult.RowsAffected()
		if err != nil {
			s.observe(order.StageInventoryUpdate, order.StageOutcomeFailure, time.Since(inventoryStarted))
			return order.CreateResult{}, fmt.Errorf("read inventory update result for product %d: %w", product.id, err)
		}
		if affected != 1 {
			exists, err := inventoryExists(ctx, tx, product.id)
			if err != nil {
				s.observe(order.StageInventoryUpdate, errorStageOutcome(err), time.Since(inventoryStarted))
				return order.CreateResult{}, fmt.Errorf("inspect inventory for product %d: %w", product.id, err)
			}
			if !exists {
				s.observe(order.StageInventoryUpdate, order.StageOutcomeFailure, time.Since(inventoryStarted))
				return order.CreateResult{}, fmt.Errorf("product %d has no inventory row", product.id)
			}
			s.observe(order.StageInventoryUpdate, order.StageOutcomeBusinessRejected, time.Since(inventoryStarted))
			return order.CreateResult{}, fmt.Errorf("product %d: %w", product.id, order.ErrInsufficientStock)
		}
		s.observe(order.StageInventoryUpdate, order.StageOutcomeSuccess, time.Since(inventoryStarted))
	}

	commitStarted := time.Now()
	if err := tx.Commit(); err != nil {
		s.observe(order.StageCommit, errorStageOutcome(err), time.Since(commitStarted))
		return order.CreateResult{}, fmt.Errorf("%w: %w", errCommitFailed, err)
	}
	s.observe(order.StageCommit, order.StageOutcomeSuccess, time.Since(commitStarted))
	committed = true
	return order.CreateResult{Order: order.Order{
		ID:               orderID,
		OrderNumber:      orderNumber,
		Status:           order.StatusCreated,
		TotalAmountCents: total,
		Items:            responseItems,
		CreatedAt:        createdAt,
	}}, nil
}

type lockedProduct struct {
	id         uint64
	sku        string
	name       string
	priceCents uint64
	status     string
	quantity   uint32
	subtotal   uint64
}

func lockProduct(ctx context.Context, tx *sql.Tx, productID uint64) (lockedProduct, error) {
	var product lockedProduct
	err := tx.QueryRowContext(ctx, `
		SELECT id, sku, name, price_cents, status
		FROM products
		WHERE id = ?
		FOR SHARE`, productID).Scan(&product.id, &product.sku, &product.name, &product.priceCents, &product.status)
	return product, err
}

func checkedSubtotal(price uint64, quantity uint32) (uint64, error) {
	if price == 0 || quantity == 0 || price > order.MaxOrderAmount/uint64(quantity) {
		return 0, order.ErrAmountOverflow
	}
	return price * uint64(quantity), nil
}

func inventoryExists(ctx context.Context, tx *sql.Tx, productID uint64) (bool, error) {
	var marker uint8
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM inventories WHERE product_id = ?`, productID).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type storedOrder struct {
	order       order.Order
	requestHash []byte
}

func lookupOrder(ctx context.Context, db queryer, userID uint64, key []byte) (storedOrder, bool, error) {
	var stored storedOrder
	err := db.QueryRowContext(ctx, `
		SELECT id, order_no, request_hash, status, total_amount_cents, created_at
		FROM orders
		WHERE user_id = ? AND idempotency_key = ?`, userID, key).Scan(
		&stored.order.ID,
		&stored.order.OrderNumber,
		&stored.requestHash,
		&stored.order.Status,
		&stored.order.TotalAmountCents,
		&stored.order.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedOrder{}, false, nil
	}
	if err != nil {
		return storedOrder{}, false, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT product_id, sku_snapshot, name_snapshot, unit_price_cents, quantity,
		       unit_price_cents * quantity AS subtotal_cents
		FROM order_items
		WHERE order_id = ?
		ORDER BY product_id ASC`, stored.order.ID)
	if err != nil {
		return storedOrder{}, false, err
	}
	defer rows.Close()
	stored.order.Items = make([]order.Item, 0)
	for rows.Next() {
		var item order.Item
		if err := rows.Scan(&item.ProductID, &item.SKU, &item.Name, &item.UnitPriceCents, &item.Quantity, &item.SubtotalCents); err != nil {
			return storedOrder{}, false, err
		}
		stored.order.Items = append(stored.order.Items, item)
	}
	if err := rows.Err(); err != nil {
		return storedOrder{}, false, err
	}
	return stored, true, nil
}

func (s *OrderStore) lookupCommitted(ctx context.Context, userID uint64, key []byte) (storedOrder, bool, error) {
	return lookupOrder(ctx, s.db, userID, key)
}

func runWithRetry[T any](
	ctx context.Context,
	maxAttempts int,
	baseDelay time.Duration,
	operation func() (T, error),
	retryObservers ...func(time.Duration),
) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := operation()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if errors.Is(err, errCommitFailed) || !isRetryableTransactionError(err) || attempt == maxAttempts {
			return zero, err
		}

		delay := baseDelay * time.Duration(1<<(attempt-1))
		retryStarted := time.Now()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
			for _, observer := range retryObservers {
				if observer != nil {
					observer(time.Since(retryStarted))
				}
			}
		}
	}
	return zero, lastErr
}

func (s *OrderStore) observe(stage, outcome string, duration time.Duration) {
	if s.recorder != nil {
		s.recorder.ObserveTransactionStage(stage, outcome, duration)
	}
}

func errorStageOutcome(err error) string {
	if err == nil {
		return order.StageOutcomeSuccess
	}
	if isRetryableTransactionError(err) {
		return order.StageOutcomeRetryableFailure
	}
	return order.StageOutcomeFailure
}

func createStageOutcome(err error, result order.CreateResult) string {
	if err == nil {
		if result.Replayed {
			return order.StageOutcomeReplay
		}
		return order.StageOutcomeSuccess
	}
	if isRetryableTransactionError(err) {
		return order.StageOutcomeRetryableFailure
	}
	for _, businessErr := range []error{
		order.ErrIdempotencyConflict, order.ErrProductNotFound, order.ErrProductInactive,
		order.ErrInsufficientStock, order.ErrAmountOverflow,
	} {
		if errors.Is(err, businessErr) {
			return order.StageOutcomeBusinessRejected
		}
	}
	return order.StageOutcomeFailure
}

func isRetryableTransactionError(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func classifyInfrastructureError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1205, 1213:
			return fmt.Errorf("%s: transaction retries exhausted: %w", operation, order.ErrDatabaseUnavailable)
		case 1040, 1042, 1043, 1047, 1053, 1129, 1130, 1203, 2002, 2003, 2006, 2013:
			return fmt.Errorf("%s: %w", operation, order.ErrDatabaseUnavailable)
		}
	}
	// A connection can fail after a write, when the driver deliberately does
	// not return ErrBadConn (database/sql may retry that sentinel). Classify the
	// typed transport error for the HTTP response without retrying side effects.
	var networkErr net.Error
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysqldriver.ErrInvalidConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.As(err, &networkErr) {
		return fmt.Errorf("%s: %w", operation, order.ErrDatabaseUnavailable)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// An aborted write may expose a private driver error. Retain the typed cleanup
// failure as well, so a closed connection is classified without matching text.
// ErrTxDone is expected after Commit (including an uncertain Commit result).
func preserveRollbackFailure(operationErr, rollbackErr error) error {
	if rollbackErr == nil || errors.Is(rollbackErr, sql.ErrTxDone) {
		return operationErr
	}
	return errors.Join(operationErr, fmt.Errorf("rollback order transaction: %w", rollbackErr))
}

func generateOrderNumber() (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "ORD" + time.Now().UTC().Format("20060102150405") + hex.EncodeToString(random), nil
}
