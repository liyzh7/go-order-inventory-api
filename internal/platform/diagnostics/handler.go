// Package diagnostics exposes local-only runtime measurements for controlled
// performance tests. Registration is opt-in and the application HTTP address
// is independently restricted to a literal loopback IP.
package diagnostics

import (
	"database/sql"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

type snapshot struct {
	CapturedAtUTC string        `json:"captured_at_utc"`
	Runtime       runtimeStats  `json:"runtime"`
	Database      databaseStats `json:"database"`
}

type runtimeStats struct {
	Goroutines      int    `json:"goroutines"`
	HeapAllocBytes  uint64 `json:"heap_alloc_bytes"`
	HeapInUseBytes  uint64 `json:"heap_in_use_bytes"`
	StackInUseBytes uint64 `json:"stack_in_use_bytes"`
	SysBytes        uint64 `json:"sys_bytes"`
	NumGC           uint32 `json:"num_gc"`
	PauseTotalNS    uint64 `json:"pause_total_ns"`
}

type databaseStats struct {
	MaxOpenConnections int   `json:"max_open_connections"`
	OpenConnections    int   `json:"open_connections"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"wait_count"`
	WaitDurationNS     int64 `json:"wait_duration_ns"`
	MaxIdleClosed      int64 `json:"max_idle_closed"`
	MaxLifetimeClosed  int64 `json:"max_lifetime_closed"`
}

// Register adds no routes unless the caller explicitly opts in. The endpoint
// contains aggregate counters only; it never exposes SQL, credentials, request
// bodies, tokens, or user data.
func Register(router *gin.Engine, db *sql.DB, transactions *TransactionCollector) {
	router.GET("/debug/runtime", func(c *gin.Context) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		pool := db.Stats()
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, snapshot{
			CapturedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
			Runtime: runtimeStats{
				Goroutines: runtime.NumGoroutine(), HeapAllocBytes: memory.HeapAlloc,
				HeapInUseBytes: memory.HeapInuse, StackInUseBytes: memory.StackInuse,
				SysBytes: memory.Sys, NumGC: memory.NumGC, PauseTotalNS: memory.PauseTotalNs,
			},
			Database: databaseStats{
				MaxOpenConnections: pool.MaxOpenConnections, OpenConnections: pool.OpenConnections,
				InUse: pool.InUse, Idle: pool.Idle, WaitCount: pool.WaitCount,
				WaitDurationNS: pool.WaitDuration.Nanoseconds(), MaxIdleClosed: pool.MaxIdleClosed,
				MaxLifetimeClosed: pool.MaxLifetimeClosed,
			},
		})
	})
	router.GET("/debug/transactions", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, transactions.Snapshot())
	})
	router.POST("/debug/transactions/reset", func(c *gin.Context) {
		transactions.Reset()
		c.Status(http.StatusNoContent)
	})

	// Explicit handlers avoid importing the global DefaultServeMux into the
	// application route tree. Profiles stay on the already loopback-only server.
	router.GET("/debug/pprof/", gin.WrapH(http.HandlerFunc(pprof.Index)))
	router.GET("/debug/pprof/cmdline", gin.WrapH(http.HandlerFunc(pprof.Cmdline)))
	router.GET("/debug/pprof/profile", gin.WrapH(http.HandlerFunc(pprof.Profile)))
	router.POST("/debug/pprof/symbol", gin.WrapH(http.HandlerFunc(pprof.Symbol)))
	router.GET("/debug/pprof/symbol", gin.WrapH(http.HandlerFunc(pprof.Symbol)))
	router.GET("/debug/pprof/trace", gin.WrapH(http.HandlerFunc(pprof.Trace)))
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		router.GET("/debug/pprof/"+name, gin.WrapH(pprof.Handler(name)))
	}
}
