//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pressly/goose/v3"

	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/httpapi"
	"internshiplab/order-api/internal/order"
	"internshiplab/order-api/internal/product"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

type authenticatedClient struct {
	router             http.Handler
	token              string
	userID             uint64
	username, password string
}

func newAuthenticatedRouter(t *testing.T, db *sql.DB) http.Handler {
	t.Helper()
	service, err := auth.NewService(mysqlstore.NewAuthStore(db, 3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	return httpapi.NewRouter(product.NewHandler(product.NewService(mysqlstore.NewProductStore(db, 3*time.Second))), order.NewHandler(order.NewService(mysqlstore.NewOrderStore(db, 10*time.Second))), service)
}
func registerClient(t *testing.T, router http.Handler) authenticatedClient {
	t.Helper()
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "user_" + hex.EncodeToString(random[:8])
	password := hex.EncodeToString(random) + "!"
	body, _ := json.Marshal(map[string]string{"username": name, "password": password})
	response := authRequest(router, "/auth/register", string(body), "")
	assertHTTPStatus(t, response, 201)
	response = authRequest(router, "/auth/login", string(body), "")
	assertHTTPStatus(t, response, 200)
	var result struct {
		Data auth.LoginResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.AccessToken == "" {
		t.Fatal("login did not issue token")
	}
	return authenticatedClient{router: router, token: result.Data.AccessToken, userID: result.Data.User.ID, username: name, password: password}
}

type authorizedTransport struct {
	router http.Handler
	token  string
}

func (c authorizedTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+c.token)
	c.router.ServeHTTP(w, copy)
}

// A test client attaches a real API-issued token. Authentication middleware is
// always executed; there is no test-mode bypass or fake identity injection.
func (c authenticatedClient) handler() http.Handler { return authorizedTransport{c.router, c.token} }
func authRequest(router http.Handler, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestAuthenticationWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	router := newAuthenticatedRouter(t, db)
	a := registerClient(t, router)
	var stored string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id=?`, a.userID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == a.password || !strings.HasPrefix(stored, "$argon2id$") || !auth.VerifyPassword(a.password, stored) {
		t.Fatal("password storage invalid")
	}
	body, _ := json.Marshal(map[string]string{"username": " " + strings.ToUpper(a.username) + " ", "password": a.password})
	duplicate := authRequest(router, "/auth/register", string(body), "")
	assertHTTPStatus(t, duplicate, 409)
	if strings.Contains(duplicate.Body.String(), a.username) {
		t.Fatal("registration disclosed username")
	}
	assertHTTPStatus(t, authRequest(router, "/auth/login", string(body), ""), 200)
	wrong, _ := json.Marshal(map[string]string{"username": a.username, "password": a.password + "wrong"})
	missing, _ := json.Marshal(map[string]string{"username": "nonexistent_user", "password": a.password + "wrong"})
	r1 := authRequest(router, "/auth/login", string(wrong), "")
	r2 := authRequest(router, "/auth/login", string(missing), "")
	assertHTTPStatus(t, r1, 401)
	assertHTTPStatus(t, r2, 401)
	if r1.Body.String() != r2.Body.String() {
		t.Fatal("missing and wrong password errors differ")
	}
	assertHTTPStatus(t, performRequest(a.handler(), http.MethodGet, "/auth/me"), 200)
	assertHTTPStatus(t, authRequest(router, "/auth/logout", "", a.token), 200)
	assertHTTPStatus(t, performRequest(a.handler(), http.MethodGet, "/auth/me"), 401)
	for _, action := range []string{"expired", "disabled"} {
		client := registerClient(t, router)
		if action == "expired" {
			if _, err := db.Exec(`UPDATE auth_sessions SET created_at=UTC_TIMESTAMP(6)-INTERVAL 2 DAY,expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 DAY WHERE user_id=?`, client.userID); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := db.Exec(`UPDATE users SET status='DISABLED' WHERE id=?`, client.userID); err != nil {
				t.Fatal(err)
			}
		}
		assertHTTPStatus(t, performRequest(client.handler(), http.MethodGet, "/auth/me"), 401)
	}
	invalid := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	invalid.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 43))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, invalid)
	assertHTTPStatus(t, response, 401)
	var hash []byte
	if err := db.QueryRow(`SELECT token_hash FROM auth_sessions WHERE user_id=? LIMIT 1`, a.userID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(a.token))
	if hex.EncodeToString(hash) != hex.EncodeToString(want[:]) {
		t.Fatal("token hash mismatch")
	}
	if strings.Contains(string(hash), a.token) {
		t.Fatal("plaintext token in session")
	}
	if _, err := db.Exec(`INSERT INTO users(username,password_hash,status) VALUES ('UPPER_NAME','not-a-real-hash','ACTIVE')`); err == nil {
		t.Fatal("database accepted non-normalized username")
	}
	if _, err := db.Exec(`INSERT INTO auth_sessions(user_id,token_hash,expires_at) VALUES (999999,?,UTC_TIMESTAMP(6)+INTERVAL 1 DAY)`, make([]byte, 32)); err == nil {
		t.Fatal("session without valid user accepted")
	}
}

func TestOrderOwnershipAndUserIdempotencyWithMySQL(t *testing.T) {
	db := openMigratedTestDatabase(t)
	router := newAuthenticatedRouter(t, db)
	a, b := registerClient(t, router), registerClient(t, router)
	productID := insertProductWithInventory(t, context.Background(), db, "AUTH-STOCK", "Scoped stock", 500, "ACTIVE", 50)
	body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":2}]}`, productID)
	created := createLifecycleOrder(t, a.handler(), "shared-key", body)
	spoofed := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(strings.TrimSuffix(body, "}")+fmt.Sprintf(`,"user_id":%d}`, b.userID)))
	spoofed.Header.Set("Authorization", "Bearer "+a.token)
	spoofed.Header.Set("Content-Type", "application/json")
	spoofed.Header.Set("Idempotency-Key", "spoof-user")
	spoofResponse := httptest.NewRecorder()
	router.ServeHTTP(spoofResponse, spoofed)
	assertHTTPStatus(t, spoofResponse, 400)
	path := fmt.Sprintf("/orders/%d", created.ID)
	assertHTTPStatus(t, performRequest(a.handler(), http.MethodGet, path), 200)
	assertHTTPStatus(t, performRequest(b.handler(), http.MethodGet, path), 404)
	spoofHeader := httptest.NewRequest(http.MethodGet, path, nil)
	spoofHeader.Header.Set("Authorization", "Bearer "+b.token)
	spoofHeader.Header.Set("X-User-ID", fmt.Sprint(a.userID))
	spoofResponse = httptest.NewRecorder()
	router.ServeHTTP(spoofResponse, spoofHeader)
	assertHTTPStatus(t, spoofResponse, 404)
	assertHTTPStatus(t, performRequest(b.handler(), http.MethodPost, path+"/cancel"), 404)
	assertLifecycleState(t, db, created.ID, order.StatusCreated, 1)
	assertInventory(t, db, productID, 48)
	assertHTTPStatus(t, postOrder(a.handler(), "shared-key", body), 200)
	assertHTTPStatus(t, postOrder(a.handler(), "shared-key", fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":3}]}`, productID)), 409)
	other := createLifecycleOrder(t, b.handler(), "shared-key", body)
	if other.ID == created.ID {
		t.Fatal("cross-user replay")
	}
	for _, endpoint := range []struct{ method, path string }{{"POST", "/orders"}, {"GET", path}, {"POST", path + "/cancel"}} {
		assertHTTPStatus(t, performRequest(router, endpoint.method, endpoint.path), 401)
	}
	// Legacy orders remain NULL and cannot be claimed using a matching key.
	legacy, err := db.Exec(`INSERT INTO orders(order_no,idempotency_key,request_hash,status,total_amount_cents) VALUES ('LEGACY',?,?,'CREATED',500)`, []byte("shared-key"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, _ := legacy.LastInsertId()
	for _, client := range []authenticatedClient{a, b} {
		assertHTTPStatus(t, performRequest(client.handler(), http.MethodGet, fmt.Sprintf("/orders/%d", legacyID)), 404)
		assertHTTPStatus(t, performRequest(client.handler(), http.MethodPost, fmt.Sprintf("/orders/%d/cancel", legacyID)), 404)
	}
	assertHTTPStatus(t, performRequest(a.handler(), http.MethodPost, path+"/cancel"), 200)
	assertInventory(t, db, productID, 48)
	// Both users race on the same key; each user must have exactly one winner.
	clients := []authenticatedClient{a, b}
	responses := runConcurrentOrders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }), 12, func(index int) (string, string) { return "unused", body })
	// Requests above intentionally lack credentials: none may mutate stock.
	for _, response := range responses {
		assertHTTPStatus(t, response, 401)
	}
	responses = make([]*httptest.ResponseRecorder, 12)
	done := make(chan int, 12)
	for index := range responses {
		go func(index int) {
			responses[index] = postOrder(clients[index%2].handler(), "concurrent-shared-key", body)
			done <- index
		}(index)
	}
	for range responses {
		<-done
	}
	ids := map[uint64]uint64{}
	for index, response := range responses {
		if response.Code != 201 && response.Code != 200 {
			t.Fatalf("concurrent response=%d", response.Code)
		}
		id := decodeOrderResponse(t, response).ID
		uid := clients[index%2].userID
		if prior, ok := ids[uid]; ok && prior != id {
			t.Fatal("same-user duplicate orders")
		}
		ids[uid] = id
	}
	if ids[a.userID] == ids[b.userID] {
		t.Fatal("cross-user response leak")
	}
	var count, quantity int
	if err := db.QueryRow(`SELECT COUNT(*),SUM(oi.quantity) FROM orders o JOIN order_items oi ON oi.order_id=o.id WHERE o.idempotency_key=?`, []byte("concurrent-shared-key")).Scan(&count, &quantity); err != nil {
		t.Fatal(err)
	}
	if count != 2 || quantity != 4 {
		t.Fatalf("orders=%d quantity=%d", count, quantity)
	}
	for uid, id := range ids {
		var owner uint64
		if err := db.QueryRow(`SELECT user_id FROM orders WHERE id=?`, id).Scan(&owner); err != nil || owner != uid {
			t.Fatal("wrong owner")
		}
	}
	assertInventory(t, db, productID, 44)
	t.Log("A/B same concurrent key: 2 owned orders, 4 units deducted; unauthorized requests and cross-user operations changed nothing")
}

func TestAuthMigrationCompatibility(t *testing.T) {
	admin, cfg := openAdminDatabase(t)
	name := uniqueDatabaseName(t)
	createIsolatedDatabase(t, admin, name)
	t.Cleanup(func() { dropIsolatedDatabase(t, admin, name); admin.Close() })
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = goose.SetDialect("mysql"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpTo(db, migrationDirectory(t), 4); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO orders(order_no,idempotency_key,request_hash,status,total_amount_cents) VALUES ('BEFORE-AUTH','old-key',?,'CREATED',1000)`, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(db, migrationDirectory(t)); err != nil {
		t.Fatal(err)
	}
	var owner sql.NullInt64
	var amount int
	if err = db.QueryRow(`SELECT user_id,total_amount_cents FROM orders WHERE order_no='BEFORE-AUTH'`).Scan(&owner, &amount); err != nil || owner.Valid || amount != 1000 {
		t.Fatal("legacy data changed")
	}
	if _, err = db.Exec(`INSERT INTO orders(user_id,order_no,idempotency_key,request_hash,status,total_amount_cents) VALUES (999999,'INVALID-OWNER','new-key',?,'CREATED',1000)`, make([]byte, 32)); err == nil {
		t.Fatal("invalid owner accepted")
	}
	if err = goose.DownTo(db, migrationDirectory(t), 4); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(db, migrationDirectory(t)); err != nil {
		t.Fatal(err)
	}
	// Old global uniqueness cannot represent valid cross-user duplicate keys.
	// Down must refuse atomically and retain both orders rather than lose data.
	router := newAuthenticatedRouter(t, db)
	a, b := registerClient(t, router), registerClient(t, router)
	p := insertProductWithInventory(t, context.Background(), db, "DOWN-COMPAT", "Test", 1000, "ACTIVE", 10)
	body := fmt.Sprintf(`{"items":[{"product_id":%d,"quantity":1}]}`, p)
	createLifecycleOrder(t, a.handler(), "same-user-key", body)
	createLifecycleOrder(t, b.handler(), "same-user-key", body)
	if err = goose.Down(db, migrationDirectory(t)); err == nil {
		t.Fatal("unsafe rollback unexpectedly succeeded")
	}
	var owned int
	if err = db.QueryRow(`SELECT COUNT(*) FROM orders WHERE user_id IS NOT NULL`).Scan(&owned); err != nil || owned != 2 {
		t.Fatal("failed down changed data")
	}
}
