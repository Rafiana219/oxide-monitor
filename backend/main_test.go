package main

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"pending to processing", "pending", "processing", true},
		{"pending to failed", "pending", "failed", true},
		{"pending to cancelled", "pending", "cancelled", true},
		{"processing to completed", "processing", "completed", true},
		{"processing to failed", "processing", "failed", true},
		{"processing to cancelled", "processing", "cancelled", true},
		{"completed cannot return to processing", "completed", "processing", false},
		{"failed is terminal", "failed", "processing", false},
		{"cancelled is terminal", "cancelled", "processing", false},
		{"pending cannot complete directly", "pending", "completed", false},
		{"unknown status", "unknown", "processing", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canTransition(tt.from, tt.to)
			if got != tt.want {
				t.Errorf(
					"canTransition(%q, %q) = %v; want %v",
					tt.from, tt.to, got, tt.want,
				)
			}
		})
	}
}
