package data

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khinshankhan/nomex/data/sqlcgen"
	"github.com/khinshankhan/nomex/data/sqltime"
)

// listAll lists with no limit unless p sets one.
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

// a stored "-04:00" offset only works because queries wrap it in datetime()
func TestTimestampComparisonsHonourOffsets(t *testing.T) {
	db := openTest(t)
	ctx := t.Context()

	// raw text, skipping sqltime. 3h behind UTC on the wall clock, so as plain
	// text it sorts before datetime('now')
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

	due, err := db.DueSuffixes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("DueSuffixes returned %v, want nothing due", due)
	}
}

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

// namedQuery returns the SQL for one query in queries/checks.sql.
func namedQuery(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile("../queries/checks.sql")
	if err != nil {
		t.Fatalf("read queries: %v", err)
	}
	_, rest, found := strings.Cut(string(b), "-- name: "+name+" ")
	if !found {
		t.Fatalf("no query named %s", name)
	}
	stmt, _, _ := strings.Cut(rest, ";")

	var sb strings.Builder
	for _, line := range strings.Split(stmt, "\n")[1:] {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			sb.WriteString(line + "\n")
		}
	}
	return sb.String()
}

// without its index the claim sorts every due row: 270-350ms per batch at 2M
// rows, against 0.5ms
func TestClaimUsesItsIndex(t *testing.T) {
	db := openTest(t)

	rows, err := db.sql.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+namedQuery(t, "DueChecksForSuffix"), "dev", 100)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan = append(plan, detail)
	}
	got := strings.Join(plan, "\n")

	if !strings.Contains(got, "idx_checks_suffix_work") {
		t.Errorf("claim doesn't use idx_checks_suffix_work:\n%s", got)
	}
	if strings.Contains(got, "TEMP B-TREE") {
		t.Errorf("claim sorts instead of reading in index order:\n%s", got)
	}
}
