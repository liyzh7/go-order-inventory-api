package product

const (
	StatusActive = "ACTIVE"

	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Product is the read model returned by the public product endpoints.
// PriceCents deliberately uses an integer so that API clients never depend on
// binary floating-point rounding for monetary values.
type Product struct {
	ID                uint64 `json:"id"`
	SKU               string `json:"sku"`
	Name              string `json:"name"`
	PriceCents        uint64 `json:"price_cents"`
	AvailableQuantity uint32 `json:"available_quantity"`
}

// ListParams describes stable, ID-based cursor pagination.
type ListParams struct {
	Limit   int
	AfterID uint64
}

// ListResult carries a page and the cursor required to request the next page.
type ListResult struct {
	Products    []Product
	NextAfterID *uint64
	HasMore     bool
}
