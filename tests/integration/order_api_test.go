//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"internshiplab/order-api/internal/order"
)

func TestOrderCreationAndIdempotencyWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	firstID := insertProductWithInventory(t, ctx, db, "ORDER-A", "First snapshot", 1250, "ACTIVE", 20)
	secondID := insertProductWithInventory(t, ctx, db, "ORDER-B", "Second snapshot", 2750, "ACTIVE", 20)
	inactiveID := insertProductWithInventory(t, ctx, db, "ORDER-OFF", "Inactive", 500, "INACTIVE", 20)
	router := newOrderRouter(t, db)

	t.Run("single product creates order with snapshot and exact total", func(t *testing.T) {
		response := postOrder(router, "single-001", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, firstID))
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", response.Code, response.Body.String())
		}
		created := decodeOrderResponse(t, response)
		if created.TotalAmountCents != 2500 || len(created.Items) != 1 {
			t.Fatalf("created order = %#v", created)
		}
		item := created.Items[0]
		if item.Name != "First snapshot" || item.UnitPriceCents != 1250 || item.SubtotalCents != 2500 {
			t.Errorf("snapshot item = %#v", item)
		}
		assertInventory(t, db, firstID, 18)
		assertOrderState(t, db, "single-001", 1, 1, 2500)

		replay := postOrder(router, "single-001", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, firstID))
		if replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("replay status=%d header=%q body=%s", replay.Code, replay.Header().Get("Idempotency-Replayed"), replay.Body.String())
		}
		if replayOrder := decodeOrderResponse(t, replay); replayOrder.ID != created.ID {
			t.Errorf("replay order ID = %d, want %d", replayOrder.ID, created.ID)
		}
		assertInventory(t, db, firstID, 18)
		assertOrderState(t, db, "single-001", 1, 1, 2500)

		conflict := postOrder(router, "single-001", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":3}]}`, firstID))
		if conflict.Code != http.StatusConflict {
			t.Fatalf("conflict status=%d, want 409; body=%s", conflict.Code, conflict.Body.String())
		}
		assertInventory(t, db, firstID, 18)
	})

	t.Run("multi product request ordering is idempotent", func(t *testing.T) {
		forward := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":1},{"product_id":%d,"quantity":2}]}`, firstID, secondID)
		reverse := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2},{"product_id":%d,"quantity":1}]}`, secondID, firstID)
		createdResponse := postOrder(router, "multi-order-001", forward)
		if createdResponse.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
		}
		created := decodeOrderResponse(t, createdResponse)
		if created.TotalAmountCents != 6750 || len(created.Items) != 2 || created.Items[0].ProductID != firstID {
			t.Fatalf("created order = %#v", created)
		}
		replayedResponse := postOrder(router, "multi-order-001", reverse)
		if replayedResponse.Code != http.StatusOK || replayedResponse.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("replay status=%d body=%s", replayedResponse.Code, replayedResponse.Body.String())
		}
		if replayed := decodeOrderResponse(t, replayedResponse); replayed.ID != created.ID {
			t.Errorf("replayed ID=%d, want %d", replayed.ID, created.ID)
		}
	})

	t.Run("missing inactive and insufficient products do not create orders", func(t *testing.T) {
		tests := []struct {
			key        string
			productID  uint64
			quantity   int
			wantStatus int
		}{
			{key: "missing-001", productID: 9_999_999, quantity: 1, wantStatus: http.StatusNotFound},
			{key: "inactive-001", productID: inactiveID, quantity: 1, wantStatus: http.StatusConflict},
			{key: "stock-001", productID: secondID, quantity: 100, wantStatus: http.StatusConflict},
		}
		for _, tt := range tests {
			response := postOrder(router, tt.key, fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":%d}]}`, tt.productID, tt.quantity))
			if response.Code != tt.wantStatus {
				t.Errorf("%s status=%d, want %d; body=%s", tt.key, response.Code, tt.wantStatus, response.Body.String())
			}
			assertOrderState(t, db, tt.key, 0, 0, 0)
		}
	})
}

func TestOrderTransactionRollbackWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	firstID := insertProductWithInventory(t, ctx, db, "ROLLBACK-A", "Rollback first", 1000, "ACTIVE", 10)
	secondID := insertProductWithInventory(t, ctx, db, "ROLLBACK-B", "Rollback second", 2000, "ACTIVE", 0)
	router := newOrderRouter(t, db)

	response := postOrder(router, "rollback-stock-001", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":3},{"product_id":%d,"quantity":1}]}`, firstID, secondID))
	if response.Code != http.StatusConflict {
		t.Fatalf("stock rollback status=%d body=%s", response.Code, response.Body.String())
	}
	assertInventory(t, db, firstID, 10)
	assertInventory(t, db, secondID, 0)
	assertOrderState(t, db, "rollback-stock-001", 0, 0, 0)

	failingID := insertProductWithInventory(t, ctx, db, "ROLLBACK-C", "FAIL_ITEM", 3000, "ACTIVE", 10)
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER reject_controlled_order_item
		BEFORE INSERT ON order_items FOR EACH ROW
		BEGIN
			IF NEW.name_snapshot = 'FAIL_ITEM' THEN
				SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'controlled order item failure';
			END IF;
		END`); err != nil {
		t.Fatalf("create controlled failure trigger: %v", err)
	}
	response = postOrder(router, "rollback-item-001", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, failingID))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("item failure status=%d, want 500; body=%s", response.Code, response.Body.String())
	}
	assertInventory(t, db, failingID, 10)
	assertOrderState(t, db, "rollback-item-001", 0, 0, 0)
}

func TestOrderAmountLimitRollsBackWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	router := newOrderRouter(t, db)

	items := make([]string, 0, 10)
	ids := make([]uint64, 0, 10)
	for index := 0; index < 10; index++ {
		id := insertProductWithInventory(t, ctx, db, fmt.Sprintf("MAX-%02d", index), fmt.Sprintf("Maximum %d", index), 1_000_000_000_000, "ACTIVE", 1000)
		ids = append(ids, id)
		items = append(items, fmt.Sprintf(`{"product_id":%d,"quantity":1000}`, id))
	}
	response := postOrder(router, "amount-limit-001", `{"items":[`+strings.Join(items, ",")+`]}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("amount limit status=%d, want 400; body=%s", response.Code, response.Body.String())
	}
	assertOrderState(t, db, "amount-limit-001", 0, 0, 0)
	for _, id := range ids {
		assertInventory(t, db, id, 1000)
	}
}

func TestConcurrentIdempotencyWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	productID := insertProductWithInventory(t, ctx, db, "CONCURRENT-IDEM", "Concurrent idem", 888, "ACTIVE", 20)
	router := newOrderRouter(t, db)
	body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, productID)

	const requests = 12
	responses := runConcurrentOrders(router, requests, func(int) (string, string) { return "same-concurrent-key", body })
	created := 0
	replayed := 0
	var orderID uint64
	for _, response := range responses {
		switch response.Code {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			if response.Header().Get("Idempotency-Replayed") != "true" {
				t.Errorf("200 response missing replay header: %s", response.Body.String())
			}
			replayed++
		default:
			t.Fatalf("unexpected concurrent response %d: %s", response.Code, response.Body.String())
		}
		got := decodeOrderResponse(t, response).ID
		if orderID == 0 {
			orderID = got
		} else if got != orderID {
			t.Errorf("order ID=%d, want all responses to use %d", got, orderID)
		}
	}
	if created != 1 || replayed != requests-1 {
		t.Errorf("created=%d replayed=%d, want 1 and %d", created, replayed, requests-1)
	}
	assertInventory(t, db, productID, 18)
	assertOrderState(t, db, "same-concurrent-key", 1, 1, 1776)
}

func TestConcurrentInventoryCompetitionWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	productID := insertProductWithInventory(t, ctx, db, "CONCURRENT-STOCK", "Concurrent stock", 700, "ACTIVE", 5)
	router := newOrderRouter(t, db)
	body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":1}]}`, productID)

	const requests = 12
	responses := runConcurrentOrders(router, requests, func(index int) (string, string) {
		return fmt.Sprintf("stock-key-%02d", index), body
	})
	var successful int
	var insufficient int
	for _, response := range responses {
		switch response.Code {
		case http.StatusCreated:
			successful++
		case http.StatusConflict:
			insufficient++
		default:
			t.Fatalf("unexpected stock response %d: %s", response.Code, response.Body.String())
		}
	}
	if successful != 5 || insufficient != requests-5 {
		t.Errorf("successful=%d insufficient=%d, want 5 and %d", successful, insufficient, requests-5)
	}
	assertInventory(t, db, productID, 0)
	var orderCount, itemCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM order_items`).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 5 || itemCount != 5 {
		t.Errorf("orders=%d items=%d, want 5 each", orderCount, itemCount)
	}
}

func newOrderRouter(t *testing.T, db *sql.DB) http.Handler {
	router := newAuthenticatedRouter(t, db)
	return registerClient(t, router).handler()
}

func postOrder(handler http.Handler, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeOrderResponse(t *testing.T, response *httptest.ResponseRecorder) order.Order {
	t.Helper()
	var payload struct {
		Data order.Order `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode order response: %v; body=%s", err, response.Body.String())
	}
	return payload.Data
}

func assertInventory(t *testing.T, db *sql.DB, productID uint64, want uint32) {
	t.Helper()
	var got uint32
	if err := db.QueryRow(`SELECT available_qty FROM inventories WHERE product_id = ?`, productID).Scan(&got); err != nil {
		t.Fatalf("read inventory for %d: %v", productID, err)
	}
	if got != want {
		t.Errorf("inventory for %d = %d, want %d", productID, got, want)
	}
}

func assertOrderState(t *testing.T, db *sql.DB, key string, wantOrders, wantItems int, wantTotal uint64) {
	t.Helper()
	var orderCount int
	var total uint64
	if err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(total_amount_cents), 0)
		FROM orders WHERE idempotency_key = ?`, []byte(key)).Scan(&orderCount, &total); err != nil {
		t.Fatalf("read order state for %q: %v", key, err)
	}
	var itemCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM order_items oi
		INNER JOIN orders o ON o.id = oi.order_id
		WHERE o.idempotency_key = ?`, []byte(key)).Scan(&itemCount); err != nil {
		t.Fatalf("read order item state for %q: %v", key, err)
	}
	if orderCount != wantOrders || itemCount != wantItems || total != wantTotal {
		t.Errorf("key %q: orders=%d items=%d total=%d, want %d/%d/%d", key, orderCount, itemCount, total, wantOrders, wantItems, wantTotal)
	}
}

func runConcurrentOrders(
	handler http.Handler,
	count int,
	input func(index int) (key string, body string),
) []*httptest.ResponseRecorder {
	responses := make([]*httptest.ResponseRecorder, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(count)
	for index := 0; index < count; index++ {
		go func(index int) {
			defer wait.Done()
			key, body := input(index)
			<-start
			responses[index] = postOrder(handler, key, body)
		}(index)
	}
	close(start)
	wait.Wait()
	return responses
}
