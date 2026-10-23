//go:build backup && linux

package release_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/httpapi"
	"internshiplab/order-api/internal/order"
	"internshiplab/order-api/internal/product"
	mysqlstore "internshiplab/order-api/internal/store/mysql"
)

type tableEvidence struct {
	Rows         int    `json:"rows"`
	DataSHA256   string `json:"data_sha256"`
	SchemaSHA256 string `json:"schema_sha256"`
}

func TestLogicalBackupRestore(t *testing.T) {
	name := os.Getenv("BACKUP_DATABASE")
	if os.Getenv("BACKUP_TEST_ENABLED") != "1" || !regexp.MustCompile(`^order_api_backup_[a-f0-9]{12}$`).MatchString(name) {
		t.Fatal("dedicated backup environment required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	open := func(host, dbName string) *sql.DB {
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = "root", "backup-root-not-a-secret", "tcp", host+":3306", dbName
		cfg.ParseTime, cfg.Loc, cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = true, time.UTC, 3*time.Second, 5*time.Second, 5*time.Second
		cfg.Params = map[string]string{"time_zone": "'+00:00'"}
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("open test database failed")
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		t.Cleanup(func() { db.Close() })
		return db
	}
	sourceAdmin, targetAdmin := open("mysql-source", ""), open("mysql-target", "")
	for _, db := range []*sql.DB{sourceAdmin, targetAdmin} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name=?", name).Scan(&count); err != nil || count != 0 {
			t.Fatal("backup test requires two fresh instances with no destination schema")
		}
	}
	if _, err := sourceAdmin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	source := open("mysql-source", name)
	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, source, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO products(id,sku,name,price_cents,status) VALUES(1,'BACKUP-A','Synthetic A',1000,'ACTIVE'),(2,'BACKUP-B','Synthetic B',2000,'ACTIVE'),(3,'BACKUP-C','Synthetic C',3000,'ACTIVE')`,
		`INSERT INTO inventories(product_id,available_qty) VALUES(1,100),(2,200),(3,50)`,
	} {
		if _, err := source.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	start := func(db *sql.DB) *httptest.Server {
		as, err := auth.NewService(mysqlstore.NewAuthStore(db, 3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		s := httptest.NewServer(httpapi.NewRouter(product.NewHandler(product.NewService(mysqlstore.NewProductStore(db, 3*time.Second))), order.NewHandler(order.NewService(mysqlstore.NewOrderStore(db, 3*time.Second))), as))
		t.Cleanup(s.Close)
		return s
	}
	openApplication := func(host string, admin *sql.DB) *sql.DB {
		// A logical business-schema dump does not transfer mysql.* accounts.
		// Recreate the least-privilege runtime account explicitly on each instance.
		for _, q := range []string{`CREATE USER 'backup_app'@'%' IDENTIFIED BY 'backup-app-not-a-secret'`, "GRANT SELECT, INSERT, UPDATE, DELETE ON `" + name + "`.* TO 'backup_app'@'%'"} {
			if _, err := admin.ExecContext(ctx, q); err != nil {
				t.Fatal("isolated runtime account setup failed")
			}
		}
		cfg := mysql.NewConfig()
		cfg.User = "backup_app"
		cfg.Passwd = "backup-app-not-a-secret"
		cfg.Net = "tcp"
		cfg.Addr = host + ":3306"
		cfg.DBName = name
		cfg.ParseTime = true
		cfg.Loc = time.UTC
		cfg.Timeout = 3 * time.Second
		cfg.ReadTimeout = 5 * time.Second
		cfg.WriteTimeout = 5 * time.Second
		cfg.Params = map[string]string{"time_zone": "'+00:00'"}
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			t.Fatal("application pool setup failed")
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		t.Cleanup(func() { db.Close() })
		return db
	}
	s := start(openApplication("mysql-source", sourceAdmin))
	statuses := map[string]int{}
	call := func(server *httptest.Server, label, method, path, token, key string, body any, expected int) map[string]any {
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("HTTP request failed")
		}
		defer res.Body.Close()
		statuses[label] = res.StatusCode
		if res.StatusCode != expected {
			t.Fatalf("%s status=%d want=%d", label, res.StatusCode, expected)
		}
		var result struct {
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&result); err != nil {
			t.Fatal("decode response failed")
		}
		return result.Data
	}
	credentials := func(username string) map[string]string {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			t.Fatal(err)
		}
		return map[string]string{"username": username, "password": base64.RawURLEncoding.EncodeToString(secret)}
	}
	a, b := credentials("synthetic_backup_a"), credentials("synthetic_backup_b")
	call(s, "register_a", "POST", "/auth/register", "", "", a, 201)
	call(s, "register_b", "POST", "/auth/register", "", "", b, 201)
	tokenA := call(s, "login_a", "POST", "/auth/login", "", "", a, 200)["access_token"].(string)
	tokenB := call(s, "login_b", "POST", "/auth/login", "", "", b, 200)["access_token"].(string)
	items := func(values ...int) map[string]any {
		rows := []map[string]int{}
		for i := 0; i < len(values); i += 2 {
			rows = append(rows, map[string]int{"product_id": values[i], "quantity": values[i+1]})
		}
		return map[string]any{"items": rows}
	}
	created := call(s, "create_a", "POST", "/orders", tokenA, "backup-same-key", items(1, 2, 2, 3), 201)
	idA := uint64(created["id"].(float64))
	call(s, "create_b_same_key", "POST", "/orders", tokenB, "backup-same-key", items(1, 1), 201)
	cancelled := call(s, "create_to_cancel", "POST", "/orders", tokenA, "backup-cancelled", items(3, 1), 201)
	cancelPath := fmt.Sprintf("/orders/%.0f/cancel", cancelled["id"])
	call(s, "cancel_before_backup", "POST", cancelPath, tokenA, "", nil, 200)
	revoked := call(s, "extra_login", "POST", "/auth/login", "", "", a, 200)["access_token"].(string)
	call(s, "logout_before_backup", "POST", "/auth/logout", revoked, "", nil, 200)
	expired := call(s, "expiring_login", "POST", "/auth/login", "", "", b, 200)["access_token"].(string)
	expiredHash := sha256.Sum256([]byte(expired))
	if _, err := source.ExecContext(ctx, `UPDATE auth_sessions SET created_at=UTC_TIMESTAMP(6)-INTERVAL 3 DAY,expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 DAY WHERE token_hash=?`, expiredHash[:]); err != nil {
		t.Fatal(err)
	}
	s.Close()

	snapshot := func(db *sql.DB) map[string]tableEvidence {
		out := map[string]tableEvidence{}
		for _, table := range []string{"users", "auth_sessions", "products", "inventories", "orders", "order_items", "goose_db_version"} {
			// The table identifiers are a fixed whitelist, not HTTP input.
			rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1")
			if err != nil {
				t.Fatal(err)
			}
			cols, err := rows.Columns()
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			h := sha256.New()
			count := 0
			for rows.Next() {
				values := make([]any, len(cols))
				dest := make([]any, len(cols))
				for i := range values {
					dest[i] = &values[i]
				}
				if err := rows.Scan(dest...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				json.NewEncoder(h).Encode(values)
				count++
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			var ignored, ddl string
			if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table).Scan(&ignored, &ddl); err != nil {
				t.Fatal(err)
			}
			schemaHash := sha256.Sum256([]byte(ddl))
			out[table] = tableEvidence{count, hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(schemaHash[:])}
		}
		return out
	}
	before := snapshot(source)
	control := os.Getenv("BACKUP_CONTROL_DIR")
	if control != "/control" {
		t.Fatal("exact control mount required")
	}
	if err := os.WriteFile(filepath.Join(control, "backup.request"), []byte(name), 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case <-ctx.Done():
			t.Fatal("backup/restore control deadline")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(control, "backup.done")); err == nil {
				waiting = false
			}
		}
	}
	target := open("mysql-target", name)
	after := snapshot(target)
	x, _ := json.Marshal(before)
	y, _ := json.Marshal(after)
	if !bytes.Equal(x, y) {
		t.Fatal("restored row counts, canonical data or SHOW CREATE TABLE hashes differ")
	}
	version, err := goose.GetDBVersionContext(ctx, target)
	if err != nil || version != 6 {
		t.Fatal("restored migration version mismatch")
	}
	var databaseVersion string
	if err := target.QueryRowContext(ctx, "SELECT VERSION()").Scan(&databaseVersion); err != nil {
		t.Fatal(err)
	}
	restored := start(openApplication("mysql-target", targetAdmin))
	call(restored, "restored_me", "GET", "/auth/me", tokenA, "", nil, 200)
	call(restored, "restored_revoked", "GET", "/auth/me", revoked, "", nil, 401)
	call(restored, "restored_expired", "GET", "/auth/me", expired, "", nil, 401)
	path := fmt.Sprintf("/orders/%d", idA)
	call(restored, "restored_order", "GET", path, tokenA, "", nil, 200)
	call(restored, "cross_user_get", "GET", path, tokenB, "", nil, 404)
	call(restored, "cross_user_cancel", "POST", path+"/cancel", tokenB, "", nil, 404)
	replay := call(restored, "restored_idempotency", "POST", "/orders", tokenA, "backup-same-key", items(2, 3, 1, 2), 200)
	if uint64(replay["id"].(float64)) != idA {
		t.Fatal("restored replay selected another order")
	}
	call(restored, "restored_cancel_replay", "POST", cancelPath, tokenA, "", nil, 200)
	newOrder := call(restored, "restored_create", "POST", "/orders", tokenA, "after-restore", items(2, 1), 201)
	newPath := fmt.Sprintf("/orders/%.0f/cancel", newOrder["id"])
	call(restored, "restored_new_cancel", "POST", newPath, tokenA, "", nil, 200)
	call(restored, "restored_new_cancel_replay", "POST", newPath, tokenA, "", nil, 200)
	checks := map[string]int{}
	queries := map[string]string{
		"negative_inventory":   `SELECT COUNT(*) FROM inventories WHERE available_qty<0`,
		"amount_mismatch":      `SELECT COUNT(*) FROM orders o WHERE total_amount_cents<>(SELECT COALESCE(SUM(unit_price_cents*quantity),0) FROM order_items WHERE order_id=o.id)`,
		"orders_without_items": `SELECT COUNT(*) FROM orders o WHERE NOT EXISTS(SELECT 1 FROM order_items WHERE order_id=o.id)`,
		"orphan_items":         `SELECT COUNT(*) FROM order_items i LEFT JOIN orders o ON o.id=i.order_id WHERE o.id IS NULL`,
		"duplicate_user_key":   `SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM orders GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) d`,
		"invalid_owner":        `SELECT COUNT(*) FROM orders o LEFT JOIN users u ON u.id=o.user_id WHERE u.id IS NULL`,
		"inventory_mismatch":   `SELECT COUNT(*) FROM inventories v WHERE available_qty<>(CASE product_id WHEN 1 THEN 100 WHEN 2 THEN 200 WHEN 3 THEN 50 END)-COALESCE((SELECT SUM(i.quantity) FROM order_items i JOIN orders o ON o.id=i.order_id WHERE i.product_id=v.product_id AND o.status='CREATED'),0)`,
	}
	for label, q := range queries {
		var count int
		if err := target.QueryRowContext(ctx, q).Scan(&count); err != nil {
			t.Fatal(err)
		}
		checks[label] = count
		if count != 0 {
			t.Fatalf("invariant failed: %s", label)
		}
	}
	// Exercise restored unique, CHECK and FK constraints; each rejected statement
	// must leave the snapshot unchanged. No destructive schema rollback is used.
	beforeRejectedStatements := snapshot(target)
	for label, tc := range map[string]struct {
		query string
		code  uint16
	}{
		"sku_unique":         {`INSERT INTO products(sku,name,price_cents,status) VALUES('BACKUP-A','duplicate',1,'ACTIVE')`, 1062},
		"sku_uppercase":      {`INSERT INTO products(sku,name,price_cents,status) VALUES('backup-invalid','invalid',1,'ACTIVE')`, 3819},
		"inventory_unsigned": {`UPDATE inventories SET available_qty=-1 WHERE product_id=1`, 1264},
		"inventory_fk":       {`INSERT INTO inventories(product_id,available_qty) VALUES(99999,1)`, 1452},
		"owner_fk":           {`UPDATE orders SET user_id=99999 WHERE id=1`, 1452},
		"user_key_unique":    {`INSERT INTO orders(user_id,order_no,idempotency_key,request_hash,status,total_amount_cents) SELECT user_id,'BACKUP-DUPLICATE',idempotency_key,request_hash,status,total_amount_cents FROM orders WHERE id=1`, 1062},
	} {
		_, err := target.ExecContext(ctx, tc.query)
		var driverErr *mysql.MySQLError
		if !errors.As(err, &driverErr) || driverErr.Number != tc.code {
			t.Fatalf("restored constraint %s did not return MySQL code %d", label, tc.code)
		}
		checks[label] = 0
	}
	final := snapshot(target)
	for table, previous := range beforeRejectedStatements {
		if previous.Rows != final[table].Rows || previous.DataSHA256 != final[table].DataSHA256 {
			t.Fatal("rejected statement changed stored data")
		}
	}
	result := map[string]any{"passed": true, "database": name, "database_version": databaseVersion, "migration_version": version, "before": before, "restored": after, "http_statuses": statuses, "violations": checks, "final": final, "scope": "logical InnoDB dump, fresh instance restore, real loopback HTTP through authenticated application router; no crash durability or disaster recovery guarantee"}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), tokenA) || strings.Contains(string(encoded), a["password"]) {
		t.Fatal("secret in evidence")
	}
	if err := os.WriteFile(filepath.Join(control, "backup-report.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}
