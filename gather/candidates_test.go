package gather

import (
	"slices"
	"testing"
)

func TestSpecCount(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want int64
	}{
		{"single length", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 1}, 26},
		{"two lengths", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2}, 26 + 676},
		{"two TLDs", Spec{TLDs: []string{"dev", "net"}, MinLen: 1, MaxLen: 2}, (26 + 676) * 2},
		{"only length 3", Spec{TLDs: []string{"dev"}, MinLen: 3, MaxLen: 3}, 17576},
		{"custom alphabet", Spec{TLDs: []string{"dev"}, MinLen: 2, MaxLen: 2, Alphabet: "ab"}, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.spec.Count()
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if got != tt.want {
				t.Errorf("Count() = %d, want %d", got, tt.want)
			}

			// Count sizes a run up front, so it has to match All
			var n int64
			for range tt.spec.All() {
				n++
			}
			if n != tt.want {
				t.Errorf("All() yielded %d, want %d", n, tt.want)
			}
		})
	}
}

func TestSpecAllOrder(t *testing.T) {
	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2, Alphabet: "ab"}

	got := slices.Collect(spec.All())
	want := []string{"a.dev", "b.dev", "aa.dev", "ab.dev", "ba.dev", "bb.dev"}

	if !slices.Equal(got, want) {
		t.Errorf("All() = %v, want %v", got, want)
	}
}

// Count should refuse totals it can't represent. It used to go negative at 15
// characters and the CLI printed it.
func TestCountRejectsOversizedSpace(t *testing.T) {
	for _, n := range []int{15, 20, 63} {
		spec := Spec{TLDs: []string{"dev"}, MinLen: n, MaxLen: n}
		got, err := spec.Count()
		if err == nil {
			t.Errorf("max=%d: Count() = %d, want an error", n, got)
		}
		if got < 0 {
			t.Errorf("max=%d: Count() returned a negative total %d", n, got)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		ok   bool
	}{
		{"valid", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 3}, true},
		{"no TLDs", Spec{MinLen: 1, MaxLen: 3}, false},
		{"zero min", Spec{TLDs: []string{"dev"}, MinLen: 0, MaxLen: 3}, false},
		{"max below min", Spec{TLDs: []string{"dev"}, MinLen: 3, MaxLen: 2}, false},
		{"label too long", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 64}, false},
		{"dotted TLD", Spec{TLDs: []string{"co.uk"}, MinLen: 1, MaxLen: 2}, false},
		{"empty TLD", Spec{TLDs: []string{""}, MinLen: 1, MaxLen: 2}, false},
		{"whitespace alphabet", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2, Alphabet: " "}, false},
		{"uppercase alphabet", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2, Alphabet: "ABC"}, false},
		{"digits and hyphen", Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 2, Alphabet: "abc0-9"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
