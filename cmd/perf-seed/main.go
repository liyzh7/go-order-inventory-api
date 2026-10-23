// Command perf-seed creates deterministic synthetic data only in an explicitly
// named order_api_perf_* database. Its stdout contains disposable bearer tokens
// and must be redirected to an ignored local file.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"internshiplab/order-api/internal/auth"
	"internshiplab/order-api/internal/platform/config"
	"internshiplab/order-api/internal/platform/database"
)

var performanceDatabase = regexp.MustCompile(`^order_api_perf_[a-z0-9_]+$`)

type manifest struct {
	Seed               int64           `json:"seed"`
	ProductCount       int             `json:"product_count"`
	CompetitionProduct uint64          `json:"competition_product_id"`
	CompetitionStock   uint32          `json:"competition_initial_stock"`
	Users              []manifestUser  `json:"users"`
	CancellationOrders []manifestOrder `json:"cancellation_orders"`
}
type manifestUser struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}
type manifestOrder struct {
	ID        uint64 `json:"id"`
	UserIndex int    `json:"user_index"`
	ProductID uint64 `json:"product_id"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "performance seed failed:", safeCategory(err))
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("perf-seed", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	expected := flags.String("expected-database", "", "exact performance database name")
	allowReset := flags.Bool("allow-reset", false, "delete and recreate synthetic rows in the exact performance database")
	productCount := flags.Int("products", 1000, "number of synthetic products")
	userCount := flags.Int("users", 25, "number of synthetic users")
	cancelOrders := flags.Int("cancellation-orders", 500, "pre-created orders for query/cancel tests")
	competitionStock := flags.Int("competition-stock", 100, "initial stock of product 1")
	seed := flags.Int64("seed", 20261016, "deterministic data seed")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("invalid flags")
	}
	if !*allowReset || *productCount < 10 || *productCount > 100000 || *userCount < 2 || *userCount > 1000 || *cancelOrders < 0 || *cancelOrders > 100000 || *competitionStock < 1 {
		return errors.New("unsafe seed options")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := validateTarget(cfg.Database.Name, *expected); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.OpenMySQL(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := seedDatabase(ctx, db, *productCount, *userCount, *cancelOrders, uint32(*competitionStock), *seed)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func validateTarget(actual, expected string) error {
	if actual == "" || actual != expected || !performanceDatabase.MatchString(actual) {
		return errors.New("database target is not the exact expected order_api_perf_* database")
	}
	return nil
}

func seedDatabase(ctx context.Context, db *sql.DB, products, users, cancellationOrders int, competitionStock uint32, seed int64) (manifest, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return manifest{}, err
	}
	defer tx.Rollback()
	for _, statement := range []string{"DELETE FROM order_items", "DELETE FROM orders", "DELETE FROM auth_sessions", "DELETE FROM users", "DELETE FROM inventories", "DELETE FROM products"} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return manifest{}, err
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	productStatement, err := tx.PrepareContext(ctx, `INSERT INTO products(id,sku,name,price_cents,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return manifest{}, err
	}
	defer productStatement.Close()
	inventoryStatement, err := tx.PrepareContext(ctx, `INSERT INTO inventories(product_id,available_qty,updated_at) VALUES(?,?,?)`)
	if err != nil {
		return manifest{}, err
	}
	defer inventoryStatement.Close()
	for index := 1; index <= products; index++ {
		if _, err := productStatement.ExecContext(ctx, index, fmt.Sprintf("PERF-%06d", index), fmt.Sprintf("Synthetic Product %06d", index), 100+index%1000, "ACTIVE", now, now); err != nil {
			return manifest{}, err
		}
		stock := uint32(1_000_000)
		if index == 1 {
			stock = competitionStock
		}
		if _, err := inventoryStatement.ExecContext(ctx, index, stock, now); err != nil {
			return manifest{}, err
		}
	}
	passwordHash, err := auth.HashPassword("performance-only-password")
	if err != nil {
		return manifest{}, err
	}
	result := manifest{Seed: seed, ProductCount: products, CompetitionProduct: 1, CompetitionStock: competitionStock, Users: make([]manifestUser, 0, users), CancellationOrders: make([]manifestOrder, 0, cancellationOrders)}
	userStatement, err := tx.PrepareContext(ctx, `INSERT INTO users(id,username,password_hash,status,created_at) VALUES(?,?,?,?,?)`)
	if err != nil {
		return manifest{}, err
	}
	defer userStatement.Close()
	sessionStatement, err := tx.PrepareContext(ctx, `INSERT INTO auth_sessions(user_id,token_hash,expires_at,created_at) VALUES(?,?,?,?)`)
	if err != nil {
		return manifest{}, err
	}
	defer sessionStatement.Close()
	for index := 1; index <= users; index++ {
		username := fmt.Sprintf("perf_user_%04d", index)
		if _, err := userStatement.ExecContext(ctx, index, username, passwordHash, "ACTIVE", now); err != nil {
			return manifest{}, err
		}
		raw := sha256.Sum256([]byte(fmt.Sprintf("order-api-performance-token:%d:%d", seed, index)))
		token := base64.RawURLEncoding.EncodeToString(raw[:])
		tokenHash := sha256.Sum256([]byte(token))
		if _, err := sessionStatement.ExecContext(ctx, index, tokenHash[:], now.Add(24*time.Hour), now); err != nil {
			return manifest{}, err
		}
		result.Users = append(result.Users, manifestUser{ID: uint64(index), Username: username, Token: token})
	}
	orderStatement, err := tx.PrepareContext(ctx, `INSERT INTO orders(id,user_id,order_no,idempotency_key,request_hash,status,total_amount_cents,created_at,updated_at) VALUES(?,?,?,?,?,'CREATED',?,?,?)`)
	if err != nil {
		return manifest{}, err
	}
	defer orderStatement.Close()
	itemStatement, err := tx.PrepareContext(ctx, `INSERT INTO order_items(order_id,product_id,sku_snapshot,name_snapshot,unit_price_cents,quantity) VALUES(?,?,?,?,?,1)`)
	if err != nil {
		return manifest{}, err
	}
	defer itemStatement.Close()
	deductions := make(map[int]int)
	for index := 1; index <= cancellationOrders; index++ {
		userIndex := (index - 1) % users
		productID := 2 + (index-1)%(products-1)
		key := fmt.Sprintf("perf-seed-cancel-%06d", index)
		hash := sha256.Sum256([]byte(key))
		price := 100 + productID%1000
		if _, err := orderStatement.ExecContext(ctx, index, userIndex+1, fmt.Sprintf("PERFC%08d", index), key, hash[:], price, now, now); err != nil {
			return manifest{}, err
		}
		if _, err := itemStatement.ExecContext(ctx, index, productID, fmt.Sprintf("PERF-%06d", productID), fmt.Sprintf("Synthetic Product %06d", productID), price); err != nil {
			return manifest{}, err
		}
		deductions[productID]++
		result.CancellationOrders = append(result.CancellationOrders, manifestOrder{ID: uint64(index), UserIndex: userIndex, ProductID: uint64(productID)})
	}
	for productID, quantity := range deductions {
		if _, err := tx.ExecContext(ctx, `UPDATE inventories SET available_qty=available_qty-? WHERE product_id=? AND available_qty>=?`, quantity, productID, quantity); err != nil {
			return manifest{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return manifest{}, err
	}
	return result, nil
}

func safeCategory(err error) string {
	if err == nil {
		return "none"
	}
	return "invalid_configuration_or_database_error"
}
