package product

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidArgument = errors.New("invalid product argument")
	ErrNotFound        = errors.New("product not found")
)

// Store is owned by the business package so that the service is independent
// from MySQL and can be tested without a database.
type Store interface {
	ListActive(ctx context.Context, afterID uint64, limit int) ([]Product, error)
	GetActiveByID(ctx context.Context, id uint64) (Product, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	if params.Limit < 1 || params.Limit > MaxPageSize {
		return ListResult{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidArgument, MaxPageSize)
	}

	// One extra row tells the service whether another page exists without an
	// additional COUNT query, which would be both slower and racy under writes.
	products, err := s.store.ListActive(ctx, params.AfterID, params.Limit+1)
	if err != nil {
		return ListResult{}, fmt.Errorf("list active products: %w", err)
	}

	result := ListResult{
		Products: make([]Product, 0, min(len(products), params.Limit)),
	}
	if len(products) > params.Limit {
		result.HasMore = true
		products = products[:params.Limit]
	}
	result.Products = append(result.Products, products...)
	if result.HasMore && len(result.Products) > 0 {
		nextAfterID := result.Products[len(result.Products)-1].ID
		result.NextAfterID = &nextAfterID
	}

	return result, nil
}

func (s *Service) Get(ctx context.Context, id uint64) (Product, error) {
	if id == 0 {
		return Product{}, fmt.Errorf("%w: product id must be greater than zero", ErrInvalidArgument)
	}

	found, err := s.store.GetActiveByID(ctx, id)
	if err != nil {
		return Product{}, fmt.Errorf("get active product %d: %w", id, err)
	}
	return found, nil
}
