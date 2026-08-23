// Package gather generates candidate domains and seeds them into the database.
//
// Seeding is decoupled from checking. Writing rows is fast -- roughly 57k
// rows/sec measured, so the full 1-5 character space across three TLDs is about
// eleven minutes -- while checking them is bounded by registry rate limits and
// takes months. The queue is expected to stay longer than the sweep can drain.
package gather

import (
	"fmt"
	"iter"
	"strings"
)

// Alphabet is the default label alphabet: lowercase ASCII only.
//
// Digits and hyphens are legal in a hostname label but excluded by default.
// They multiply the space (36 characters instead of 26 means a 5-character
// label goes from 11.9M to 60M) for candidates that are, in practice, less
// interesting to look at.
const Alphabet = "abcdefghijklmnopqrstuvwxyz"

// Spec describes a candidate space: every label of length MinLen..MaxLen over
// Alphabet, joined to each TLD.
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
		// RFC 1035: a label is at most 63 octets.
		return fmt.Errorf("max length %d: a DNS label is at most 63 characters", s.MaxLen)
	}

	// Reject characters that are not legal in a hostname label. Seeding them
	// would produce candidates every RDAP query rejects as ErrInvalidQuery,
	// which is an allowlisted reason to write to blocked -- so a typo here
	// would permanently blocklist a slice of the namespace.
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

// MaxCount caps a spec's size. Above this Count would overflow int64 and
// report a negative total, and no useful run is anywhere near it: the full
// 1-5 character space across three TLDs is 37 million.
const MaxCount = int64(1) << 40 // ~1.1 trillion

// Count returns how many candidates All will yield, or an error if the space is
// too large to represent.
//
// The space grows by the size of the alphabet per character, so one more
// character is 26x the work. Worth calling before committing to a run.
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

// All yields every candidate, shortest labels first.
//
// Order matters for seeding: short labels are the scarce, interesting ones, and
// emitting them first means an interrupted seed has covered the useful part.
// Within a length the order is lexicographic, so a resumed run repeats the same
// sequence and OR IGNORE skips what already landed.
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
