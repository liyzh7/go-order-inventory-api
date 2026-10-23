package order

import (
	"context"
	"errors"
	"internshiplab/order-api/internal/auth"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type handlerServiceStub struct {
	result  CreateResult
	err     error
	key     string
	request CreateRequest
}

func (s *handlerServiceStub) Get(_ context.Context, id uint64) (Order, error) {
	return Order{ID: id, Status: StatusCreated}, s.err
}

func (s *handlerServiceStub) Cancel(_ context.Context, id uint64) (CancelResult, error) {
	return CancelResult{Order: Order{ID: id, Status: StatusCancelled}, Replayed: s.result.Replayed}, s.err
}

func TestLifecycleHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, id := range []string{"0", "-1", "abc", "18446744073709551616"} {
			router := gin.New()
			NewHandler(&handlerServiceStub{}).RegisterRoutes(router, auth.Middleware(unitAuthenticator{}))
			path := "/orders/" + id
			if method == http.MethodPost {
				path += "/cancel"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, unitRequest(method, path, nil))
			if response.Code != http.StatusBadRequest {
				t.Errorf("%s %s: %d", method, path, response.Code)
			}
		}
	}
	for _, failure := range []struct {
		err    error
		status int
	}{
		{ErrOrderNotFound, 404}, {ErrInvalidState, 409}, {ErrInventoryOverflow, 409},
		{ErrCommitUncertain, 503}, {context.Canceled, 504}, {errors.New("private SQL secret"), 500},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			router := gin.New()
			NewHandler(&handlerServiceStub{err: failure.err}).RegisterRoutes(router, auth.Middleware(unitAuthenticator{}))
			path := "/orders/1"
			if method == http.MethodPost {
				path += "/cancel"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, unitRequest(method, path, nil))
			if response.Code != failure.status || strings.Contains(response.Body.String(), "private SQL secret") {
				t.Errorf("%s: status=%d body=%s", method, response.Code, response.Body)
			}
		}
	}
	router := gin.New()
	NewHandler(&handlerServiceStub{result: CreateResult{Replayed: true}}).RegisterRoutes(router, auth.Middleware(unitAuthenticator{}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, unitRequest(http.MethodPost, "/orders/1/cancel", nil))
	if response.Code != 200 || response.Header().Get("Cancellation-Replayed") != "true" {
		t.Fatalf("repeat cancel=%d %s", response.Code, response.Body)
	}
}

func (s *handlerServiceStub) Create(_ context.Context, key string, request CreateRequest) (CreateResult, error) {
	s.key = key
	s.request = request
	return s.result, s.err
}

func TestHandlerCreateAndReplay(t *testing.T) {
	tests := []struct {
		name       string
		replayed   bool
		wantStatus int
		wantHeader string
	}{
		{name: "created", wantStatus: http.StatusCreated},
		{name: "replayed", replayed: true, wantStatus: http.StatusOK, wantHeader: "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &handlerServiceStub{result: CreateResult{Order: Order{ID: 42}, Replayed: tt.replayed}}
			response := orderRequest(t, service, `{"items":[{"product_id":2,"quantity":3}]}`, "request-1")
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
			if got := response.Header().Get("Idempotency-Replayed"); got != tt.wantHeader {
				t.Errorf("replay header = %q, want %q", got, tt.wantHeader)
			}
			if service.key != "request-1" || len(service.request.Items) != 1 {
				t.Errorf("service input = key %q request %#v", service.key, service.request)
			}
		})
	}
}

func TestHandlerRejectsMalformedRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		key         string
		contentType string
		wantStatus  int
	}{
		{name: "missing content type", body: `{}`, key: "key-1", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing key", body: `{}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"items":[],"price":1}`, key: "key-1", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "trailing object", body: `{"items":[]} {}`, key: "key-1", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "invalid json", body: `{"items":`, key: "key-1", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"items":[]}` + strings.Repeat(" ", MaxRequestBytes), key: "key-1", contentType: "application/json", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &handlerServiceStub{}
			response := performHandlerRequest(service, tt.body, tt.key, tt.contentType)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
			if service.key != "" {
				t.Error("service was called for malformed HTTP input")
			}
		})
	}
}

func TestHandlerMapsDomainErrors(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
	}{
		{ErrInvalidIdempotency, http.StatusBadRequest},
		{ErrInvalidArgument, http.StatusBadRequest},
		{ErrAmountOverflow, http.StatusBadRequest},
		{ErrProductNotFound, http.StatusNotFound},
		{ErrProductInactive, http.StatusConflict},
		{ErrInsufficientStock, http.StatusConflict},
		{ErrIdempotencyConflict, http.StatusConflict},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{ErrDatabaseUnavailable, http.StatusServiceUnavailable},
		{errors.New("unexpected"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		service := &handlerServiceStub{err: tt.err}
		response := orderRequest(t, service, `{"items":[{"product_id":1,"quantity":1}]}`, "key-1")
		if response.Code != tt.wantStatus {
			t.Errorf("error %v: status = %d, want %d", tt.err, response.Code, tt.wantStatus)
		}
	}
}

func orderRequest(t *testing.T, service orderService, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return performHandlerRequest(service, body, key, "application/json")
}

func performHandlerRequest(service orderService, body, key, contentType string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(service).RegisterRoutes(router, auth.Middleware(unitAuthenticator{}))
	request := unitRequest(http.MethodPost, "/orders", strings.NewReader(body))
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type unitAuthenticator struct{}

func (unitAuthenticator) Authenticate(context.Context, string) (auth.Identity, error) {
	return auth.Identity{UserID: 1, SessionID: 1}, nil
}
func unitRequest(method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer unit-mock-token")
	return request
}
