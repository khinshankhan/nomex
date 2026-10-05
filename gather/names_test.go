package gather

import (
	"slices"
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		tlds  []string
		want  []string
		ok    bool
	}{
		{"bare label joins each TLD", []string{"matterwave"}, []string{"com", "dev"}, []string{"matterwave.com", "matterwave.dev"}, true},
		{"suffixed name taken as written", []string{"standingwave.dev"}, []string{"com"}, []string{"standingwave.dev"}, true},
		{"normalised", []string{"  MatterWave.COM. "}, nil, []string{"matterwave.com"}, true},
		{"duplicates keep first", []string{"b", "a", "b.com"}, []string{"com"}, []string{"b.com", "a.com"}, true},
		{"blank skipped", []string{"", "  "}, []string{"com"}, nil, true},
		{"bare label without TLDs", []string{"matterwave"}, nil, nil, false},
		{"leading hyphen", []string{"-wave"}, []string{"com"}, nil, false},
		{"empty label", []string{"wave..com"}, nil, nil, false},
		{"non-ascii", []string{"café"}, []string{"com"}, nil, false},
		{"space inside", []string{"matter wave"}, []string{"com"}, nil, false},
		{"bad TLD", []string{"wave"}, []string{"c_m"}, nil, false},
		{"label too long", []string{strings.Repeat("a", 64)}, []string{"com"}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Names(tt.names, tt.tlds)
			if (err == nil) != tt.ok {
				t.Fatalf("Names() error = %v, want ok=%v", err, tt.ok)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Names() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadNamesSkipsBlanksAndComments(t *testing.T) {
	in := "matterwave\n\n# shortlist\nstandingwave.dev  # maybe\n   \n"

	got, err := ReadNames(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ReadNames: %v", err)
	}
	want := []string{"matterwave", "standingwave.dev"}
	if !slices.Equal(got, want) {
		t.Errorf("ReadNames() = %v, want %v", got, want)
	}
}

// Seeding a chosen list should leave existing rows alone, same as Seed.
func TestSeedDomainsIsIncremental(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()

	n, err := SeedDomains(ctx, db, []string{"a.com", "b.com"}, 0)
	if err != nil {
		t.Fatalf("SeedDomains: %v", err)
	}
	if n != 2 {
		t.Fatalf("first seed inserted %d, want 2", n)
	}

	n, err = SeedDomains(ctx, db, []string{"b.com", "c.com"}, 0)
	if err != nil {
		t.Fatalf("SeedDomains: %v", err)
	}
	if n != 1 {
		t.Errorf("second seed inserted %d, want 1", n)
	}
	if got := count(t, db); got != 3 {
		t.Errorf("rows = %d, want 3", got)
	}
}
