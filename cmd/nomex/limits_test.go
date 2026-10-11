package main

import (
	"flag"
	"testing"
	"time"
)

func TestLimitFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantEvery time.Duration
		wantBurst int
		wantErr   bool
	}{
		// zero is unset, so the sweep can use a registry's measured budget
		{"unset", nil, 0, 0, false},
		{"rate 1 is explicit", []string{"-rate", "1"}, time.Second, 0, false},
		{"ten a second", []string{"-rate", "10"}, 100 * time.Millisecond, 0, false},
		{"one every two seconds", []string{"-rate", "0.5"}, 2 * time.Second, 0, false},
		{"burst alone", []string{"-burst", "5"}, 0, 5, false},
		{"zero rate", []string{"-rate", "0"}, 0, 0, true},
		{"negative rate", []string{"-rate", "-1"}, 0, 0, true},
		{"infinite rate", []string{"-rate", "+Inf"}, 0, 0, true},
		{"rate not a number", []string{"-rate", "NaN"}, 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			limits := limitFlags(flags)
			if err := flags.Parse(tt.args); err != nil {
				t.Fatalf("parse: %v", err)
			}

			got, err := limits()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error=%v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Every != tt.wantEvery || got.Burst != tt.wantBurst {
				t.Errorf("got every=%v burst=%d, want every=%v burst=%d",
					got.Every, got.Burst, tt.wantEvery, tt.wantBurst)
			}
		})
	}
}
