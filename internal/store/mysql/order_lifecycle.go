package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"internshiplab/order-api/internal/order"
)

const maxInventoryQuantity uint64 = 1<<32 - 1

// One statement supplies a single InnoDB read view for both header and items.
// No current product columns are read: historical snapshots are immutable.
const getOrderByIDQuery = `
SELECT o.id, o.order_no, o.status, o.total_amount_cents, o.created_at,
       oi.product_id, oi.sku_snapshot, oi.name_snapshot,
       oi.unit_price_cents, oi.quantity
FROM orders o
LEFT JOIN order_items oi ON oi.order_id = o.id
WHERE o.id = ? AND o.user_id = ?
ORDER BY oi.product_id ASC`

func (s *OrderStore) GetByID(ctx context.Context, userID, id uint64) (order.Order, error) {
	queryContext, cancel := context.WithTimeout(ctx, s.operationLimit)
	defer cancel()
	found, err := readOrderByID(queryContext, s.db, userID, id)
	if err != nil {
		return order.Order{}, classifyInfrastructureError("query order detail", err)
	}
	return found, nil
}

func readOrderByID(ctx context.Context, db queryer, userID, id uint64) (order.Order, error) {
	rows, err := db.QueryContext(ctx, getOrderByIDQuery, id, userID)
	if err != nil {
		return order.Order{}, err
	}
	defer rows.Close()
	found := order.Order{Items: make([]order.Item, 0)}
	var total uint64
	for rows.Next() {
		var productID, price, quantity sql.NullInt64
		var sku, name sql.NullString
		if err := rows.Scan(&found.ID, &found.OrderNumber, &found.Status,
			&found.TotalAmountCents, &found.CreatedAt, &productID, &sku, &name, &price, &quantity); err != nil {
			return order.Order{}, err
		}
		if !productID.Valid || !price.Valid || !quantity.Valid || !sku.Valid || !name.Valid || productID.Int64 <= 0 || quantity.Int64 < 1 || quantity.Int64 > order.MaxItemQuantity || price.Int64 <= 0 {
			return order.Order{}, errors.New("stored order has invalid or missing items")
		}
		subtotal, err := checkedSubtotal(uint64(price.Int64), uint32(quantity.Int64))
		if err != nil || total > order.MaxOrderAmount-subtotal {
			return order.Order{}, errors.New("stored order amount exceeds boundary")
		}
		total += subtotal
		found.Items = append(found.Items, order.Item{ProductID: uint64(productID.Int64),
			SKU: sku.String, Name: name.String, UnitPriceCents: uint64(price.Int64),
			Quantity: uint32(quantity.Int64), SubtotalCents: subtotal})
	}
	if err := rows.Err(); err != nil {
		return order.Order{}, err
	}
	if found.ID == 0 {
		return order.Order{}, order.ErrOrderNotFound
	}
	if total != found.TotalAmountCents {
		return order.Order{}, errors.New("stored order total differs from item snapshots")
	}
	return found, nil
}

func (s *OrderStore) Cancel(ctx context.Context, userID, id uint64) (order.CancelResult, error) {
	operationContext, cancel := context.WithTimeout(ctx, s.operationLimit)
	defer cancel()
	result, err := runWithRetry(operationContext, s.maxAttempts, s.retryBaseDelay, func() (order.CancelResult, error) {
		return s.cancelOnce(operationContext, userID, id)
	})
	if err == nil {
		return result, nil
	}
	if errors.Is(err, errCommitFailed) {
		// No second mutation follows an ambiguous Commit. Only a fresh, bounded
		// read can confirm the committed postcondition; cancellation remains safe
		// to retry later because the state transition is the replenishment gate.
		found, lookupErr := s.GetByID(ctx, userID, id)
		return resolveCancellationCommit(found, lookupErr, err)
	}
	return order.CancelResult{}, classifyInfrastructureError("cancel order transaction", err)
}

func resolveCancellationCommit(found order.Order, lookupErr, commitErr error) (order.CancelResult, error) {
	if lookupErr == nil && found.Status == order.StatusCancelled {
		return order.CancelResult{Order: found, Replayed: true}, nil
	}
	return order.CancelResult{}, fmt.Errorf("%w: commit=%v; verification=%v", order.ErrCommitUncertain, commitErr, lookupErr)
}

func (s *OrderStore) cancelOnce(ctx context.Context, userID, id uint64) (result order.CancelResult, returnErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return order.CancelResult{}, fmt.Errorf("begin cancellation: %w", err)
	}
	defer func() { returnErr = preserveRollbackFailure(returnErr, tx.Rollback()) }()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM orders WHERE id = ? AND user_id = ? FOR UPDATE`, id, userID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return order.CancelResult{}, order.ErrOrderNotFound
	}
	if err != nil {
		return order.CancelResult{}, fmt.Errorf("lock order: %w", err)
	}
	if status != order.StatusCreated && status != order.StatusCancelled {
		return order.CancelResult{}, order.ErrInvalidState
	}
	found, err := readOrderByID(ctx, tx, userID, id)
	if err != nil {
		return order.CancelResult{}, fmt.Errorf("read locked order: %w", err)
	}
	if status == order.StatusCancelled {
		return order.CancelResult{Order: found, Replayed: true}, nil
	}
	changed, err := tx.ExecContext(ctx, `UPDATE orders SET status = 'CANCELLED' WHERE id = ? AND user_id = ? AND status = 'CREATED'`, id, userID)
	if err != nil {
		return order.CancelResult{}, fmt.Errorf("transition order state: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return order.CancelResult{}, fmt.Errorf("read transition result: %w", err)
	}
	if count != 1 {
		return order.CancelResult{}, order.ErrInvalidState
	}
	// Items were read in ascending product ID order. The same inventory order
	// is used by creation and cancellation, including shared-stock orders.
	for _, item := range found.Items {
		changed, err := tx.ExecContext(ctx, `
			UPDATE inventories SET available_qty = available_qty + ?
			WHERE product_id = ? AND available_qty <= ?`,
			item.Quantity, item.ProductID, maxInventoryQuantity-uint64(item.Quantity))
		if err != nil {
			return order.CancelResult{}, fmt.Errorf("restore product %d: %w", item.ProductID, err)
		}
		count, err := changed.RowsAffected()
		if err != nil {
			return order.CancelResult{}, fmt.Errorf("read inventory restoration result: %w", err)
		}
		if count != 1 {
			exists, err := inventoryExists(ctx, tx, item.ProductID)
			if err != nil {
				return order.CancelResult{}, fmt.Errorf("inspect inventory: %w", err)
			}
			if !exists {
				return order.CancelResult{}, errors.New("stored order product has no inventory row")
			}
			return order.CancelResult{}, order.ErrInventoryOverflow
		}
	}
	if err := tx.Commit(); err != nil {
		return order.CancelResult{}, fmt.Errorf("%w: %w", errCommitFailed, err)
	}
	found.Status = order.StatusCancelled
	return order.CancelResult{Order: found}, nil
}
