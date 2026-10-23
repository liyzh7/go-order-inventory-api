package auth

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPasswordAndNormalization(t *testing.T) {
	password := "not-a-real-credential-123"
	a, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !VerifyPassword(password, a) || VerifyPassword("wrong-password", a) {
		t.Fatal("password hash or salt failure")
	}
	for _, invalid := range []string{"", "$argon2id$v=19$m=999999999,t=2,p=1$xxx$xxx", strings.Replace(a, "v=19", "v=16", 1)} {
		if VerifyPassword(password, invalid) {
			t.Fatal("malformed hash accepted")
		}
	}
	name, err := normalizeCredentials(" Alice_1 ", password)
	if err != nil || name != "alice_1" {
		t.Fatal(name, err)
	}
	for _, name := range []string{"ab", "bad name", "汉字用户", strings.Repeat("a", 33)} {
		if _, err := normalizeCredentials(name, password); err == nil {
			t.Fatal("invalid username")
		}
	}
	for _, pass := range []string{"short", strings.Repeat("a", 129)} {
		if _, err := normalizeCredentials("valid_user", pass); err == nil {
			t.Fatal("invalid password")
		}
	}
}

type validatorStub struct{ err error }

func (s validatorStub) Authenticate(context.Context, string) (Identity, error) {
	return Identity{UserID: 9, SessionID: 11}, s.err
}
func TestMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		value  string
		err    error
		status int
	}{{"", nil, 401}, {"Basic a", nil, 401}, {"Bearer  a", nil, 401}, {"Bearer token", ErrUnauthorized, 401}, {"Bearer token", nil, 200}} {
		router := gin.New()
		router.GET("/private", Middleware(validatorStub{test.err}), func(c *gin.Context) {
			id, ok := IdentityFromContext(c.Request.Context())
			if !ok || id.UserID != 9 {
				t.Error("identity missing")
			}
			c.Status(200)
		})
		request := httptest.NewRequest("GET", "/private", nil)
		request.Header.Set("Authorization", test.value)
		request.Header.Set("X-User-ID", "999")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatal(response.Code)
		}
	}
}
func TestLimiterBoundedAndConcurrent(t *testing.T) {
	limiter := attemptLimiter{entries: make(map[string]rateEntry)}
	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for index := 0; index < 100; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.allow("127.0.0.1", now) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 20 {
		t.Fatalf("allowed=%d", allowed)
	}
	if !limiter.allow("127.0.0.1", now.Add(time.Minute)) {
		t.Fatal("window did not reset")
	}
}
func TestStrictCredentialJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{"username":"test","password":"example-long-password","extra":1}`, `{} {}`, `{"password":`, strings.Repeat("x", 4097)} {
		router := gin.New()
		router.POST("/read", func(c *gin.Context) {
			if _, ok := readCredentials(c); ok {
				c.Status(200)
			}
		})
		request := httptest.NewRequest("POST", "/read", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("status=%d", response.Code)
		}
	}
	encoded, _ := json.Marshal(User{ID: 1, Username: "user", PasswordHash: "private", Status: "ACTIVE"})
	if strings.Contains(string(encoded), "private") {
		t.Fatal("hash exposed")
	}
}
func TestServiceUnauthorizedAndTokenFormat(t *testing.T) {
	service := &Service{}
	for _, token := range []string{"", strings.Repeat("a", 42), strings.Repeat("!", 43)} {
		if _, err := service.Authenticate(context.Background(), token); err != ErrUnauthorized {
			t.Fatal(err)
		}
	}
	if err := service.Logout(context.Background()); err != ErrUnauthorized {
		t.Fatal(err)
	}
	// An occupied bounded worker pool rejects instead of queuing unbounded KDF work.
	service.passwordSlots = make(chan struct{}, 1)
	service.passwordSlots <- struct{}{}
	if err := service.acquire(context.Background()); err != ErrBusy {
		t.Fatal(err)
	}
}

func TestHTTPThrottleIgnoresForwardedIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(nil).RegisterRoutes(router)
	for index := 0; index < 21; index++ {
		request := httptest.NewRequest("POST", "/auth/login", strings.NewReader("invalid"))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", strings.Repeat("1", index+1))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := 400
		if index == 20 {
			want = 429
		}
		if response.Code != want {
			t.Fatalf("request=%d status=%d", index, response.Code)
		}
	}
}
