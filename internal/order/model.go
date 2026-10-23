package order

import (
	"errors"
	"time"
)

const (
	StatusCreated   = "CREATED"
	StatusCancelled = "CANCELLED"

	MaxItemsPerOrder = 50
	MaxItemQuantity  = 1000
	MaxOrderAmount   = uint64(9_000_000_000_000_000)
	MaxRequestBytes  = 16 * 1024
)

var (
	ErrInvalidArgument     = errors.New("invalid order argument")
	ErrInvalidIdempotency  = errors.New("invalid idempotency key")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with another request")
	ErrProductNotFound     = errors.New("product not found")
	ErrProductInactive     = errors.New("product inactive")
	ErrInsufficientStock   = errors.New("insufficient stock")
	ErrAmountOverflow      = errors.New("order amount exceeds allowed range")
	ErrDatabaseUnavailable = errors.New("database unavailable")
	ErrCommitUncertain     = errors.New("transaction commit outcome is uncertain")
	ErrOrderNotFound       = errors.New("order not found")
	ErrInvalidState        = errors.New("order state transition is not allowed")
	ErrInventoryOverflow   = errors.New("inventory replenishment would overflow")
)

// CreateItem is intentionally price-free: product price and name are trusted
// only when read from MySQL inside the creation transaction.
type CreateItem struct {
	ProductID int64 `json:"product_id"`
	Quantity  int64 `json:"quantity"`
}

type CreateRequest struct {
	Items []CreateItem `json:"items"`
}

// NormalizedItem is sorted by ProductID before hashing and persistence.
type NormalizedItem struct {
	ProductID uint64
	Quantity  uint32
}

type Item struct {
	ProductID      uint64 `json:"product_id"`
	SKU            string `json:"sku"`
	Name           string `json:"name"`
	UnitPriceCents uint64 `json:"unit_price_cents"`
	Quantity       uint32 `json:"quantity"`
	SubtotalCents  uint64 `json:"subtotal_cents"`
}

type Order struct {
	ID               uint64    `json:"id"`
	OrderNumber      string    `json:"order_no"`
	Status           string    `json:"status"`
	TotalAmountCents uint64    `json:"total_amount_cents"`
	Items            []Item    `json:"items"`
	CreatedAt        time.Time `json:"created_at"`
}

type CreateResult struct {
	Order    Order
	Replayed bool
}

type CancelResult struct {
	Order    Order
	Replayed bool
}
