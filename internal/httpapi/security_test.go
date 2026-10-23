package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/order"
	"internshiplab/order-api/internal/platform/securitylog"
	"internshiplab/order-api/internal/product"
)

type securityAuthStore struct {
	failure error
	panics  bool
}

func (s securityAuthStore) CreateUser(context.Context, string, string) (auth.User, error) {
	if s.panics {
		panic(s.failure)
	}
	return auth.User{ID: 9, Username: "safe_name"}, s.failure
}
func (s securityAuthStore) FindUser(context.Context, string) (auth.User, error) {
	return auth.User{}, s.failure
}
func (s securityAuthStore) CreateSession(context.Context, uint64, [32]byte, time.Time) error {
	return s.failure
}
func (s securityAuthStore) FindSession(context.Context, [32]byte) (auth.Identity, error) {
	return auth.Identity{UserID: 9, SessionID: 7}, nil
}
func (s securityAuthStore) RevokeSession(context.Context, uint64, uint64) error { return s.failure }

type securityOrderService struct {
	panics bool
	secret error
}

func (s securityOrderService) Create(context.Context, string, order.CreateRequest) (order.CreateResult, error) {
	return order.CreateResult{}, s.secret
}
func (s securityOrderService) Get(context.Context, uint64) (order.Order, error) {
	return order.Order{}, order.ErrOrderNotFound
}
func (s securityOrderService) Cancel(context.Context, uint64) (order.CancelResult, error) {
	if s.panics {
		panic(s.secret)
	}
	return order.CancelResult{Order: order.Order{ID: 1, Status: order.StatusCancelled}}, nil
}

type securityProductService struct{}

func (securityProductService) List(context.Context, product.ListParams) (product.ListResult, error) {
	return product.ListResult{}, nil
}
func (securityProductService) Get(context.Context, uint64) (product.Product, error) {
	return product.Product{}, nil
}

func TestSecurityEventsNeverContainRequestOrDatabaseSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "test-secret-password-token-tokenhash-sql\r\nforged-event"
	service, err := auth.NewService(securityAuthStore{failure: errors.New(secret)})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(product.NewHandler(securityProductService{}), order.NewHandler(securityOrderService{secret: errors.New(secret)}), service)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(securitylog.NewLogger(&output))
	defer slog.SetDefault(previous)
	requests := []struct{ method, path, body, token string }{
		{"POST", "/auth/register", `{"username":"safe_name","password":"` + "long-private-password" + `"}`, ""},
		{"POST", "/auth/login", `{"username":"safe_name","password":"long-private-password"}`, ""},
		{"GET", "/orders/987654?secret=" + "query-private-secret", "", strings.Repeat("A", 43)},
		{"POST", "/orders/1/cancel", "", strings.Repeat("A", 43)},
		{"POST", "/auth/logout", "", strings.Repeat("A", 43)},
		{"GET", "/orders/987654", "", "not-a-valid-token"},
	}
	for _, item := range requests {
		request := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", "untrusted-request-id")
		request.Header.Set("Authorization", "Bearer "+item.token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Header().Get("X-Request-ID") == "untrusted-request-id" {
			t.Fatal("trusted caller supplied request ID")
		}
	}
	text := output.String()
	for _, private := range []string{"test-secret", "forged-event", "long-private-password", "query-private-secret", "untrusted-request-id", "not-a-valid-token", strings.Repeat("A", 43), "987654", "safe_name", "Authorization"} {
		if strings.Contains(text, private) {
			t.Fatalf("sensitive value logged: %q", private)
		}
	}
	events := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		events[record["event"].(string)] = true
		if record["event"] == "order.access_denied" && record["user_id"] != float64(9) {
			t.Fatal("missing authenticated actor")
		}
	}
	for _, event := range []string{"auth.register", "auth.login", "auth.logout", "auth.token", "order.access_denied", "order.cancel"} {
		if !events[event] {
			t.Fatalf("missing event %s", event)
		}
	}
}

func TestPanicDoesNotRecordSuccessfulBusinessEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scope := range []string{"auth.register", "order.cancel"} {
		service, err := auth.NewService(securityAuthStore{failure: errors.New("panic-private-secret"), panics: true})
		if err != nil {
			t.Fatal(err)
		}
		router := NewRouter(product.NewHandler(securityProductService{}), order.NewHandler(securityOrderService{panics: true, secret: errors.New("panic-private-secret")}), service)
		var output bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(securitylog.NewLogger(&output))
		path, body := "/auth/register", `{"username":"panic_user","password":"long-private-password"}`
		if scope == "order.cancel" {
			path = "/orders/1/cancel"
			body = ""
		}
		request := httptest.NewRequest("POST", path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		slog.SetDefault(previous)
		if response.Code != http.StatusInternalServerError {
			t.Fatal("panic did not return 500")
		}
		if strings.Contains(output.String(), "panic-private-secret") || strings.Contains(output.String(), `"outcome":"success"`) {
			t.Fatal("panic leaked or reported success")
		}
	}
}
