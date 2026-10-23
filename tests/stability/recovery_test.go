//go:build recovery && linux

package stability_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
)

const baseURL = "http://127.0.0.1:18082"

type response struct {
	Status   int
	Code     string
	ID       uint64
	State    string
	Token    string
	Err      error
	MS       float64
	Replayed bool
}
type evidence struct {
	Name           string  `json:"name"`
	UTC            string  `json:"utc"`
	ElapsedMS      float64 `json:"elapsed_ms"`
	ResponseMS     float64 `json:"response_ms"`
	Status         int     `json:"http_status"`
	Code           string  `json:"error_code"`
	TransportError bool    `json:"transport_error"`
	Detail         string  `json:"detail"`
}
type fixture struct {
	t              *testing.T
	db             *sql.DB
	cmd            *exec.Cmd
	binary         string
	tokenA, tokenB string
	events         []evidence
}

func TestIsolatedRecovery(t *testing.T) {
	name := os.Getenv("DB_NAME")
	if !regexp.MustCompile(`^order_api_recovery_[a-f0-9]{12}$`).MatchString(name) || os.Getenv("RECOVERY_TEST_ENABLED") != "1" {
		t.Fatal("exact dedicated recovery environment required")
	}
	cfg := mysql.NewConfig()
	cfg.User = "root"
	cfg.Passwd = os.Getenv("MYSQL_ROOT_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = "mysql:3306"
	cfg.ParseTime = true
	cfg.Timeout = 3 * time.Second
	cfg.ReadTimeout = 4 * time.Second
	cfg.WriteTimeout = 4 * time.Second
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec("GRANT ALL ON `" + name + "`.* TO 'recovery_app'@'%'"); err != nil {
		t.Fatal(err)
	}
	admin.Close()
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(3)
	if err = goose.SetDialect("mysql"); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var existing int
	if err = db.QueryRow("SELECT COUNT(*) FROM products").Scan(&existing); err != nil || existing != 0 {
		t.Fatal("recovery volume is not empty: preserve it and select a new exact test volume")
	}
	for _, q := range []string{`INSERT INTO products(id,sku,name,price_cents,status) VALUES(1,'RECOVER-A','Synthetic A',1000,'ACTIVE'),(2,'RECOVER-B','Synthetic B',2000,'ACTIVE')`, `INSERT INTO inventories(product_id,available_qty) VALUES(1,100),(2,200)`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(t.TempDir(), "order-api")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %s", output)
	}
	f := &fixture{t: t, db: db, binary: binary}
	defer func() {
		if f.cmd != nil {
			f.cmd.Process.Kill()
			f.cmd.Wait()
		}
		data, _ := json.MarshalIndent(f.events, "", "  ")
		os.WriteFile("/control/recovery-report.json", data, 0600)
	}()
	f.start()
	f.tokenA = f.register()
	f.tokenB = f.register()
	created := f.require(f.call(context.Background(), "POST", "/orders", "before-fault", f.tokenA, oneItem), 201)
	f.check()

	t.Run("database_temporarily_paused", func(t *testing.T) {
		f.t = t
		before := f.runtime()
		start := time.Now()
		f.action("pause")
		result := f.call(context.Background(), "POST", "/orders", "unavailable", f.tokenA, oneItem)
		if result.Status != 503 || result.MS > 4500 {
			t.Fatalf("paused database status=%d duration=%.1fms", result.Status, result.MS)
		}
		f.action("resume")
		f.waitDatabase()
		f.waitAuthentication()
		recovered := f.require(f.call(context.Background(), "POST", "/orders", "unavailable", f.tokenA, oneItem), 201)
		f.record("database_pause", start, result, "3s database/auth deadline; same process recovered; one order created after resume")
		if recovered.ID == created.ID {
			t.Fatal("different key returned wrong order")
		}
		after := f.runtime()
		if after.Pool.InUse != 0 || after.Runtime.Goroutines > before.Runtime.Goroutines+12 {
			t.Fatal("resources did not settle after database recovery")
		}
		f.check()
	})
	t.Run("SQL_failure_rolls_back_multiple_items", func(t *testing.T) {
		f.t = t
		start := time.Now()
		before := f.counts()
		_, err := db.Exec(`CREATE TRIGGER fail_recovery_inventory BEFORE UPDATE ON inventories FOR EACH ROW BEGIN IF NEW.product_id=2 THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='controlled recovery failure'; END IF; END`)
		if err != nil {
			t.Fatal(err)
		}
		result := f.call(context.Background(), "POST", "/orders", "sql-fault", f.tokenA, twoItems)
		if result.Status != 500 {
			t.Fatalf("injected SQL status=%d", result.Status)
		}
		if f.counts() != before {
			t.Fatal("SQL fault left partial changes")
		}
		if _, err = db.Exec("DROP TRIGGER fail_recovery_inventory"); err != nil {
			t.Fatal(err)
		}
		f.require(f.call(context.Background(), "POST", "/orders", "sql-fault", f.tokenA, twoItems), 201)
		f.record("SQL_trigger_rollback", start, result, "second inventory UPDATE failed; header, two items and first deduction rolled back; retry succeeded")
		f.check()
	})
	t.Run("client_cancellation", func(t *testing.T) {
		f.t = t
		start := time.Now()
		before := f.counts()
		lock := f.lock()
		ctx, cancel := context.WithCancel(context.Background())
		result := f.async(ctx, "client-cancel")
		f.waitLock()
		cancel()
		got := <-result
		if got.Err == nil {
			t.Fatal("cancelled client unexpectedly received a response")
		}
		lock.Rollback()
		f.waitTransactions()
		if f.counts() != before {
			t.Fatal("cancelled transaction persisted")
		}
		f.require(f.call(context.Background(), "POST", "/orders", "client-cancel", f.tokenA, oneItem), 201)
		f.record("client_context_cancellation", start, got, "blocked request cancelled; no partial commit; same key safely retried")
		f.check()
	})
	t.Run("client_deadline", func(t *testing.T) {
		f.t = t
		start := time.Now()
		before := f.counts()
		lock := f.lock()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		result := f.async(ctx, "deadline-key")
		f.waitLock()
		got := <-result
		lock.Rollback()
		f.waitTransactions()
		if !errors.Is(got.Err, context.DeadlineExceeded) || f.counts() != before {
			t.Fatal("client deadline failed to abort without partial data")
		}
		f.require(f.call(context.Background(), "POST", "/orders", "deadline-key", f.tokenA, oneItem), 201)
		f.record("client_deadline_200ms", start, got, "client deadline expired; transaction rolled back; same key retry created once")
		f.check()
	})
	t.Run("graceful_shutdown_inflight", func(t *testing.T) {
		f.t = t
		start := time.Now()
		lock := f.lock()
		result := f.async(context.Background(), "graceful-key")
		f.waitLock()
		if err := f.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		lock.Rollback()
		got := f.require(<-result, 201)
		if err := f.cmd.Wait(); err != nil {
			t.Fatalf("graceful exit: %v", err)
		}
		f.cmd = nil
		f.start()
		f.require(f.call(context.Background(), "GET", "/auth/me", "", f.tokenA, ""), 200)
		replay := f.require(f.call(context.Background(), "POST", "/orders", "graceful-key", f.tokenA, oneItem), 200)
		if replay.ID != got.ID || !replay.Replayed {
			t.Fatal("graceful restart lost idempotency")
		}
		f.record("SIGTERM_inflight_and_restart", start, replay, "inflight request completed 201; exit code 0; original session valid; same key replayed")
		f.check()
	})
	t.Run("abnormal_process_exit", func(t *testing.T) {
		f.t = t
		start := time.Now()
		before := f.counts()
		lock := f.lock()
		result := f.async(context.Background(), "killed-key")
		f.waitLock()
		if err := f.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		f.cmd.Wait()
		f.cmd = nil
		got := <-result
		lock.Rollback()
		f.waitTransactions()
		if got.Err == nil {
			t.Fatal("killed process returned HTTP success")
		}
		if f.counts() != before {
			t.Fatal("killed uncommitted transaction survived")
		}
		f.start()
		f.require(f.call(context.Background(), "GET", "/auth/me", "", f.tokenA, ""), 200)
		f.require(f.call(context.Background(), "POST", "/orders", "killed-key", f.tokenA, oneItem), 201)
		f.record("SIGKILL_inflight_and_restart", start, got, "connection ended; uncommitted header/items/deduction absent; session persisted; safe retry created once")
		f.check()
	})
	t.Run("dedicated_MySQL_restart_inflight", func(t *testing.T) {
		f.t = t
		start := time.Now()
		before := f.counts()
		lock := f.lock()
		result := f.async(context.Background(), "mysql-restart-key")
		f.waitLock()
		f.action("restart")
		lock.Rollback()
		got := <-result
		if got.Err != nil || got.Status != http.StatusServiceUnavailable {
			t.Fatalf("interrupted transaction returned %d", got.Status)
		}
		f.waitDatabase()
		f.waitAuthentication()
		if f.counts() != before {
			t.Fatal("restart persisted uncommitted data")
		}
		f.require(f.call(context.Background(), "GET", fmt.Sprintf("/orders/%d", created.ID), "", f.tokenA, ""), 200)
		f.require(f.call(context.Background(), "POST", "/orders", "mysql-restart-key", f.tokenA, oneItem), 201)
		f.record("dedicated_MySQL_restart", start, got, "committed data and sessions survived; blocked transaction rolled back; same process recovered and retried once")
		f.check()
	})
	t.Run("cancellation_and_authorization_after_recovery", func(t *testing.T) {
		f.t = t
		start := time.Now()
		f.require(f.call(context.Background(), "GET", fmt.Sprintf("/orders/%d", created.ID), "", f.tokenB, ""), 404)
		f.require(f.call(context.Background(), "POST", fmt.Sprintf("/orders/%d/cancel", created.ID), "", f.tokenB, ""), 404)
		f.require(f.call(context.Background(), "POST", fmt.Sprintf("/orders/%d/cancel", created.ID), "", f.tokenA, ""), 200)
		before := f.counts()
		repeat := f.require(f.call(context.Background(), "POST", fmt.Sprintf("/orders/%d/cancel", created.ID), "", f.tokenA, ""), 200)
		if f.counts() != before {
			t.Fatal("repeated cancellation changed data")
		}
		f.record("post_recovery_authorization_and_cancel", start, repeat, "other user GET/cancel 404; repeat cancellation 200 without second replenishment")
		f.check()
	})
	f.t = t
	if !t.Failed() {
		final := f.counts()
		f.record("final_database_invariants", time.Now(), response{Status: 200}, fmt.Sprintf("orders=%d items=%d stock_1=%d stock_2=%d; all six invariants zero", final[0], final[1], final[2], final[3]))
	}
}

const oneItem = `{"items":[{"product_id":1,"quantity":1}]}`
const twoItems = `{"items":[{"product_id":1,"quantity":1},{"product_id":2,"quantity":1}]}`

func (f *fixture) call(ctx context.Context, method, path, key, token, body string) response {
	started := time.Now()
	req, _ := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 5 * time.Second}
	r, err := client.Do(req)
	out := response{Err: err, MS: float64(time.Since(started)) / float64(time.Millisecond)}
	if err != nil {
		return out
	}
	defer r.Body.Close()
	var payload struct {
		Data struct {
			ID          uint64 `json:"id"`
			Status      string `json:"status"`
			AccessToken string `json:"access_token"`
		}
		Error struct{ Code string }
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&payload); err != nil {
		out.Err = err
	}
	out.Status = r.StatusCode
	out.Code = payload.Error.Code
	out.ID = payload.Data.ID
	out.State = payload.Data.Status
	out.Token = payload.Data.AccessToken
	out.Replayed = r.Header.Get("Idempotency-Replayed") == "true"
	return out
}
func (f *fixture) require(r response, status int) response {
	f.t.Helper()
	if r.Err != nil || r.Status != status {
		f.t.Fatalf("HTTP got status=%d category=%s transport_error=%v want=%d", r.Status, r.Code, r.Err != nil, status)
	}
	return r
}
func (f *fixture) register() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		f.t.Fatal(err)
	}
	name := "r_" + hex.EncodeToString(random[:8])
	body, _ := json.Marshal(map[string]string{"username": name, "password": hex.EncodeToString(random)})
	f.require(f.call(context.Background(), "POST", "/auth/register", "", "", string(body)), 201)
	r := f.require(f.call(context.Background(), "POST", "/auth/login", "", "", string(body)), 200)
	if r.Token == "" {
		f.t.Fatal("no token")
	}
	return r.Token
}
func (f *fixture) start() {
	f.t.Helper()
	f.cmd = exec.Command(f.binary)
	f.cmd.Env = append(os.Environ(), "HTTP_ADDR=127.0.0.1:18082", "DB_QUERY_TIMEOUT=3s", "HTTP_SHUTDOWN_TIMEOUT=10s", "DIAGNOSTICS_ENABLED=true", "TRANSACTION_DIAGNOSTICS_ENABLED=false", "GOMEMLIMIT=256MiB", "GOMAXPROCS=2")
	// Logs contain only existing safe event fields. Never copy HTTP requests.
	log, err := os.OpenFile("/control/server.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	f.cmd.Stdout = log
	f.cmd.Stderr = log
	err = f.cmd.Start()
	log.Close()
	if err != nil {
		f.t.Fatal(err)
	}
	f.wait(func() bool { return f.call(context.Background(), "GET", "/health", "", "", "").Status == 200 }, 15*time.Second, "server readiness")
}
func (f *fixture) async(ctx context.Context, key string) <-chan response {
	c := make(chan response, 1)
	go func() { c <- f.call(ctx, "POST", "/orders", key, f.tokenA, oneItem) }()
	return c
}
func (f *fixture) lock() *sql.Tx {
	tx, err := f.db.Begin()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE inventories SET available_qty=available_qty WHERE product_id=1"); err != nil {
		f.t.Fatal(err)
	}
	return tx
}
func (f *fixture) waitLock() {
	f.wait(func() bool {
		var n int
		return f.db.QueryRow("SELECT COUNT(*) FROM performance_schema.data_lock_waits").Scan(&n) == nil && n > 0
	}, 2*time.Second, "inventory lock wait")
}
func (f *fixture) waitTransactions() {
	f.wait(func() bool {
		var n int
		return f.db.QueryRow("SELECT COUNT(*) FROM information_schema.innodb_trx WHERE trx_query IS NOT NULL").Scan(&n) == nil && n == 0
	}, 5*time.Second, "transaction rollback")
}
func (f *fixture) waitDatabase() {
	f.wait(func() bool { return f.db.Ping() == nil }, 15*time.Second, "database recovery")
}
func (f *fixture) waitAuthentication() {
	f.wait(func() bool { return f.call(context.Background(), "GET", "/auth/me", "", f.tokenA, "").Status == 200 }, 15*time.Second, "application pool/auth recovery")
}
func (f *fixture) wait(probe func() bool, limit time.Duration, what string) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if probe() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.t.Fatalf("timeout waiting for %s", what)
}
func (f *fixture) action(action string) {
	f.t.Helper()
	path := "/control/" + action
	if err := os.WriteFile(path+".request", []byte("requested"), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.wait(func() bool {
		data, err := os.ReadFile(path + ".done")
		return err == nil && bytes.Equal(data, []byte("ok"))
	}, 45*time.Second, "host "+action)
}
func (f *fixture) counts() [4]int64 {
	var v [4]int64
	for i, q := range []string{"SELECT COUNT(*) FROM orders", "SELECT COUNT(*) FROM order_items", "SELECT available_qty FROM inventories WHERE product_id=1", "SELECT available_qty FROM inventories WHERE product_id=2"} {
		if err := f.db.QueryRow(q).Scan(&v[i]); err != nil {
			f.t.Fatal("read counts failed")
		}
	}
	return v
}
func (f *fixture) check() {
	f.t.Helper()
	queries := []string{
		"SELECT COUNT(*) FROM inventories WHERE available_qty<0",
		"SELECT COUNT(*) FROM orders o LEFT JOIN order_items i ON i.order_id=o.id WHERE i.order_id IS NULL",
		"SELECT COUNT(*) FROM order_items i LEFT JOIN orders o ON o.id=i.order_id WHERE o.id IS NULL",
		"SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM orders GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) d",
		"SELECT COUNT(*) FROM (SELECT o.id FROM orders o JOIN order_items i ON i.order_id=o.id GROUP BY o.id,o.total_amount_cents HAVING o.total_amount_cents<>SUM(i.unit_price_cents*i.quantity)) d",
		"SELECT COUNT(*) FROM inventories v WHERE v.available_qty<>IF(v.product_id=1,100,200)-COALESCE((SELECT SUM(i.quantity) FROM order_items i JOIN orders o ON o.id=i.order_id WHERE i.product_id=v.product_id AND o.status='CREATED'),0)",
	}
	for _, q := range queries {
		var n int
		if err := f.db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			f.t.Fatalf("invariant failed n=%d err=%v", n, err)
		}
	}
}

type runtimeView struct {
	Runtime struct {
		Goroutines int `json:"goroutines"`
	}
	Pool struct {
		InUse int `json:"in_use"`
	} `json:"database"`
}

func (f *fixture) runtime() runtimeView {
	var result runtimeView
	client := http.Client{Timeout: 3 * time.Second}
	r, err := client.Get(baseURL + "/debug/runtime")
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	if err = json.NewDecoder(r.Body).Decode(&result); err != nil {
		f.t.Fatal(err)
	}
	return result
}
func (f *fixture) record(name string, start time.Time, r response, detail string) {
	event := evidence{Name: name, UTC: start.UTC().Format(time.RFC3339Nano), ElapsedMS: float64(time.Since(start)) / float64(time.Millisecond), ResponseMS: r.MS, Status: r.Status, Code: r.Code, TransportError: r.Err != nil, Detail: detail}
	f.events = append(f.events, event)
	f.t.Logf("%s status=%d code=%s elapsed_ms=%.1f transport_error=%v", name, r.Status, r.Code, event.ElapsedMS, r.Err != nil)
}
