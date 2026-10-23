//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"internshiplab/order-api/internal/order"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

func TestOrderDetailAndCancellationWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	a := insertProductWithInventory(t, context.Background(), db, "LIFE-A", "Original A", 1000, "ACTIVE", 20)
	b := insertProductWithInventory(t, context.Background(), db, "LIFE-B", "Original B", 2500, "ACTIVE", 30)
	router := newOrderRouter(t, db)
	for _, multiple := range []bool{false, true} {
		t.Run(fmt.Sprintf("multiple=%v", multiple), func(t *testing.T) {
			key := fmt.Sprintf("life-%v", multiple)
			body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, a)
			if multiple {
				body = fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":3},{"product_id":%d,"quantity":2}]}`, b, a)
			}
			created := createLifecycleOrder(t, router, key, body)
			assertInventory(t, db, a, 18)
			if multiple {
				assertInventory(t, db, b, 27)
			}
			path := fmt.Sprintf("/orders/%d", created.ID)
			response := performRequest(router, http.MethodGet, path)
			assertHTTPStatus(t, response, 200)
			found := decodeOrderResponse(t, response)
			if found.Status != order.StatusCreated || found.ID != created.ID || found.TotalAmountCents != created.TotalAmountCents || !found.CreatedAt.Equal(created.CreatedAt) {
				t.Fatalf("detail=%+v created=%+v", found, created)
			}
			if strings.Contains(response.Body.String(), "idempotency_key") || strings.Contains(response.Body.String(), "request_hash") {
				t.Fatal("private fields leaked")
			}
			assertSnapshotsAndTotals(t, found)
			response = performRequest(router, http.MethodPost, path+"/cancel")
			assertHTTPStatus(t, response, 200)
			if response.Header().Get("Cancellation-Replayed") != "" {
				t.Fatal("first cancellation reported replay")
			}
			if decodeOrderResponse(t, response).Status != order.StatusCancelled {
				t.Fatal("cancel did not return CANCELLED")
			}
			for repeat := 0; repeat < 2; repeat++ {
				response = performRequest(router, http.MethodPost, path+"/cancel")
				assertHTTPStatus(t, response, 200)
				if response.Header().Get("Cancellation-Replayed") != "true" {
					t.Fatal("repeat cancellation missing header")
				}
			}
			assertInventory(t, db, a, 20)
			assertInventory(t, db, b, 30)
			assertLifecycleState(t, db, created.ID, order.StatusCancelled, len(created.Items))
			response = performRequest(router, http.MethodGet, path)
			assertHTTPStatus(t, response, 200)
			if decodeOrderResponse(t, response).Status != order.StatusCancelled {
				t.Fatal("GET did not observe cancellation")
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, input := range []struct {
			id     string
			status int
		}{{"9999999", 404}, {"0", 400}, {"abc", 400}, {"-1", 400}} {
			path := "/orders/" + input.id
			if method == http.MethodPost {
				path += "/cancel"
			}
			assertHTTPStatus(t, performRequest(router, method, path), input.status)
		}
	}
	// Product edits must not alter the stored order snapshot.
	if _, err := db.Exec(`UPDATE products SET name='Changed A',sku='CHANGED-A',price_cents=9999 WHERE id=?`, a); err != nil {
		t.Fatal(err)
	}
	response := performRequest(router, http.MethodGet, "/orders/1")
	assertHTTPStatus(t, response, 200)
	item := decodeOrderResponse(t, response).Items[0]
	if item.Name != "Original A" || item.SKU != "LIFE-A" || item.UnitPriceCents != 1000 {
		t.Fatalf("snapshot changed=%+v", item)
	}
}

func TestCancellationFailureRollbackWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	a := insertProductWithInventory(t, context.Background(), db, "FAIL-A", "First", 1000, "ACTIVE", 10)
	b := insertProductWithInventory(t, context.Background(), db, "FAIL-B", "Second", 2000, "ACTIVE", 20)
	router := newOrderRouter(t, db)
	created := createLifecycleOrder(t, router, "cancel-failure", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2},{"product_id":%d,"quantity":3}]}`, a, b))
	path := fmt.Sprintf("/orders/%d/cancel", created.ID)
	triggers := []struct{ name, sql string }{
		{"fail_cancel_state", `CREATE TRIGGER fail_cancel_state BEFORE UPDATE ON orders FOR EACH ROW BEGIN IF NEW.status='CANCELLED' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='controlled state failure'; END IF; END`},
		{"fail_cancel_inventory", fmt.Sprintf(`CREATE TRIGGER fail_cancel_inventory BEFORE UPDATE ON inventories FOR EACH ROW BEGIN IF NEW.product_id=%d AND NEW.available_qty>OLD.available_qty THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='controlled replenishment failure'; END IF; END`, b)},
	}
	for _, trigger := range triggers {
		t.Run(trigger.name, func(t *testing.T) {
			if _, err := db.Exec(trigger.sql); err != nil {
				t.Fatal(err)
			}
			assertHTTPStatus(t, performRequest(router, http.MethodPost, path), 500)
			assertInventory(t, db, a, 8)
			assertInventory(t, db, b, 17)
			assertLifecycleState(t, db, created.ID, order.StatusCreated, 2)
			if _, err := db.Exec("DROP TRIGGER " + trigger.name); err != nil {
				t.Fatal(err)
			}
		})
	}
	assertHTTPStatus(t, performRequest(router, http.MethodPost, path), 200)
	assertInventory(t, db, a, 10)
	assertInventory(t, db, b, 20)
	assertLifecycleState(t, db, created.ID, order.StatusCancelled, 2)
	t.Log("state and second-product SQL failure both rolled back; after removing triggers retry restored inventories 10/20")
}

func TestConcurrentCancellationWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	a := insertProductWithInventory(t, context.Background(), db, "CON-CANCEL-A", "A", 1000, "ACTIVE", 100)
	b := insertProductWithInventory(t, context.Background(), db, "CON-CANCEL-B", "B", 2000, "ACTIVE", 200)
	router := newOrderRouter(t, db)
	body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2},{"product_id":%d,"quantity":3}]}`, a, b)
	created := createLifecycleOrder(t, router, "concurrent-cancel", body)
	responses := concurrentCancellations(router, 20, func(int) uint64 { return created.ID })
	replays := 0
	for _, response := range responses {
		assertHTTPStatus(t, response, 200)
		if response.Header().Get("Cancellation-Replayed") == "true" {
			replays++
		}
		if decodeOrderResponse(t, response).Status != order.StatusCancelled {
			t.Fatal("wrong cancelled state")
		}
	}
	if replays != 19 {
		t.Fatalf("replays=%d want 19", replays)
	}
	assertInventory(t, db, a, 100)
	assertInventory(t, db, b, 200)
	assertLifecycleState(t, db, created.ID, order.StatusCancelled, 2)
	t.Logf("20 cancellations: 20 HTTP 200, %d replays; inventories 100/200; 1 order and 2 items", replays)
	// Distinct orders share both inventory rows; all cancellations use the same
	// product lock order and must collectively restore exactly the original stock.
	ids := make([]uint64, 10)
	for index := range ids {
		ids[index] = createLifecycleOrder(t, router, fmt.Sprintf("shared-cancel-%d", index), body).ID
	}
	responses = concurrentCancellations(router, len(ids), func(index int) uint64 { return ids[index] })
	for _, response := range responses {
		assertHTTPStatus(t, response, 200)
	}
	for _, id := range ids {
		assertLifecycleState(t, db, id, order.StatusCancelled, 2)
	}
	assertInventory(t, db, a, 100)
	assertInventory(t, db, b, 200)
	var headers, items int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM order_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if headers != 11 || items != 22 {
		t.Fatalf("orders=%d items=%d want 11/22", headers, items)
	}
	t.Log("10 distinct shared-stock orders cancelled concurrently: 11 total orders/22 items retained; stocks restored 100/200")
}

func TestCancellationOverflowAndContextWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	a := insertProductWithInventory(t, context.Background(), db, "OVERFLOW-A", "A", 1000, "ACTIVE", 10)
	router := newOrderRouter(t, db)
	created := createLifecycleOrder(t, router, "overflow-cancel", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, a))
	if _, err := db.Exec(`UPDATE inventories SET available_qty=4294967295 WHERE product_id=?`, a); err != nil {
		t.Fatal(err)
	}
	assertHTTPStatus(t, performRequest(router, http.MethodPost, fmt.Sprintf("/orders/%d/cancel", created.ID)), 409)
	assertLifecycleState(t, db, created.ID, order.StatusCreated, 1)
	assertInventory(t, db, a, 4294967295)
	if _, err := db.Exec(`UPDATE inventories SET available_qty=8 WHERE product_id=?`, a); err != nil {
		t.Fatal(err)
	}
	// Hold the target order row to force cancellation to respect its context.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE orders SET status=status WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = mysqlstore.NewOrderStore(db, time.Second).Cancel(ctx, 1, created.ID)
	if err == nil {
		t.Fatal("blocked cancellation did not fail with expired context")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("context=%v error=%v", ctx.Err(), err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertLifecycleState(t, db, created.ID, order.StatusCreated, 1)
	assertInventory(t, db, a, 8)
	assertHTTPStatus(t, performRequest(router, http.MethodPost, fmt.Sprintf("/orders/%d/cancel", created.ID)), 200)
	assertInventory(t, db, a, 10)
}

func TestOrderDetailReadConsistencyAndFailureWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	a := insertProductWithInventory(t, context.Background(), db, "READ-A", "Before", 1000, "ACTIVE", 20)
	router := newOrderRouter(t, db)
	created := createLifecycleOrder(t, router, "consistent-read", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, a))
	// In this disposable test only, alternate an entire valid header+snapshot
	// pair in one transaction. A reader mixing statement snapshots would return
	// a mismatched total or an inconsistent name/price pair.
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for index := 0; index < 40; index++ {
			tx, err := db.Begin()
			if err != nil {
				done <- err
				return
			}
			price := 1000 + index%2*1000
			if _, err = tx.Exec(`UPDATE orders SET total_amount_cents=? WHERE id=?`, price*2, created.ID); err == nil {
				_, err = tx.Exec(`UPDATE order_items SET unit_price_cents=?,name_snapshot=? WHERE order_id=?`, price, fmt.Sprintf("Price-%d", price), created.ID)
			}
			if err != nil {
				tx.Rollback()
				done <- err
				return
			}
			if err = tx.Commit(); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	for index := 0; index < 80; index++ {
		response := performRequest(router, http.MethodGet, fmt.Sprintf("/orders/%d", created.ID))
		assertHTTPStatus(t, response, 200)
		found := decodeOrderResponse(t, response)
		assertSnapshotsAndTotals(t, found)
		if found.Items[0].Name != "Before" && found.Items[0].Name != fmt.Sprintf("Price-%d", found.Items[0].UnitPriceCents) {
			t.Fatalf("mixed snapshot=%+v", found)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Drop neither schema nor data: table rename creates a reversible SQL fault
	// only inside this isolated database.
	if _, err := db.Exec(`RENAME TABLE order_items TO order_items_unavailable`); err != nil {
		t.Fatal(err)
	}
	response := performRequest(router, http.MethodGet, fmt.Sprintf("/orders/%d", created.ID))
	assertHTTPStatus(t, response, 500)
	if strings.Contains(response.Body.String(), "order_items") || strings.Contains(response.Body.String(), "1146") {
		t.Fatal("internal SQL error leaked")
	}
	if _, err := db.Exec(`RENAME TABLE order_items_unavailable TO order_items`); err != nil {
		t.Fatal(err)
	}
	assertHTTPStatus(t, performRequest(router, http.MethodGet, fmt.Sprintf("/orders/%d", created.ID)), 200)
}

func createLifecycleOrder(t *testing.T, handler http.Handler, key, body string) order.Order {
	t.Helper()
	response := postOrder(handler, key, body)
	assertHTTPStatus(t, response, 201)
	return decodeOrderResponse(t, response)
}

func assertHTTPStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatal(statusFailureMessage(response, want))
	}
}

