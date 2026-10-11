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

			// has to match All
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

func TestCountLimit(t *testing.T) {
	const limit = int64(1) << 40

	tests := []struct {
		name string
		spec Spec
		want int64 // 0 means an error
	}{
		{"exactly the limit", Spec{TLDs: []string{"dev"}, MinLen: 40, MaxLen: 40, Alphabet: "ab"}, limit},
		{"one length past it", Spec{TLDs: []string{"dev"}, MinLen: 41, MaxLen: 41, Alphabet: "ab"}, 0},
		{"exactly the limit across TLDs", Spec{TLDs: []string{"dev", "net"}, MinLen: 39, MaxLen: 39, Alphabet: "ab"}, limit},
		{"TLDs push it past", Spec{TLDs: []string{"dev", "net"}, MinLen: 40, MaxLen: 40, Alphabet: "ab"}, 0},
		{"15 characters", Spec{TLDs: []string{"dev"}, MinLen: 15, MaxLen: 15}, 0},
		{"63 characters", Spec{TLDs: []string{"dev"}, MinLen: 63, MaxLen: 63}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.spec.Count()
			if tt.want == 0 {
				if err == nil || got != 0 {
					t.Errorf("Count() = %d, %v, want 0 and an error", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("Count() = %d, %v, want %d", got, err, tt.want)
			}
		})
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
