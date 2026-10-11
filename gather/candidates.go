// Package gather generates candidate domains and seeds them into the database.
// Seeding runs at about 57k rows/sec, checking is what takes months.
package gather

import (
	"fmt"
	"iter"
	"strings"
)

// Alphabet is the default label alphabet. Digits and hyphens are left out, they
// take 5 character labels from 11.9M to 60M.
const Alphabet = "abcdefghijklmnopqrstuvwxyz"

// Spec is every label of length MinLen..MaxLen over Alphabet, joined to each
// TLD.
type Spec struct {
	TLDs     []string
	MinLen   int
	MaxLen   int
	Alphabet string
}

// Validate reports whether the spec describes a usable space.
func (s Spec) Validate() error {
	switch {
	case len(s.TLDs) == 0:
		return fmt.Errorf("no TLDs given")
	case s.MinLen < 1:
		return fmt.Errorf("min length %d: must be at least 1", s.MinLen)
	case s.MaxLen < s.MinLen:
		return fmt.Errorf("max length %d is below min length %d", s.MaxLen, s.MinLen)
	case s.MaxLen > 63:
		// RFC 1035 caps a label at 63 octets
		return fmt.Errorf("max length %d: a DNS label is at most 63 characters", s.MaxLen)
	}

	// NOTE: RDAP would block an illegal label for good
	for _, r := range s.alphabet() {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("alphabet contains %q: labels allow only a-z, 0-9 and '-'", r)
		}
	}

	for _, tld := range s.TLDs {
		if tld == "" || strings.ContainsAny(tld, ".") {
			return fmt.Errorf("bad TLD %q", tld)
		}
	}

	if s.alphabet() == "" {
		return fmt.Errorf("empty alphabet")
	}
	return nil
}

func (s Spec) alphabet() string {
	if s.Alphabet != "" {
		return s.Alphabet
	}
	return Alphabet
}

// MaxCount caps a spec's size, well short of overflowing int64. All 1-5
// character labels across three TLDs is 37 million.
const MaxCount = int64(1) << 40 // ~1.1 trillion

// Count returns how many candidates All yields, or an error past MaxCount.
func (s Spec) Count() (int64, error) {
	alpha := int64(len(s.alphabet()))
	tlds := int64(len(s.TLDs))

	var total int64
	for n := s.MinLen; n <= s.MaxLen; n++ {
		combos := int64(1)
		for range n {
			combos *= alpha
			if combos > MaxCount {
				return 0, fmt.Errorf("labels of %d characters over a %d-character alphabet exceed the %d candidate limit", n, alpha, MaxCount)
			}
		}
		total += combos
	}

	if tlds > 0 && total > MaxCount/tlds {
		return 0, fmt.Errorf("%d TLDs x %d labels exceeds the %d candidate limit", tlds, total, MaxCount)
	}
	return total * tlds, nil
}

// All yields every candidate, shortest first, then in lexicographic order so a
// resumed run repeats the same sequence.
func (s Spec) All() iter.Seq[string] {
	return func(yield func(string) bool) {
		alpha := s.alphabet()
		for n := s.MinLen; n <= s.MaxLen; n++ {
			label := make([]byte, n)
			if !emit(alpha, label, 0, s.TLDs, yield) {
				return
			}
		}
	}
}

// emit fills label left to right, yielding a domain per TLD at each leaf.
func emit(alpha string, label []byte, pos int, tlds []string, yield func(string) bool) bool {
	if pos == len(label) {
		for _, tld := range tlds {
			if !yield(string(label) + "." + tld) {
				return false
			}
		}
		return true
	}

	for i := 0; i < len(alpha); i++ {
		label[pos] = alpha[i]
		if !emit(alpha, label, pos+1, tlds, yield) {
			return false
		}
	}
	return true
}
