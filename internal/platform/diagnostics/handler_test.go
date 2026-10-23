package diagnostics

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"
)

func TestRuntimeSnapshotContainsBoundedAggregateState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	db, err := sql.Open("mysql", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(7)
	Register(router, db, NewTransactionCollector(true))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/runtime", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CapturedAtUTC == "" || body.Runtime.Goroutines < 1 || body.Database.MaxOpenConnections != 7 {
		t.Fatalf("unexpected snapshot: %+v", body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("diagnostic response is cacheable")
	}
}

func TestTransactionDiagnosticRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	db, err := sql.Open("mysql", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	collector := NewTransactionCollector(true)
	collector.ObserveTransactionStage("commit", "success", time.Millisecond)
	Register(router, db, collector)

	reset := httptest.NewRecorder()
	router.ServeHTTP(reset, httptest.NewRequest(http.MethodPost, "/debug/transactions/reset", nil))
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d", reset.Code)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/transactions", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":true`) {
		t.Fatalf("snapshot status=%d body=%s", response.Code, response.Body.String())
	}
}
