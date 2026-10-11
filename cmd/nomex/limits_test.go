package main

import (
	"math"
	"testing"
	"time"
)

func TestLimits(t *testing.T) {
	tests := []struct {
		name      string
		rate      float64
		wantEvery time.Duration
		wantErr   bool
	}{
		{"one a second", 1, time.Second, false},
		{"ten a second", 10, 100 * time.Millisecond, false},
		{"one every two seconds", 0.5, 2 * time.Second, false},
		{"zero", 0, 0, true},
		{"negative", -1, 0, true},
		{"infinite", math.Inf(1), 0, true},
		{"not a number", math.NaN(), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := limits(tt.rate, 3)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error=%v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Every != tt.wantEvery || got.Burst != 3 {
				t.Errorf("got every=%v burst=%d, want every=%v burst=3", got.Every, got.Burst, tt.wantEvery)
			}
		})
	}
}
