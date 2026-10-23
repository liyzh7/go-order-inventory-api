package main

import "testing"

func TestIsDisposableDatabase(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "order_api_it_1234", want: true},
		{name: "order_api_rollback_test", want: true},
		{name: "order_api", want: false},
		{name: "production", want: false},
		{name: "test_order_api", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDisposableDatabase(tt.name); got != tt.want {
				t.Errorf("isDisposableDatabase(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
