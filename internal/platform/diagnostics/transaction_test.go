package diagnostics

import (
	"testing"
	"time"
)

func TestTransactionCollectorSummarizesAndResets(t *testing.T) {
	collector := NewTransactionCollector(true)
	for _, duration := range []time.Duration{time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond} {
		collector.ObserveTransactionStage("commit", "success", duration)
	}
	snapshot := collector.Snapshot()
	if !snapshot.Enabled || len(snapshot.Series) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	got := snapshot.Series[0]
	if got.Stage != "commit" || got.Outcome != "success" || got.Durations.Count != 3 || got.Durations.P50MS != 2 {
		t.Errorf("series = %+v", got)
	}
	if snapshot.RecorderOverhead.Count != 3 {
		t.Errorf("overhead count = %d, want 3", snapshot.RecorderOverhead.Count)
	}
	collector.Reset()
	if got := collector.Snapshot(); len(got.Series) != 0 || got.RecorderOverhead.Count != 0 {
		t.Errorf("after reset = %+v", got)
	}
}

func TestDisabledTransactionCollectorDoesNotRecord(t *testing.T) {
	collector := NewTransactionCollector(false)
	collector.ObserveTransactionStage("commit", "success", time.Second)
	if got := collector.Snapshot(); got.Enabled || len(got.Series) != 0 {
		t.Fatalf("disabled snapshot = %+v", got)
	}
}
