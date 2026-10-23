// Command perf-verify performs read-only consistency checks against an exact
// order_api_perf_* database and emits aggregate, non-sensitive JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"internshiplab/order-api/internal/platform/config"
	"internshiplab/order-api/internal/platform/database"
)

var performanceDatabase = regexp.MustCompile(`^order_api_perf_[a-z0-9_]+$`)

type report struct {
	Products, Users, Sessions, Orders, OrderItems, CreatedOrders, CancelledOrders              int64
	InventoryUnits                                                                             uint64 `json:"inventory_units"`
	NegativeInventory, AmountMismatches, OrdersWithoutItems, OrphanItems, DuplicateIdempotency int64
	Valid                                                                                      bool `json:"valid"`
}

func main() {
	flags := flag.NewFlagSet("perf-verify", flag.ExitOnError)
	expected := flags.String("expected-database", "", "exact performance database name")
	flags.Parse(os.Args[1:])
	cfg, err := config.Load()
	if err != nil || cfg.Database.Name != *expected || !performanceDatabase.MatchString(cfg.Database.Name) {
		fmt.Fprintln(os.Stderr, "performance verification failed: invalid_configuration")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.OpenMySQL(ctx, cfg.Database)
	if err != nil {
		fail()
	}
	defer db.Close()
	var result report
	queries := []struct {
		destination *int64
		query       string
	}{
		{&result.Products, `SELECT COUNT(*) FROM products`}, {&result.Users, `SELECT COUNT(*) FROM users`},
		{&result.Sessions, `SELECT COUNT(*) FROM auth_sessions`}, {&result.Orders, `SELECT COUNT(*) FROM orders`},
		{&result.OrderItems, `SELECT COUNT(*) FROM order_items`}, {&result.CreatedOrders, `SELECT COUNT(*) FROM orders WHERE status='CREATED'`},
		{&result.CancelledOrders, `SELECT COUNT(*) FROM orders WHERE status='CANCELLED'`}, {&result.NegativeInventory, `SELECT COUNT(*) FROM inventories WHERE available_qty < 0`},
		{&result.AmountMismatches, `SELECT COUNT(*) FROM (SELECT o.id FROM orders o JOIN order_items oi ON oi.order_id=o.id GROUP BY o.id,o.total_amount_cents HAVING o.total_amount_cents <> SUM(oi.unit_price_cents*oi.quantity)) x`},
		{&result.OrdersWithoutItems, `SELECT COUNT(*) FROM orders o LEFT JOIN order_items oi ON oi.order_id=o.id WHERE oi.order_id IS NULL`},
		{&result.OrphanItems, `SELECT COUNT(*) FROM order_items oi LEFT JOIN orders o ON o.id=oi.order_id LEFT JOIN products p ON p.id=oi.product_id WHERE o.id IS NULL OR p.id IS NULL`},
		{&result.DuplicateIdempotency, `SELECT COUNT(*) FROM (SELECT user_id,idempotency_key FROM orders WHERE user_id IS NOT NULL GROUP BY user_id,idempotency_key HAVING COUNT(*)>1) x`},
	}
	for _, item := range queries {
		if err := db.QueryRowContext(ctx, item.query).Scan(item.destination); err != nil {
			fail()
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(available_qty),0) FROM inventories`).Scan(&result.InventoryUnits); err != nil {
		fail()
	}
	result.Valid = result.NegativeInventory == 0 && result.AmountMismatches == 0 && result.OrdersWithoutItems == 0 && result.OrphanItems == 0 && result.DuplicateIdempotency == 0 && result.Orders == result.OrderItems
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail()
	}
	if !result.Valid {
		os.Exit(2)
	}
}

func fail() {
	fmt.Fprintln(os.Stderr, "performance verification failed: database_or_consistency_error")
	os.Exit(1)
}
