package securitylog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAllowlistedMetadata(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(NewLogger(&output))
	defer slog.SetDefault(previous)
	secret := "private-password-token-hash\r\nINJECTED"
	ctx, err := NewContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUser(ctx, 9)
	Log(ctx, secret, secret, 401, UserID(ctx), secret)
	LogCleanup(ctx, "success", "none", 3, 2*time.Second)
	if strings.Contains(output.String(), "private-password") || strings.Contains(output.String(), "INJECTED") {
		t.Fatal("untrusted metadata escaped allowlist")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines=%d", len(lines))
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"time", "request_id", "event", "outcome", "http_status", "user_id", "error_category"} {
			if _, ok := event[field]; !ok {
				t.Fatalf("missing %s", field)
			}
		}
		if event["request_id"] != RequestID(ctx) {
			t.Fatal("correlation ID changed")
		}
	}
}

func TestHTTPDoesNotLogUntrustedRequestsOrPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(NewLogger(&output))
	defer slog.SetDefault(previous)
	secret := "do-not-log-password-token-or-header"
	router := gin.New()
	router.Use(HTTP())
	router.POST("/panic/:value", func(c *gin.Context) { panic(secret) })
	request := httptest.NewRequest(http.MethodPost, "/panic/"+secret+"?password="+secret, strings.NewReader(secret))
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("X-Request-ID", secret+"\r\nforged")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 500 {
		t.Fatalf("status=%d", response.Code)
	}
	if len(response.Header().Get("X-Request-ID")) != 32 {
		t.Fatal("server request ID missing")
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "forged") {
		t.Fatal("request or panic secret logged")
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event["event"] != "http.panic" || event["http_status"] != float64(500) {
		t.Fatal("wrong recovery event")
	}
}

func TestFailureLogBudget(t *testing.T) {
	var limit budget
	now := time.Now()
	for count := 1; count <= 100; count++ {
		emit, suppressed := limit.allow(now)
		if emit != (count <= 31) || suppressed != (count == 31) {
			t.Fatalf("count=%d emit=%v suppressed=%v", count, emit, suppressed)
		}
	}
	if emit, suppressed := limit.allow(now.Add(time.Minute)); !emit || suppressed {
		t.Fatal("budget did not reset")
	}
}
