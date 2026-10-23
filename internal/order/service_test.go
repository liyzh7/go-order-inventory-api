package order

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"

	"internshiplab/order-api/internal/auth"
)

type recordingStore struct {
	userID uint64
	key    []byte
	hash   [sha256.Size]byte
	items  []NormalizedItem
	result CreateResult
	err    error
}

func (s *recordingStore) GetByID(_ context.Context, _ uint64, id uint64) (Order, error) {
	return Order{ID: id, Status: StatusCreated}, s.err
}

func (s *recordingStore) Cancel(_ context.Context, _ uint64, id uint64) (CancelResult, error) {
	return CancelResult{Order: Order{ID: id, Status: StatusCancelled}}, s.err
}

func TestServiceLifecycleValidationAndErrors(t *testing.T) {
	service := NewService(&recordingStore{})
	if _, err := service.Get(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Get(0) error=%v", err)
	}
	if _, err := service.Cancel(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Cancel(0) error=%v", err)
	}
	if found, err := service.Get(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), 7); err != nil || found.ID != 7 {
		t.Fatalf("Get(7)=%+v err=%v", found, err)
	}
	if result, err := service.Cancel(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), 7); err != nil || result.Order.Status != StatusCancelled {
		t.Fatalf("Cancel(7)=%+v err=%v", result, err)
	}
	service = NewService(&recordingStore{err: ErrOrderNotFound})
	if _, err := service.Get(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), 7); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("Get wraps error=%v", err)
	}
	if _, err := service.Cancel(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), 7); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("Cancel wraps error=%v", err)
	}
}

func (s *recordingStore) Create(_ context.Context, userID uint64, key []byte, hash [sha256.Size]byte, items []NormalizedItem) (CreateResult, error) {
	s.userID = userID
	s.key = append([]byte(nil), key...)
	s.hash = hash
	s.items = append([]NormalizedItem(nil), items...)
	return s.result, s.err
}

func TestServiceCreateNormalizesAndHashesItems(t *testing.T) {
	store := &recordingStore{result: CreateResult{Order: Order{ID: 7}}}
	service := NewService(store)
	request := CreateRequest{Items: []CreateItem{{ProductID: 9, Quantity: 2}, {ProductID: 3, Quantity: 4}}}

	result, err := service.Create(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), "checkout-001", request)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.Order.ID != 7 {
		t.Fatalf("order ID = %d, want 7", result.Order.ID)
	}
	wantItems := []NormalizedItem{{ProductID: 3, Quantity: 4}, {ProductID: 9, Quantity: 2}}
	if !reflect.DeepEqual(store.items, wantItems) {
		t.Errorf("normalized items = %#v, want %#v", store.items, wantItems)
	}
	if string(store.key) != "checkout-001" {
		t.Errorf("key = %q, want checkout-001", store.key)
	}
	if store.userID != 1 {
		t.Fatalf("store userID=%d", store.userID)
	}
	if store.hash != HashNormalizedItems(wantItems) {
		t.Error("store received a hash that does not match normalized items")
	}
}

func TestOrderServiceRequiresIdentity(t *testing.T) {
	service := NewService(&recordingStore{})
	if _, err := service.Create(context.Background(), "key", CreateRequest{}); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), 1); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := service.Cancel(context.Background(), 1); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
}

func TestHashNormalizedItemsIgnoresInputOrderAfterNormalization(t *testing.T) {
	first, err := normalizeItems([]CreateItem{{ProductID: 1, Quantity: 2}, {ProductID: 2, Quantity: 3}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeItems([]CreateItem{{ProductID: 2, Quantity: 3}, {ProductID: 1, Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if HashNormalizedItems(first) != HashNormalizedItems(second) {
		t.Error("equivalent requests produced different hashes")
	}
}

func TestServiceCreateValidation(t *testing.T) {
	fiftyOne := make([]CreateItem, MaxItemsPerOrder+1)
	for index := range fiftyOne {
		fiftyOne[index] = CreateItem{ProductID: int64(index + 1), Quantity: 1}
	}
	tests := []struct {
		name    string
		key     string
		request CreateRequest
		wantErr error
	}{
		{name: "missing key", request: CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: 1}}}, wantErr: ErrInvalidIdempotency},
		{name: "key has spaces", key: "bad key", request: CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: 1}}}, wantErr: ErrInvalidIdempotency},
		{name: "key too long", key: strings.Repeat("a", 65), request: CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: 1}}}, wantErr: ErrInvalidIdempotency},
		{name: "empty items", key: "key-1", request: CreateRequest{}, wantErr: ErrInvalidArgument},
		{name: "too many items", key: "key-1", request: CreateRequest{Items: fiftyOne}, wantErr: ErrInvalidArgument},
		{name: "nonpositive product", key: "key-1", request: CreateRequest{Items: []CreateItem{{ProductID: 0, Quantity: 1}}}, wantErr: ErrInvalidArgument},
		{name: "nonpositive quantity", key: "key-1", request: CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: 0}}}, wantErr: ErrInvalidArgument},
		{name: "quantity too large", key: "key-1", request: CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: MaxItemQuantity + 1}}}, wantErr: ErrInvalidArgument},
		{name: "duplicate product", key: "key-1", request: CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: 1}, {ProductID: 1, Quantity: 2}}}, wantErr: ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &recordingStore{}
			_, err := NewService(store).Create(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), tt.key, tt.request)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if store.items != nil {
				t.Error("store was called for invalid input")
			}
		})
	}
}

func TestServiceCreateWrapsStoreError(t *testing.T) {
	wanted := errors.New("store failed")
	store := &recordingStore{err: wanted}
	_, err := NewService(store).Create(auth.WithIdentity(context.Background(), auth.Identity{UserID: 1}), "key-1", CreateRequest{Items: []CreateItem{{ProductID: 1, Quantity: 1}}})
	if !errors.Is(err, wanted) {
		t.Fatalf("error = %v, want wrapped store error", err)
	}
}
