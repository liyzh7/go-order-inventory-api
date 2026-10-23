package product

import (
	"context"
	"errors"
	"testing"
)

type fakeStore struct {
	listFn func(ctx context.Context, afterID uint64, limit int) ([]Product, error)
	getFn  func(ctx context.Context, id uint64) (Product, error)
}

func (f *fakeStore) ListActive(ctx context.Context, afterID uint64, limit int) ([]Product, error) {
	if f.listFn == nil {
		panic("unexpected ListActive call")
	}
	return f.listFn(ctx, afterID, limit)
}

func (f *fakeStore) GetActiveByID(ctx context.Context, id uint64) (Product, error) {
	if f.getFn == nil {
		panic("unexpected GetActiveByID call")
	}
	return f.getFn(ctx, id)
}

func TestServiceList(t *testing.T) {
	tests := []struct {
		name       string
		params     ListParams
		store      *fakeStore
		wantIDs    []uint64
		wantMore   bool
		wantCursor *uint64
		wantErr    error
	}{
		{
			name:   "returns stable page",
			params: ListParams{Limit: 2, AfterID: 10},
			store: &fakeStore{listFn: func(_ context.Context, afterID uint64, limit int) ([]Product, error) {
				if afterID != 10 || limit != 3 {
					t.Fatalf("store args = (%d, %d), want (10, 3)", afterID, limit)
				}
				return []Product{{ID: 11}, {ID: 12}, {ID: 13}}, nil
			}},
			wantIDs:    []uint64{11, 12},
			wantMore:   true,
			wantCursor: uint64Pointer(12),
		},
		{
			name:   "normalizes nil result to empty list",
			params: ListParams{Limit: DefaultPageSize},
			store: &fakeStore{listFn: func(_ context.Context, _ uint64, limit int) ([]Product, error) {
				if limit != DefaultPageSize+1 {
					t.Fatalf("store limit = %d, want %d", limit, DefaultPageSize+1)
				}
				return nil, nil
			}},
			wantIDs: []uint64{},
		},
		{
			name:   "wraps store failure",
			params: ListParams{Limit: 1},
			store: &fakeStore{listFn: func(context.Context, uint64, int) ([]Product, error) {
				return nil, errStoreUnavailable
			}},
			wantErr: errStoreUnavailable,
		},
		{
			name:    "rejects zero limit before store call",
			params:  ListParams{Limit: 0},
			store:   &fakeStore{},
			wantErr: ErrInvalidArgument,
		},
		{
			name:    "rejects oversized limit before store call",
			params:  ListParams{Limit: MaxPageSize + 1},
			store:   &fakeStore{},
			wantErr: ErrInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := NewService(tt.store).List(context.Background(), tt.params)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want error matching %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(result.Products) != len(tt.wantIDs) {
				t.Fatalf("product count = %d, want %d", len(result.Products), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				if result.Products[i].ID != wantID {
					t.Errorf("product[%d].ID = %d, want %d", i, result.Products[i].ID, wantID)
				}
			}
			if result.Products == nil {
				t.Error("Products is nil, want a JSON-safe empty slice")
			}
			if result.HasMore != tt.wantMore {
				t.Errorf("HasMore = %v, want %v", result.HasMore, tt.wantMore)
			}
			assertOptionalUint64(t, result.NextAfterID, tt.wantCursor)
		})
	}
}

func TestServiceGet(t *testing.T) {
	wanted := Product{ID: 9, SKU: "BOOK-001", Name: "Book", PriceCents: 2999, AvailableQuantity: 7}
	tests := []struct {
		name    string
		id      uint64
		store   *fakeStore
		want    Product
		wantErr error
	}{
		{
			name: "returns active product",
			id:   9,
			store: &fakeStore{getFn: func(_ context.Context, id uint64) (Product, error) {
				if id != 9 {
					t.Fatalf("store id = %d, want 9", id)
				}
				return wanted, nil
			}},
			want: wanted,
		},
		{
			name:    "rejects zero id before store call",
			id:      0,
			store:   &fakeStore{},
			wantErr: ErrInvalidArgument,
		},
		{
			name: "preserves not found classification",
			id:   99,
			store: &fakeStore{getFn: func(context.Context, uint64) (Product, error) {
				return Product{}, ErrNotFound
			}},
			wantErr: ErrNotFound,
		},
		{
			name: "wraps store failure",
			id:   9,
			store: &fakeStore{getFn: func(context.Context, uint64) (Product, error) {
				return Product{}, errStoreUnavailable
			}},
			wantErr: errStoreUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewService(tt.store).Get(context.Background(), tt.id)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want error matching %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Get() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

var errStoreUnavailable = errors.New("store unavailable")

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func assertOptionalUint64(t *testing.T, got, want *uint64) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Errorf("cursor = %v, want %v", got, want)
		}
		return
	}
	if *got != *want {
		t.Errorf("cursor = %d, want %d", *got, *want)
	}
}
