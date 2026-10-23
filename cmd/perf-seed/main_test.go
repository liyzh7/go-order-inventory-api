package main

import "testing"

func TestValidateTargetRequiresExactPerformanceDatabase(t *testing.T) {
	tests := []struct {
		actual, expected string
		ok               bool
	}{
		{"order_api_perf_baseline", "order_api_perf_baseline", true},
		{"order_api", "order_api", false},
		{"order_api_perf_baseline", "order_api_perf_other", false},
		{"order_api_perf_BAD", "order_api_perf_BAD", false},
	}
	for _, test := range tests {
		if got := validateTarget(test.actual, test.expected); (got == nil) != test.ok {
			t.Errorf("validateTarget(%q,%q) error=%v", test.actual, test.expected, got)
		}
	}
}
