package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"internshiplab/order-api/internal/platform/securitylog"
)

type Authenticator interface {
	Authenticate(context.Context, string) (Identity, error)
}

func Middleware(service Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		values := c.Request.Header.Values("Authorization")
		if len(values) != 1 {
			unauthorized(c)
			return
		}
		parts := strings.Split(values[0], " ")
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			unauthorized(c)
			return
		}
		identity, err := service.Authenticate(c.Request.Context(), parts[1])
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				unauthorized(c)
			} else {
				securitylog.Log(c.Request.Context(), "auth.token", "failure", 503, 0, "database_error")
				writeAuthError(c, 503, "temporarily_unavailable", "authentication temporarily unavailable")
				c.Abort()
			}
			return
		}
		if identity.UserID == 0 {
			unauthorized(c)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Request = c.Request.WithContext(securitylog.WithUser(WithIdentity(c.Request.Context(), identity), identity.UserID))
		c.Next()
	}
}
func unauthorized(c *gin.Context) {
	securitylog.Log(c.Request.Context(), "auth.token", "failure", 401, 0, "unauthorized")
	c.Header("WWW-Authenticate", "Bearer")
	writeAuthError(c, 401, "unauthorized", "valid authentication required")
	c.Abort()
}

// Fixed windows are bounded by both source IP and process, and never trust
// X-Forwarded-For. This is a local single-process throttle, not a global lock.
type rateEntry struct {
	count int
	reset time.Time
}
type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
	global  rateEntry
}

func (l *attemptLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.global.reset) {
		l.global = rateEntry{reset: now.Add(time.Minute)}
	}
	for key, entry := range l.entries {
		if !now.Before(entry.reset) {
			delete(l.entries, key)
		}
	}
	entry, exists := l.entries[ip]
	if !exists {
		if len(l.entries) >= 1024 {
			return false
		}
		entry = rateEntry{reset: now.Add(time.Minute)}
	}
	if entry.count >= 20 || l.global.count >= 60 {
		return false
	}
	entry.count++
	l.global.count++
	l.entries[ip] = entry
	return true
}

type Handler struct {
	service *Service
	limiter attemptLimiter
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service, limiter: attemptLimiter{entries: make(map[string]rateEntry)}}
}
func (h *Handler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/auth/register", h.throttle, h.register)
	router.POST("/auth/login", h.throttle, h.login)
	router.POST("/auth/logout", Middleware(h.service), h.logout)
	router.GET("/auth/me", Middleware(h.service), h.me)
}
func (h *Handler) throttle(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ip, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		ip = c.Request.RemoteAddr
	}
	if !h.limiter.allow(ip, time.Now()) {
		event := "auth.register"
		if c.FullPath() == "/auth/login" {
			event = "auth.login"
		}
		securitylog.Log(c.Request.Context(), event, "failure", 429, 0, "rate_limited")
		c.Header("Retry-After", "60")
		writeAuthError(c, 429, "rate_limited", "too many authentication attempts")
		c.Abort()
		return
	}
	c.Next()
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readCredentials(c *gin.Context) (credentials, bool) {
	media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || media != "application/json" {
		writeAuthError(c, 415, "unsupported_media_type", "Content-Type must be application/json")
		return credentials{}, false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var input credentials
	if err = decoder.Decode(&input); err != nil {
		writeAuthError(c, 400, "invalid_json", "invalid credentials JSON")
		return credentials{}, false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAuthError(c, 400, "invalid_json", "one JSON object required")
		return credentials{}, false
	}
	return input, true
}
func (h *Handler) register(c *gin.Context) {
	defer recordAuth(c, "auth.register")
	input, ok := readCredentials(c)
	if !ok {
		return
	}
	user, err := h.service.Register(c.Request.Context(), input.Username, input.Password)
	if err != nil {
		authFailure(c, err)
		return
	}
	c.Request = c.Request.WithContext(securitylog.WithUser(c.Request.Context(), user.ID))
	c.JSON(201, gin.H{"data": user})
}
func (h *Handler) login(c *gin.Context) {
	defer recordAuth(c, "auth.login")
	input, ok := readCredentials(c)
	if !ok {
		return
	}
	result, err := h.service.Login(c.Request.Context(), input.Username, input.Password)
	if err != nil {
		authFailure(c, err)
		return
	}
	c.Request = c.Request.WithContext(securitylog.WithUser(c.Request.Context(), result.User.ID))
	c.JSON(200, gin.H{"data": result})
}
func (h *Handler) logout(c *gin.Context) {
	defer recordAuth(c, "auth.logout")
	if err := h.service.Logout(c.Request.Context()); err != nil {
		authFailure(c, err)
		return
	}
	c.JSON(200, gin.H{"data": gin.H{"logged_out": true}})
}
func (h *Handler) me(c *gin.Context) {
	id, _ := IdentityFromContext(c.Request.Context())
	c.JSON(200, gin.H{"data": User{ID: id.UserID, Username: id.Username}})
}
func authFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeAuthError(c, 400, "invalid_argument", "username must be 3-32 ASCII letters/digits/underscore; password must be 12-128 UTF-8 bytes")
	case errors.Is(err, ErrRegistration):
		writeAuthError(c, 409, "registration_failed", "registration could not be completed")
	case errors.Is(err, ErrCredentials):
		writeAuthError(c, 401, "invalid_credentials", "invalid username or password")
	case errors.Is(err, ErrUnauthorized):
		unauthorized(c)
	case errors.Is(err, ErrBusy):
		writeAuthError(c, 429, "rate_limited", "authentication capacity reached; retry later")
	default:
		writeAuthError(c, 503, "temporarily_unavailable", "authentication temporarily unavailable")
	}
}
func writeAuthError(c *gin.Context, status int, code, message string) {
	c.Set("auth_error_category", code)
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func recordAuth(c *gin.Context, event string) {
	status := c.Writer.Status()
	outcome, category := "success", "none"
	if !c.Writer.Written() {
		status = 500
		outcome = "failure"
		category = "internal_error"
	}
	if status >= 400 {
		outcome = "failure"
		if value := c.GetString("auth_error_category"); value != "" {
			category = value
		}
		if category == "temporarily_unavailable" {
			category = "database_error"
		}
	}
	securitylog.Log(c.Request.Context(), event, outcome, status, securitylog.UserID(c.Request.Context()), category)
}