// Shared with login assertions: even an unexpected successful response can
// contain a valid session token. Never dump its body into persisted CI logs.
func statusFailureMessage(response *httptest.ResponseRecorder, want int) string {
	return fmt.Sprintf("status=%d want=%d (response body intentionally omitted)", response.Code, want)
}

func assertSnapshotsAndTotals(t *testing.T, found order.Order) {
	t.Helper()
	var sum uint64
	for index, item := range found.Items {
		if index > 0 && item.ProductID <= found.Items[index-1].ProductID {
			t.Fatal("items not ordered by product ID")
		}
		if item.SubtotalCents != item.UnitPriceCents*uint64(item.Quantity) {
			t.Fatal("invalid subtotal")
		}
		sum += item.SubtotalCents
	}
	if sum != found.TotalAmountCents {
		t.Fatalf("sum=%d header=%d", sum, found.TotalAmountCents)
	}
}

func assertLifecycleState(t *testing.T, db *sql.DB, id uint64, status string, itemCount int) {
	t.Helper()
	var gotStatus string
	var gotItems int
	if err := db.QueryRow(`SELECT o.status,COUNT(oi.product_id) FROM orders o LEFT JOIN order_items oi ON oi.order_id=o.id WHERE o.id=? GROUP BY o.id,o.status`, id).Scan(&gotStatus, &gotItems); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotItems != itemCount {
		t.Fatalf("order=%d status=%s items=%d want=%s/%d", id, gotStatus, gotItems, status, itemCount)
	}
}

func concurrentCancellations(handler http.Handler, count int, id func(int) uint64) []*httptest.ResponseRecorder {
	responses := make([]*httptest.ResponseRecorder, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			responses[index] = performRequest(handler, http.MethodPost, fmt.Sprintf("/orders/%d/cancel", id(index)))
		}(index)
	}
	close(start)
	wait.Wait()
	return responses
}
