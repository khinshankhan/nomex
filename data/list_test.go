package data

import (
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
			// The premise of the project: a lapsed answer is still an answer,
			// so it is listed unless explicitly excluded.
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

// The stale flag is what tells a caller how much to trust the row.
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

// SQLite compares DATETIME text lexicographically, so a stored "-04:00" offset
// is not interpreted: a timestamp an hour in the future reads as hours stale
// unless the comparison wraps the column in datetime().
func TestTimestampComparisonsHonourOffsets(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	// Deliberately not UTC. A caller passing a local time must not silently
	// corrupt due-detection.
	local := time.Now().Add(time.Hour)
	if local.Location() == time.UTC {
		t.Skip("machine is on UTC; this bug is invisible here")
	}

	if err := db.UpsertCheck(ctx, sqlcgen.UpsertCheckParams{
		Domain: "future.dev", Status: "not_found", FreshUntil: sqltime.At(local),
	}); err != nil {
		t.Fatal(err)
	}

	rows := listAll(t, db, sqlcgen.ListChecksParams{})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Stale {
		t.Error("a timestamp an hour in the future was reported stale")
	}

	// And the sweep must not claim it as work.
	due, err := db.DueChecks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("DueChecks returned %v, want nothing due", due)
	}
}
