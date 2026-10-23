package diagnostics

import (
	"sort"
	"sync"
	"time"
)

const maxTransactionSamplesPerSeries = 100_000

type transactionSeriesKey struct {
	stage   string
	outcome string
}

type transactionSeries struct {
	durationsNS []int64
	dropped     uint64
}

// TransactionCollector is an opt-in, in-memory diagnostic collector. It keeps
// only stage names, result categories, and durations. Samples are bounded so a
// forgotten local diagnostic process cannot grow memory without limit.
type TransactionCollector struct {
	enabled bool

	mu        sync.Mutex
	resetAt   time.Time
	series    map[transactionSeriesKey]*transactionSeries
	overhead  []int64
	droppedOH uint64
}

type transactionSnapshot struct {
	Enabled             bool                       `json:"enabled"`
	ResetAtUTC          string                     `json:"reset_at_utc"`
	CapturedAtUTC       string                     `json:"captured_at_utc"`
	Series              []transactionSeriesSummary `json:"series"`
	RecorderOverhead    durationSummary            `json:"recorder_overhead"`
	NestedDurationsNote string                     `json:"nested_durations_note"`
}

type transactionSeriesSummary struct {
	Stage     string          `json:"stage"`
	Outcome   string          `json:"outcome"`
	Durations durationSummary `json:"durations"`
}

type durationSummary struct {
	Count          int     `json:"count"`
	DroppedSamples uint64  `json:"dropped_samples"`
	TotalMS        float64 `json:"total_ms"`
	AverageMS      float64 `json:"average_ms"`
	P50MS          float64 `json:"p50_ms"`
	P95MS          float64 `json:"p95_ms"`
	P99MS          float64 `json:"p99_ms"`
	MaxMS          float64 `json:"max_ms"`
}

func NewTransactionCollector(enabled bool) *TransactionCollector {
	return &TransactionCollector{
		enabled: enabled,
		resetAt: time.Now().UTC(),
		series:  make(map[transactionSeriesKey]*transactionSeries),
	}
}

func (c *TransactionCollector) Enabled() bool { return c != nil && c.enabled }

// ObserveTransactionStage uses Go's monotonic duration and measures its own
// collection overhead separately. It intentionally performs no logging or I/O.
func (c *TransactionCollector) ObserveTransactionStage(stage, outcome string, duration time.Duration) {
	if !c.Enabled() {
		return
	}
	overheadStarted := time.Now()
	c.mu.Lock()
	key := transactionSeriesKey{stage: stage, outcome: outcome}
	series := c.series[key]
	if series == nil {
		series = &transactionSeries{}
		c.series[key] = series
	}
	if len(series.durationsNS) < maxTransactionSamplesPerSeries {
		series.durationsNS = append(series.durationsNS, duration.Nanoseconds())
	} else {
		series.dropped++
	}
	overhead := time.Since(overheadStarted).Nanoseconds()
	if len(c.overhead) < maxTransactionSamplesPerSeries {
		c.overhead = append(c.overhead, overhead)
	} else {
		c.droppedOH++
	}
	c.mu.Unlock()
}

func (c *TransactionCollector) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.resetAt = time.Now().UTC()
	c.series = make(map[transactionSeriesKey]*transactionSeries)
	c.overhead = nil
	c.droppedOH = 0
	c.mu.Unlock()
}

func (c *TransactionCollector) Snapshot() transactionSnapshot {
	now := time.Now().UTC()
	if c == nil {
		return transactionSnapshot{CapturedAtUTC: now.Format(time.RFC3339Nano)}
	}
	c.mu.Lock()
	resetAt := c.resetAt
	keys := make([]transactionSeriesKey, 0, len(c.series))
	values := make(map[transactionSeriesKey]transactionSeries, len(c.series))
	for key, value := range c.series {
		keys = append(keys, key)
		values[key] = transactionSeries{durationsNS: append([]int64(nil), value.durationsNS...), dropped: value.dropped}
	}
	overhead := append([]int64(nil), c.overhead...)
	droppedOH := c.droppedOH
	c.mu.Unlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].stage == keys[j].stage {
			return keys[i].outcome < keys[j].outcome
		}
		return keys[i].stage < keys[j].stage
	})
	summaries := make([]transactionSeriesSummary, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		summaries = append(summaries, transactionSeriesSummary{
			Stage: key.stage, Outcome: key.outcome,
			Durations: summarizeDurations(value.durationsNS, value.dropped),
		})
	}
	return transactionSnapshot{
		Enabled: c.enabled, ResetAtUTC: resetAt.Format(time.RFC3339Nano), CapturedAtUTC: now.Format(time.RFC3339Nano),
		Series: summaries, RecorderOverhead: summarizeDurations(overhead, droppedOH),
		NestedDurationsNote: "http, service, transaction, and SQL stage durations are nested; do not add overlapping totals",
	}
}

func summarizeDurations(values []int64, dropped uint64) durationSummary {
	if len(values) == 0 {
		return durationSummary{DroppedSamples: dropped}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total int64
	for _, value := range sorted {
		total += value
	}
	toMS := func(value int64) float64 { return float64(value) / float64(time.Millisecond) }
	percentile := func(percent float64) int64 {
		index := int(float64(len(sorted)-1) * percent)
		return sorted[index]
	}
	return durationSummary{
		Count: len(sorted), DroppedSamples: dropped, TotalMS: toMS(total), AverageMS: toMS(total) / float64(len(sorted)),
		P50MS: toMS(percentile(0.50)), P95MS: toMS(percentile(0.95)), P99MS: toMS(percentile(0.99)), MaxMS: toMS(sorted[len(sorted)-1]),
	}
}
