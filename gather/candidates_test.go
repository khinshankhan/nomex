package gather

import (
	"slices"
	"strings"
	"testing"
)

// mustCount fails the test rather than returning an error, so assertions stay
// readable.
func mustCount(t *testing.T, s Spec) int64 {
	t.Helper()
	n, err := s.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	return n
}

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
			if got := mustCount(t, tt.spec); got != tt.want {
				t.Errorf("Count() = %d, want %d", got, tt.want)
			}

			// Count is used to size a run before committing to it, so it has
			// to agree with what All actually produces.
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

// Shortest first, so an interrupted seed has covered the scarce labels.
func TestSpecAllShortestFirst(t *testing.T) {
	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 3}

	last := 0
	for d := range spec.All() {
		n := strings.Index(d, ".")
		if n < last {
			t.Fatalf("label length went from %d to %d at %s", last, n, d)
		}
		last = n
	}
}

// The sequence must be identical run to run, or a resumed seed would generate
// different candidates and OR IGNORE would not line up.
func TestSpecAllDeterministic(t *testing.T) {
	spec := Spec{TLDs: []string{"dev", "net"}, MinLen: 1, MaxLen: 2, Alphabet: "abc"}

	if a, b := slices.Collect(spec.All()), slices.Collect(spec.All()); !slices.Equal(a, b) {
		t.Error("All() produced a different sequence on the second call")
	}
}

func TestSpecAllStopsEarly(t *testing.T) {
	spec := Spec{TLDs: []string{"dev"}, MinLen: 1, MaxLen: 5}

	n := 0
	for range spec.All() {
		n++
		if n == 3 {
			break
		}
	}
	if n != 3 {
		t.Errorf("stopped after %d, want 3", n)
	}
}

// Count must refuse to report a total it cannot represent: it silently went
// negative at 15 characters before the limit was added, and the CLI prints it.
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
