//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/sessioncleanup"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

func insertCleanupSession(t *testing.T, db *sql.DB, userID uint64, created, expires time.Time, revoked *time.Time) string {
	t.Helper()
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(token))
	if _, err := db.Exec(`INSERT INTO auth_sessions (user_id,token_hash,created_at,expires_at,revoked_at) VALUES (?,?,?,?,?)`, userID, hash[:], created, expires, revoked); err != nil {
		t.Fatal(err)
	}
	return token
}

func assertCleanupTokenStatus(t *testing.T, router http.Handler, token string, status int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertHTTPStatus(t, response, status)
}

func TestSessionCleanupRetentionAndBatches(t *testing.T) {
	db := openMigratedTestDatabase(t)
	router := newAuthenticatedRouter(t, db)
	client := registerClient(t, router)
	now := time.Now().UTC().Truncate(time.Microsecond)
	old := now.Add(-8 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)
	active := insertCleanupSession(t, db, client.userID, now.Add(-24*time.Hour), now.Add(24*time.Hour), nil)
	expiredOld := insertCleanupSession(t, db, client.userID, now.Add(-9*24*time.Hour), old, nil)
	revokedOld := insertCleanupSession(t, db, client.userID, now.Add(-9*24*time.Hour), now.Add(24*time.Hour), &old)
	expiredRecent := insertCleanupSession(t, db, client.userID, now.Add(-2*24*time.Hour), recent, nil)
	revokedRecent := insertCleanupSession(t, db, client.userID, now.Add(-2*24*time.Hour), now.Add(24*time.Hour), &recent)
	for range 5 {
		insertCleanupSession(t, db, client.userID, now.Add(-9*24*time.Hour), old, nil)
	}
	for _, token := range []string{expiredOld, revokedOld, expiredRecent, revokedRecent} {
		assertCleanupTokenStatus(t, router, token, 401)
	}
	options := sessioncleanup.DefaultOptions()
	options.BatchSize = 2
	service := sessioncleanup.NewService(mysqlstore.NewSessionCleanupStore(db, 3*time.Second))
	result, err := service.Run(context.Background(), options)
	if err != nil || result.Deleted != 7 || result.Batches != 4 || result.LimitReached {
		t.Fatalf("cleanup result=%+v err=%v", result, err)
	}
	var count, users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_sessions`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("remaining=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("user count=%d err=%v", users, err)
	}
	assertCleanupTokenStatus(t, router, active, 200)
	assertCleanupTokenStatus(t, router, client.token, 200)
	for _, token := range []string{expiredOld, revokedOld, expiredRecent, revokedRecent} {
		assertCleanupTokenStatus(t, router, token, 401)
	}
	result, err = service.Run(context.Background(), options)
	if err != nil || result.Deleted != 0 || result.Batches != 1 {
		t.Fatalf("repeat cleanup=%+v err=%v", result, err)
	}
	t.Log("deleted 7 eligible sessions in 4 batches; retained 2 active and 2 within retention; repeat deleted 0; invalid tokens remain unauthorized")
}

func TestSessionCleanupFailureAndCancellation(t *testing.T) {
	db := openMigratedTestDatabase(t)
	client := registerClient(t, newAuthenticatedRouter(t, db))
	now := time.Now().UTC()
	insertCleanupSession(t, db, client.userID, now.Add(-10*24*time.Hour), now.Add(-8*24*time.Hour), nil)
	service := sessioncleanup.NewService(mysqlstore.NewSessionCleanupStore(db, 3*time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := service.Run(ctx, sessioncleanup.DefaultOptions()); !errors.Is(err, context.Canceled) || result.Deleted != 0 {
		t.Fatalf("cancel result=%+v err=%v", result, err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if result, err := service.Run(ctx, sessioncleanup.DefaultOptions()); !errors.Is(err, context.DeadlineExceeded) || result.Deleted != 0 {
		t.Fatalf("timeout result=%+v err=%v", result, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER cleanup_delete_failure BEFORE DELETE ON auth_sessions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='controlled session cleanup failure'`); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Run(context.Background(), sessioncleanup.DefaultOptions()); err == nil || result.Deleted != 0 {
		t.Fatalf("failure result=%+v err=%v", result, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_sessions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("failure deleted sessions: count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DROP TRIGGER cleanup_delete_failure`); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Run(context.Background(), sessioncleanup.DefaultOptions()); err != nil || result.Deleted != 1 {
		t.Fatalf("recovery result=%+v err=%v", result, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background(), sessioncleanup.DefaultOptions()); err == nil {
		t.Fatal("closed database cleanup succeeded")
	}
	t.Log("cancelled/expired contexts and trigger failure deleted nothing; retry after removing trigger deleted 1; closed DB stopped cleanup")
}

func TestSessionCleanupConcurrentLoginLogout(t *testing.T) {
	db := openMigratedTestDatabase(t)
	router := newAuthenticatedRouter(t, db)
	client := registerClient(t, router)
	now := time.Now().UTC()
	for range 20 {
		insertCleanupSession(t, db, client.userID, now.Add(-10*24*time.Hour), now.Add(-8*24*time.Hour), nil)
	}
	start := make(chan struct{})
	type cleanupOutcome struct {
		result sessioncleanup.Result
		err    error
	}
	cleaned := make(chan cleanupOutcome, 1)
	login := make(chan *httptest.ResponseRecorder, 1)
	logout := make(chan *httptest.ResponseRecorder, 1)
	body, _ := json.Marshal(map[string]string{"username": client.username, "password": client.password})
	go func() {
		<-start
		options := sessioncleanup.DefaultOptions()
		options.BatchSize = 2
		result, err := sessioncleanup.NewService(mysqlstore.NewSessionCleanupStore(db, 3*time.Second)).Run(context.Background(), options)
		cleaned <- cleanupOutcome{result, err}
	}()
	go func() { <-start; login <- authRequest(router, "/auth/login", string(body), "") }()
	go func() { <-start; logout <- authRequest(router, "/auth/logout", "", client.token) }()
	close(start)
	result := <-cleaned
	if result.err != nil || result.result.Deleted != 20 {
		t.Fatalf("concurrent cleanup=%+v err=%v", result.result, result.err)
	}
	loggedIn := <-login
	assertHTTPStatus(t, loggedIn, 200)
	assertHTTPStatus(t, <-logout, 200)
	var issued struct {
		Data auth.LoginResult `json:"data"`
	}
	if err := json.Unmarshal(loggedIn.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	assertCleanupTokenStatus(t, router, issued.Data.AccessToken, 200)
	assertCleanupTokenStatus(t, router, client.token, 401)
	var total, active, revoked int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(revoked_at IS NULL),0),COALESCE(SUM(revoked_at IS NOT NULL),0) FROM auth_sessions`).Scan(&total, &active, &revoked); err != nil || total != 2 || active != 1 || revoked != 1 {
		t.Fatalf("sessions total=%d active=%d revoked=%d err=%v", total, active, revoked, err)
	}
	t.Log("concurrent real login/logout and 20 deletions: 1 valid newly-issued session, 1 retained revoked session; no active token removed")
}
