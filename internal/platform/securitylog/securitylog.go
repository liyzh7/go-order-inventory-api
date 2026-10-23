// Package securitylog records allowlisted security metadata. It intentionally
// has no API accepting raw errors, requests, tokens, usernames or arbitrary attrs.
package securitylog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"
	"time"
)

type contextKey struct{}
type metadata struct {
	requestID string
	userID    uint64
}

func NewContext(ctx context.Context) (context.Context, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, contextKey{}, metadata{requestID: hex.EncodeToString(random)}), nil
}
func WithUser(ctx context.Context, userID uint64) context.Context {
	m, _ := ctx.Value(contextKey{}).(metadata)
	m.userID = userID
	return context.WithValue(ctx, contextKey{}, m)
}
func RequestID(ctx context.Context) string {
	m, _ := ctx.Value(contextKey{}).(metadata)
	return m.requestID
}
func UserID(ctx context.Context) uint64 { m, _ := ctx.Value(contextKey{}).(metadata); return m.userID }

func NewLogger(writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey {
			return slog.Time(slog.TimeKey, attr.Value.Time().UTC())
		}
		return attr
	}}))
}

type budget struct {
	mu    sync.Mutex
	reset time.Time
	count int
}

// The first 30 failures per minute are emitted, followed by a single suppression
// marker. A bounded counter avoids a user-controlled per-IP/event map.
func (b *budget) allow(now time.Time) (emit, suppression bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !now.Before(b.reset) {
		b.reset = now.Add(time.Minute)
		b.count = 0
	}
	b.count++
	if b.count <= 30 {
		return true, false
	}
	if b.count == 31 {
		return true, true
	}
	return false, false
}

var failures budget

func category(value string) string {
	switch value {
	case "none", "invalid_argument", "invalid_json", "invalid_credentials", "registration_failed", "unauthorized", "rate_limited", "order_not_found", "order_state_conflict", "inventory_overflow", "insufficient_stock", "product_not_found", "product_inactive", "idempotency_conflict", "invalid_idempotency_key", "unsupported_media_type", "timeout", "cancelled", "database_error", "internal_error":
		return value
	default:
		return "internal_error"
	}
}
func event(value string) string {
	switch value {
	case "auth.register", "auth.login", "auth.logout", "auth.token", "order.access_denied", "order.cancel", "order.operation", "product.operation", "http.panic", "session.cleanup":
		return value
	default:
		return "security.unknown"
	}
}

func Log(ctx context.Context, eventName, outcome string, status int, userID uint64, errorCategory string) {
	eventName = event(eventName)
	errorCategory = category(errorCategory)
	switch outcome {
	case "success", "failure", "replayed":
	default:
		outcome = "failure"
	}
	if status < 0 || status > 599 {
		status = 0
	}
	if outcome == "failure" {
		emit, suppressed := failures.allow(time.Now())
		if !emit {
			return
		}
		if suppressed {
			eventName = "security.events_suppressed"
			outcome = "suppressed"
			errorCategory = "rate_limited"
			userID = 0
			status = 0
		}
	}
	slog.InfoContext(ctx, "security_event", "request_id", RequestID(ctx), "event", eventName,
		"outcome", outcome, "http_status", status, "user_id", userID, "error_category", errorCategory)
}

func LogCleanup(ctx context.Context, outcome, errorCategory string, deleted int64, duration time.Duration) {
	if outcome != "success" {
		outcome = "failure"
	}
	if deleted < 0 {
		deleted = 0
	}
	if duration < 0 {
		duration = 0
	}
	// Each bounded CLI invocation always gets a summary, even if HTTP failures
	// exhausted the log budget in a different component of the process.
	slog.InfoContext(ctx, "security_event", "request_id", RequestID(ctx), "event", "session.cleanup",
		"outcome", outcome, "http_status", 0, "user_id", 0, "error_category", category(errorCategory),
		"deleted_count", deleted, "duration_ms", duration.Milliseconds())
}
