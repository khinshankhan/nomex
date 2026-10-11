package data

import (
	"slices"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// listAll is the "no filters" call the list command makes by default.
func listAll(t *testing.T, db *DB, p sqlcgen.ListChecksParams) []sqlcgen.ListChecksRow {
	t.Helper()
	if p.Lim == 0 {
		p.Lim = -1
	}
	rows, err := db.ListChecks(t.Context(), p)
	if err != nil {
		t.Fatalf("ListChecks: %v", err)
	}
	return rows
}

func TestListChecksFilters(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	rows := []struct {
		domain string
		status string
		fresh  time.Time
	}{
		{"a.dev", "not_found", future},
		{"bb.dev", "not_found", future},
		{"ccc.dev", "not_found", past}, // stale
		{"d.net", "not_found", future},
		{"e.dev", "registered", future},
	}
	for _, r := range rows {
		if err := db.UpsertCheck(ctx, sqlcgen.UpsertCheckParams{
			Domain: r.domain, Status: r.status, FreshUntil: sqltime.At(r.fresh),
		}); err != nil {
			t.Fatalf("seed %s: %v", r.domain, err)
		}
	}

	tests := []struct {
		name   string
		params sqlcgen.ListChecksParams
		want   []string
	}{
		{
			name:   "no filters returns everything",
			params: sqlcgen.ListChecksParams{},
			want:   []string{"a.dev", "d.net", "e.dev", "bb.dev", "ccc.dev"},
		},
		{
			name:   "by status",
			params: sqlcgen.ListChecksParams{Status: "registered"},
			want:   []string{"e.dev"},
		},
		{
			name:   "by suffix",
			params: sqlcgen.ListChecksParams{Suffix: "net"},
			want:   []string{"d.net"},
		},
		{
			name:   "by label length",
			params: sqlcgen.ListChecksParams{LabelLen: 2},
			want:   []string{"bb.dev"},
		},
		{
			name:   "combined",
			params: sqlcgen.ListChecksParams{Status: "not_found", Suffix: "dev", LabelLen: 1},
			want:   []string{"a.dev"},
		},
		{
			// an old answer is still an answer, listed unless excluded
			name:   "fresh only omits lapsed answers",
			params: sqlcgen.ListChecksParams{Status: "not_found", FreshOnly: 1},
			want:   []string{"a.dev", "d.net", "bb.dev"},
		},
		{
			name:   "limit",
			params: sqlcgen.ListChecksParams{Status: "not_found", Lim: 2},
			want:   []string{"a.dev", "d.net"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listAll(t, db, tt.params)
			if len(got) != len(tt.want) {
				var names []string
				for _, r := range got {
					names = append(names, r.Domain)
				}
				t.Fatalf("got %v, want %v", names, tt.want)
			}
			for i, r := range got {
				if r.Domain != tt.want[i] {
					t.Errorf("row %d = %q, want %q", i, r.Domain, tt.want[i])
				}
			}
		})
	}
}

// The stale flag tells a caller how much to trust the row.
func TestListChecksReportsStaleness(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	if err := db.UpsertCheck(ctx, sqlcgen.UpsertCheckParams{
		Domain: "fresh.dev", Status: "not_found", FreshUntil: sqltime.At(time.Now().Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCheck(ctx, sqlcgen.UpsertCheckParams{
		Domain: "lapsed.dev", Status: "not_found", FreshUntil: sqltime.At(time.Now().Add(-time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}

	for _, r := range listAll(t, db, sqlcgen.ListChecksParams{}) {
		want := r.Domain == "lapsed.dev"
		if r.Stale != want {
			t.Errorf("%s: stale = %v, want %v", r.Domain, r.Stale, want)
		}
	}
}

// SQLite compares DATETIME as text, so a stored "-04:00" offset is ignored and
// an hour from now reads as hours stale unless wrapped in datetime().
func TestTimestampComparisonsHonourOffsets(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	// written as text so it skips sqltime's UTC normalising, like a manual
	// UPDATE would. the wall clock reads 3h behind UTC, so as plain text it
	// sorts before datetime('now')
	west := time.FixedZone("UTC-4", -4*3600)
	future := time.Now().Add(time.Hour).In(west).Format("2006-01-02 15:04:05-07:00")
	if err := db.Exec(ctx,
		`INSERT INTO checks (domain, status, fresh_until) VALUES ('future.dev', 'not_found', ?)`,
		future); err != nil {
		t.Fatal(err)
	}

	rows := listAll(t, db, sqlcgen.ListChecksParams{})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Stale {
		t.Errorf("%s, an hour from now, was reported stale", future)
	}

	// and the sweep shouldn't claim it
	due, err := db.DueChecks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("DueChecks returned %v, want nothing due", due)
	}
}

// The sweep claims work per suffix: due rows only, never blocked ones, and a
// live request (higher priority) ahead of the background sweep, then oldest
// first.
func TestClaimQueries(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	rows := []struct {
		domain   string
		fresh    time.Time
		priority int64
		queued   string
	}{
		{"a.dev", past, 0, "2026-01-02 00:00:00"},
		{"b.dev", past, 100, "2026-01-03 00:00:00"},
		{"c.dev", past, 0, "2026-01-01 00:00:00"},
		{"d.dev", future, 100, "2026-01-01 00:00:00"},
		{"e.dev", past, 100, "2026-01-01 00:00:00"},
		{"f.com", past, 0, "2026-01-01 00:00:00"},
	}
	for _, r := range rows {
		if err := db.Exec(ctx,
			`INSERT INTO checks (domain, status, fresh_until, priority, queued_at) VALUES (?, 'unchecked', ?, ?, ?)`,
			r.domain, sqltime.At(r.fresh), r.priority, r.queued); err != nil {
			t.Fatalf("seed %s: %v", r.domain, err)
		}
	}
	if err := db.BlockDomain(ctx, sqlcgen.BlockDomainParams{
		Domain: "e.dev", Reason: "no RDAP server published for this suffix", BlockedAt: sqltime.Now(),
	}); err != nil {
		t.Fatalf("block: %v", err)
	}

	claims := []struct {
		name   string
		suffix string
		limit  int64
		want   []string
	}{
		{"priority, then oldest; fresh and blocked skipped", "dev", 10, []string{"b.dev", "c.dev", "a.dev"}},
		{"limited", "dev", 2, []string{"b.dev", "c.dev"}},
		{"other suffix", "com", 10, []string{"f.com"}},
		{"unknown suffix", "net", 10, nil},
	}
	for _, tt := range claims {
		t.Run(tt.name, func(t *testing.T) {
			got, err := db.DueChecksForSuffix(ctx, sqlcgen.DueChecksForSuffixParams{Suffix: tt.suffix, Limit: tt.limit})
			if err != nil {
				t.Fatalf("DueChecksForSuffix: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("claimed %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("suffixes with work, most due first", func(t *testing.T) {
		got, err := db.DueSuffixes(ctx)
		if err != nil {
			t.Fatalf("DueSuffixes: %v", err)
		}
		want := []sqlcgen.DueSuffixesRow{{Suffix: "dev", Due: 3}, {Suffix: "com", Due: 1}}
		if !slices.Equal(got, want) {
			t.Errorf("DueSuffixes = %v, want %v", got, want)
		}
	})
}
