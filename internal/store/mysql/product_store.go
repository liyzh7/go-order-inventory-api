package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"internshiplab/order-api/internal/product"
)

const listActiveProductsQuery = `
SELECT p.id, p.sku, p.name, p.price_cents, i.available_qty
FROM products AS p
INNER JOIN inventories AS i ON i.product_id = p.id
WHERE p.status = ? AND p.id > ?
ORDER BY p.id ASC
LIMIT ?`

const getActiveProductByIDQuery = `
SELECT p.id, p.sku, p.name, p.price_cents, i.available_qty
FROM products AS p
INNER JOIN inventories AS i ON i.product_id = p.id
WHERE p.id = ? AND p.status = ?`

type ProductStore struct {
	db           *sql.DB
	queryTimeout time.Duration
}

// NewProductStore creates a store whose every query is bounded independently
// while still respecting cancellation from the incoming request context.
func NewProductStore(db *sql.DB, queryTimeout time.Duration) *ProductStore {
	return &ProductStore{db: db, queryTimeout: queryTimeout}
}

func (s *ProductStore) ListActive(ctx context.Context, afterID uint64, limit int) ([]product.Product, error) {
	queryContext, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(queryContext, listActiveProductsQuery, product.StatusActive, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query active products: %w", err)
	}
	defer rows.Close()

	products := make([]product.Product, 0)
	for rows.Next() {
		var item product.Product
		if err := rows.Scan(
			&item.ID,
			&item.SKU,
			&item.Name,
			&item.PriceCents,
			&item.AvailableQuantity,
		); err != nil {
			return nil, fmt.Errorf("scan active product: %w", err)
		}
		products = append(products, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active products: %w", err)
	}

	return products, nil
}

func (s *ProductStore) GetActiveByID(ctx context.Context, id uint64) (product.Product, error) {
	queryContext, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	var item product.Product
	err := s.db.QueryRowContext(queryContext, getActiveProductByIDQuery, id, product.StatusActive).Scan(
		&item.ID,
		&item.SKU,
		&item.Name,
		&item.PriceCents,
		&item.AvailableQuantity,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return product.Product{}, product.ErrNotFound
	}
	if err != nil {
		return product.Product{}, fmt.Errorf("query active product by id: %w", err)
	}

	return item, nil
}
