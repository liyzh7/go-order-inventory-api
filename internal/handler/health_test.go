package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"internshiplab/order-api/internal/handler"
)

func TestGetHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler.RegisterHealthRoutes(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response handler.HealthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if response.Status != "ok" {
		t.Errorf("status = %q, want %q", response.Status, "ok")
	}
}
