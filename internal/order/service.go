package order

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"internshiplab/order-api/internal/auth"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type Store interface {
	Create(ctx context.Context, userID uint64, idempotencyKey []byte, requestHash [sha256.Size]byte, items []NormalizedItem) (CreateResult, error)
	GetByID(ctx context.Context, userID, id uint64) (Order, error)
	Cancel(ctx context.Context, userID, id uint64) (CancelResult, error)
}

func (s *Service) Get(ctx context.Context, id uint64) (Order, error) {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return Order{}, auth.ErrUnauthorized
	}
	if id == 0 {
		return Order{}, fmt.Errorf("%w: order id must be positive", ErrInvalidArgument)
	}
	found, err := s.store.GetByID(ctx, identity.UserID, id)
	if err != nil {
		return Order{}, fmt.Errorf("get order %d: %w", id, err)
	}
	return found, nil
}

func (s *Service) Cancel(ctx context.Context, id uint64) (CancelResult, error) {
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return CancelResult{}, auth.ErrUnauthorized
	}
	if id == 0 {
		return CancelResult{}, fmt.Errorf("%w: order id must be positive", ErrInvalidArgument)
	}
	result, err := s.store.Cancel(ctx, identity.UserID, id)
	if err != nil {
		return CancelResult{}, fmt.Errorf("cancel order %d: %w", id, err)
	}
	return result, nil
}

type Service struct {
	store    Store
	recorder StageRecorder
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func NewServiceWithRecorder(store Store, recorder StageRecorder) *Service {
	return &Service{store: store, recorder: recorder}
}

func (s *Service) Create(ctx context.Context, idempotencyKey string, request CreateRequest) (CreateResult, error) {
	totalStarted := time.Now()
	outcome := StageOutcomeFailure
	defer func() { observeStage(s.recorder, StageServiceCreateTotal, outcome, totalStarted) }()
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return CreateResult{}, auth.ErrUnauthorized
	}
	validationStarted := time.Now()
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		observeStage(s.recorder, StageValidationAndHash, StageOutcomeBusinessRejected, validationStarted)
		return CreateResult{}, err
	}
	items, err := normalizeItems(request.Items)
	if err != nil {
		observeStage(s.recorder, StageValidationAndHash, StageOutcomeBusinessRejected, validationStarted)
		return CreateResult{}, err
	}
	hash := HashNormalizedItems(items)
	observeStage(s.recorder, StageValidationAndHash, StageOutcomeSuccess, validationStarted)

	result, err := s.store.Create(ctx, identity.UserID, []byte(idempotencyKey), hash, items)
	if err != nil {
		if isBusinessRejection(err) {
			outcome = StageOutcomeBusinessRejected
		}
		return CreateResult{}, fmt.Errorf("create order: %w", err)
	}
	if result.Replayed {
		outcome = StageOutcomeReplay
	} else {
		outcome = StageOutcomeSuccess
	}
	return result, nil
}

func observeStage(recorder StageRecorder, stage, outcome string, started time.Time) {
	if recorder != nil {
		recorder.ObserveTransactionStage(stage, outcome, time.Since(started))
	}
}

func isBusinessRejection(err error) bool {
	return errors.Is(err, ErrInvalidArgument) || errors.Is(err, ErrInvalidIdempotency) || errors.Is(err, ErrIdempotencyConflict) ||
		errors.Is(err, ErrProductNotFound) || errors.Is(err, ErrProductInactive) || errors.Is(err, ErrInsufficientStock) || errors.Is(err, ErrAmountOverflow)
}

func validateIdempotencyKey(key string) error {
	if !utf8.ValidString(key) || len([]byte(key)) < 1 || len([]byte(key)) > 64 || !idempotencyKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: must be 1-64 ASCII bytes and match %s", ErrInvalidIdempotency, idempotencyKeyPattern.String())
	}
	return nil
}

func normalizeItems(input []CreateItem) ([]NormalizedItem, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: items must not be empty", ErrInvalidArgument)
	}
	if len(input) > MaxItemsPerOrder {
		return nil, fmt.Errorf("%w: at most %d distinct items are allowed", ErrInvalidArgument, MaxItemsPerOrder)
	}

	items := make([]NormalizedItem, 0, len(input))
	seen := make(map[uint64]struct{}, len(input))
	for index, item := range input {
		if item.ProductID <= 0 {
			return nil, fmt.Errorf("%w: items[%d].product_id must be positive", ErrInvalidArgument, index)
		}
		if item.Quantity <= 0 || item.Quantity > MaxItemQuantity {
			return nil, fmt.Errorf("%w: items[%d].quantity must be between 1 and %d", ErrInvalidArgument, index, MaxItemQuantity)
		}
		productID := uint64(item.ProductID)
		if _, exists := seen[productID]; exists {
			return nil, fmt.Errorf("%w: duplicate product_id %d", ErrInvalidArgument, productID)
		}
		seen[productID] = struct{}{}
		items = append(items, NormalizedItem{ProductID: productID, Quantity: uint32(item.Quantity)})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ProductID < items[j].ProductID })
	return items, nil
}

// HashNormalizedItems uses a versioned fixed-width binary representation. It
// is independent of JSON whitespace and client item ordering.
func HashNormalizedItems(items []NormalizedItem) [sha256.Size]byte {
	canonical := make([]byte, 2+len(items)*12)
	canonical[0] = 1 // canonical request format version
	canonical[1] = byte(len(items))
	for index, item := range items {
		offset := 2 + index*12
		binary.BigEndian.PutUint64(canonical[offset:offset+8], item.ProductID)
		binary.BigEndian.PutUint32(canonical[offset+8:offset+12], item.Quantity)
	}
	return sha256.Sum256(canonical)
}
