package product

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestListHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("returns products with default pagination", func(t *testing.T) {
		store := &fakeStore{listFn: func(_ context.Context, afterID uint64, limit int) ([]Product, error) {
			if afterID != 0 || limit != DefaultPageSize+1 {
				t.Fatalf("store args = (%d, %d), want (0, %d)", afterID, limit, DefaultPageSize+1)
			}
			return []Product{{ID: 1, SKU: "BOOK-001", Name: "Book", PriceCents: 2999, AvailableQuantity: 8}}, nil
		}}

		response := performRequest(newProductRouter(store), http.MethodGet, "/products")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}

		var body struct {
			Data listData `json:"data"`
		}
		decodeJSON(t, response, &body)
		if len(body.Data.Items) != 1 || body.Data.Items[0].SKU != "BOOK-001" {
			t.Errorf("items = %+v, want BOOK-001", body.Data.Items)
		}
		if body.Data.Pagination.Limit != DefaultPageSize || body.Data.Pagination.AfterID != 0 {
			t.Errorf("pagination = %+v, want default pagination", body.Data.Pagination)
		}
	})

	t.Run("returns an empty JSON array", func(t *testing.T) {
		store := &fakeStore{listFn: func(context.Context, uint64, int) ([]Product, error) {
			return nil, nil
		}}

		response := performRequest(newProductRouter(store), http.MethodGet, "/products")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var body struct {
			Data listData `json:"data"`
		}
		decodeJSON(t, response, &body)
		if body.Data.Items == nil || len(body.Data.Items) != 0 {
			t.Errorf("items = %#v, want a non-nil empty slice", body.Data.Items)
		}
	})

	t.Run("applies cursor and reports next cursor", func(t *testing.T) {
		store := &fakeStore{listFn: func(_ context.Context, afterID uint64, limit int) ([]Product, error) {
			if afterID != 7 || limit != 2 {
				t.Fatalf("store args = (%d, %d), want (7, 2)", afterID, limit)
			}
			return []Product{{ID: 8}, {ID: 12}}, nil
		}}

		response := performRequest(newProductRouter(store), http.MethodGet, "/products?limit=1&after_id=7")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var body struct {
			Data listData `json:"data"`
		}
		decodeJSON(t, response, &body)
		if len(body.Data.Items) != 1 || body.Data.Items[0].ID != 8 {
			t.Errorf("items = %+v, want only id 8", body.Data.Items)
		}
		if !body.Data.Pagination.HasMore || body.Data.Pagination.NextAfterID == nil || *body.Data.Pagination.NextAfterID != 8 {
			t.Errorf("pagination = %+v, want next cursor 8", body.Data.Pagination)
		}
	})

	invalidCases := []string{
		"/products?limit=0",
		"/products?limit=101",
		"/products?limit=abc",
		"/products?after_id=-1",
		"/products?after_id=abc",
	}
	for _, target := range invalidCases {
		t.Run("rejects_"+target, func(t *testing.T) {
			response := performRequest(newProductRouter(&fakeStore{}), http.MethodGet, target)
			assertAPIError(t, response, http.StatusBadRequest, "invalid_argument")
		})
	}

	t.Run("hides store failure", func(t *testing.T) {
		const privateDetail = "secret-database-host"
		store := &fakeStore{listFn: func(context.Context, uint64, int) ([]Product, error) {
			return nil, errors.New("dial tcp: " + privateDetail)
		}}
		response := performRequest(newProductRouter(store), http.MethodGet, "/products")
		assertAPIError(t, response, http.StatusInternalServerError, "internal_error")
		if strings.Contains(response.Body.String(), privateDetail) {
			t.Errorf("response leaked private database detail: %s", response.Body.String())
		}
	})
}

func TestGetHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("returns product detail", func(t *testing.T) {
		wanted := Product{ID: 5, SKU: "MOUSE-001", Name: "Mouse", PriceCents: 8900, AvailableQuantity: 4}
		store := &fakeStore{getFn: func(_ context.Context, id uint64) (Product, error) {
			if id != 5 {
				t.Fatalf("store id = %d, want 5", id)
			}
			return wanted, nil
		}}

		response := performRequest(newProductRouter(store), http.MethodGet, "/products/5")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var body struct {
			Data Product `json:"data"`
		}
		decodeJSON(t, response, &body)
		if body.Data != wanted {
			t.Errorf("data = %+v, want %+v", body.Data, wanted)
		}
	})

	for _, id := range []string{"abc", "0", "-1"} {
		t.Run("rejects id "+id, func(t *testing.T) {
			response := performRequest(newProductRouter(&fakeStore{}), http.MethodGet, "/products/"+id)
			assertAPIError(t, response, http.StatusBadRequest, "invalid_argument")
		})
	}

	t.Run("maps missing product to 404", func(t *testing.T) {
		store := &fakeStore{getFn: func(context.Context, uint64) (Product, error) {
			return Product{}, ErrNotFound
		}}
		response := performRequest(newProductRouter(store), http.MethodGet, "/products/99")
		assertAPIError(t, response, http.StatusNotFound, "not_found")
	})

	t.Run("maps store failure to 500", func(t *testing.T) {
		store := &fakeStore{getFn: func(context.Context, uint64) (Product, error) {
			return Product{}, errors.New("connection refused")
		}}
		response := performRequest(newProductRouter(store), http.MethodGet, "/products/5")
		assertAPIError(t, response, http.StatusInternalServerError, "internal_error")
	})
}

func newProductRouter(store Store) http.Handler {
	service := NewService(store)
	handler := NewHandler(service)
	router := gin.New()
	handler.RegisterRoutes(router)
	return router
}

func performRequest(router http.Handler, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, nil)
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeJSON(t *testing.T, recorder *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode JSON: %v; body=%s", err, recorder.Body.String())
	}
}

func assertAPIError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var body errorResponse
	decodeJSON(t, recorder, &body)
	if body.Error.Code != wantCode {
		t.Errorf("error code = %q, want %q", body.Error.Code, wantCode)
	}
}
