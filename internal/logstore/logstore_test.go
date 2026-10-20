package logstore

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ts(hhmmss string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-10-21T"+hhmmss+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

var longMsg = strings.Repeat("x", 250)

// fixture writes a tiny dataset. Lines are deliberately out of order in
// one file to check that Open sorts.
func fixture(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"api.jsonl": `{"ts":"2026-10-21T10:00:30Z","service":"api","level":"info","msg":"request completed","route":"GET /","status":200,"latency_ms":50}
{"ts":"2026-10-21T10:01:10Z","service":"api","level":"error","msg":"upstream timeout","status":502}
{"ts":"2026-10-21T10:01:20Z","service":"api","level":"warn","msg":"slow request"}
{"ts":"2026-10-21T10:02:05Z","service":"api","level":"error","msg":"upstream timeout","status":503}
`,
		"db.jsonl": `{"ts":"2026-10-21T10:01:40Z","service":"db","level":"error","msg":"Pool EXHAUSTED","pool_in_use":5,"pool_size":5}

{"ts":"2026-10-21T10:01:05Z","service":"db","level":"error","msg":"` + longMsg + `"}
{"ts":"2026-10-21T10:02:30Z","service":"db","level":"error","msg":"pool exhausted","trace_id":"abc"}
`,
		"deploys.jsonl": `{"ts":"2026-10-21T10:00:00Z","service":"db","version":"v2","change":"shrink pool"}
{"ts":"2026-10-21T09:00:00Z","service":"api","version":"v1","change":"initial"}
`,
		"README.md": "not a log file",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOpen(t *testing.T) {
	s := fixture(t)
	if len(s.entries) != 7 || len(s.deploys) != 2 {
		t.Fatalf("loaded %d entries, %d deploys; want 7, 2", len(s.entries), len(s.deploys))
	}
	for i := 1; i < len(s.entries); i++ {
		if s.entries[i].Time.Before(s.entries[i-1].Time) {
			t.Fatal("entries not sorted")
		}
	}
	if e := s.entries[0]; e.Route != "GET /" || e.Status != 200 || e.LatencyMS != 50 {
		t.Errorf("fields not decoded: %+v", e)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("empty dir: want error")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.jsonl"), []byte("{not json\n"), 0o644)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("bad json: got %v, want error naming the line", err)
	}
}

func TestServices(t *testing.T) {
	s := fixture(t)
	got := s.Services()
	if !reflect.DeepEqual(got, []string{"api", "db"}) {
		t.Fatalf("Services() = %v", got)
	}
	got[0] = "mutated"
	if s.Services()[0] != "api" {
		t.Error("Services() exposes internal slice")
	}
}

func msgs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Service+":"+e.Msg)
	}
	return out
}

func TestSearchFilters(t *testing.T) {
	s := fixture(t)
	from, to := ts("10:00:00"), ts("11:00:00")
	cases := []struct {
		name string
		p    SearchParams
		want []string
	}{
		{"all", SearchParams{From: from, To: to}, []string{"api:request completed", "db:" + longMsg[:197] + "...", "api:upstream timeout", "api:slow request", "db:Pool EXHAUSTED", "api:upstream timeout", "db:pool exhausted"}},
		{"service", SearchParams{Service: "db", From: from, To: to}, []string{"db:" + longMsg[:197] + "...", "db:Pool EXHAUSTED", "db:pool exhausted"}},
		{"level", SearchParams{Level: "warn", From: from, To: to}, []string{"api:slow request"}},
		{"text case-insensitive", SearchParams{Text: "pool exh", From: from, To: to}, []string{"db:Pool EXHAUSTED", "db:pool exhausted"}},
		{"combined", SearchParams{Service: "api", Level: "error", Text: "TIMEOUT", From: from, To: to}, []string{"api:upstream timeout", "api:upstream timeout"}},
		{"from inclusive, to exclusive", SearchParams{From: ts("10:01:10"), To: ts("10:02:05")}, []string{"api:upstream timeout", "api:slow request", "db:Pool EXHAUSTED"}},
		{"empty range", SearchParams{From: from, To: from}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, trunc, err := s.Search(c.p)
			if err != nil {
				t.Fatal(err)
			}
			if trunc {
				t.Error("unexpected truncated")
			}
			if got == nil {
				t.Error("want non-nil slice (encodes as [] not null)")
			}
			if !reflect.DeepEqual(msgs(got), c.want) {
				t.Errorf("got %q\nwant %q", msgs(got), c.want)
			}
		})
	}
}

func TestSearchMsgTruncation(t *testing.T) {
	s := fixture(t)
	got, _, _ := s.Search(SearchParams{Text: "xxx", From: ts("10:00:00"), To: ts("11:00:00")})
	if len(got) != 1 || len([]rune(got[0].Msg)) != MaxMsgLen || !strings.HasSuffix(got[0].Msg, "...") {
		t.Fatalf("long msg not truncated to %d: %d", MaxMsgLen, len(got[0].Msg))
	}
	if len(s.entries[1].Msg) != 250 {
		t.Error("truncation mutated stored entry")
	}
}

func TestSearchLimit(t *testing.T) {
	s := fixture(t)
	from, to := ts("10:00:00"), ts("11:00:00")
	got, trunc, _ := s.Search(SearchParams{From: from, To: to, Limit: 2})
	if len(got) != 2 || !trunc {
		t.Fatalf("limit 2: got %d, truncated=%v", len(got), trunc)
	}
	if got[0].Msg != "request completed" {
		t.Error("limit should keep the earliest entries")
	}
	// Exactly as many matches as the limit: not truncated.
	if got, trunc, _ := s.Search(SearchParams{From: from, To: to, Limit: 7}); len(got) != 7 || trunc {
		t.Errorf("limit 7: got %d, truncated=%v", len(got), trunc)
	}
}

func TestSearchLimitDefaultsAndMax(t *testing.T) {
	s := &Store{services: []string{"a"}}
	base := ts("10:00:00")
	for i := range 60 {
		s.entries = append(s.entries, Entry{Time: base.Add(time.Duration(i) * time.Second), Service: "a", Level: "info"})
	}
	for _, c := range []struct{ limit, want int }{{0, DefaultLimit}, {-3, DefaultLimit}, {10, 10}, {50, 50}, {500, MaxLimit}} {
		got, trunc, _ := s.Search(SearchParams{From: base, To: base.Add(time.Hour), Limit: c.limit})
		if len(got) != c.want || !trunc {
			t.Errorf("limit %d: got %d (truncated=%v), want %d", c.limit, len(got), trunc, c.want)
		}
	}
}

func TestRangeErrors(t *testing.T) {
	s := fixture(t)
	a := ts("10:00:00")
	bad := []struct {
		name     string
		from, to time.Time
		want     string
	}{
		{"to before from", a, a.Add(-time.Second), "before"},
		{"wider than 6h", a, a.Add(6*time.Hour + time.Second), "too wide"},
		{"missing from", time.Time{}, a, "required"},
		{"missing to", a, time.Time{}, "required"},
	}
	for _, c := range bad {
		_, _, err1 := s.Search(SearchParams{From: c.from, To: c.to})
		_, err2 := s.ErrorSummary(c.from, c.to, 0)
		_, err3 := s.Deploys(c.from, c.to)
		for i, err := range []error{err1, err2, err3} {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s (fn %d): got %v, want error containing %q", c.name, i, err, c.want)
			}
		}
	}
	if _, _, err := s.Search(SearchParams{From: a, To: a.Add(6 * time.Hour)}); err != nil {
		t.Errorf("exactly 6h should be allowed: %v", err)
	}
}

func TestSearchUnknownServiceOrLevel(t *testing.T) {
	s := fixture(t)
	a := ts("10:00:00")
	if _, _, err := s.Search(SearchParams{Service: "nope", From: a, To: a.Add(time.Hour)}); err == nil || !strings.Contains(err.Error(), "api, db") {
		t.Errorf("unknown service: %v", err)
	}
	if _, _, err := s.Search(SearchParams{Level: "fatal", From: a, To: a.Add(time.Hour)}); err == nil {
		t.Error("unknown level: want error")
	}
}

func TestErrorSummary(t *testing.T) {
	s := fixture(t)
	got, err := s.ErrorSummary(ts("10:00:00"), ts("11:00:00"), 0)
	if err != nil {
		t.Fatal(err)
	}
	wantMinutes := []MinuteBucket{
		{Minute: ts("10:01:00"), Service: "api", Errors: 1, Warns: 1},
		{Minute: ts("10:01:00"), Service: "db", Errors: 2},
		{Minute: ts("10:02:00"), Service: "api", Errors: 1},
		{Minute: ts("10:02:00"), Service: "db", Errors: 1},
	}
	if !reflect.DeepEqual(got.ByMinute, wantMinutes) {
		t.Errorf("ByMinute = %+v", got.ByMinute)
	}
	wantTop := []MessageCount{
		{Service: "api", Msg: "upstream timeout", Count: 2},
		{Service: "db", Msg: "Pool EXHAUSTED", Count: 1}, // ties broken by msg
		{Service: "db", Msg: "pool exhausted", Count: 1},
		{Service: "db", Msg: longMsg[:197] + "...", Count: 1},
	}
	if !reflect.DeepEqual(got.TopMessages, wantTop) {
		t.Errorf("TopMessages = %+v", got.TopMessages)
	}
	if !reflect.DeepEqual(got.TotalErrors, map[string]int{"api": 2, "db": 3}) {
		t.Errorf("TotalErrors = %v", got.TotalErrors)
	}
	if !got.From.Equal(ts("10:00:00")) || !got.To.Equal(ts("11:00:00")) {
		t.Error("From/To not echoed")
	}
}

func TestErrorSummaryTopNAndEmpty(t *testing.T) {
	s := fixture(t)
	got, _ := s.ErrorSummary(ts("10:00:00"), ts("11:00:00"), 1)
	if len(got.TopMessages) != 2 || got.TopMessages[1].Msg != "Pool EXHAUSTED" {
		t.Errorf("topN=1: %+v", got.TopMessages)
	}
	quiet, _ := s.ErrorSummary(ts("10:00:00"), ts("10:01:00"), 0)
	if len(quiet.ByMinute) != 0 || quiet.ByMinute == nil || quiet.TopMessages == nil {
		t.Errorf("quiet range: %+v", quiet)
	}
	if !reflect.DeepEqual(quiet.TotalErrors, map[string]int{"api": 0, "db": 0}) {
		t.Errorf("quiet TotalErrors should list every service with 0: %v", quiet.TotalErrors)
	}
}

func TestDeploys(t *testing.T) {
	s := fixture(t)
	all, err := s.Deploys(ts("08:00:00"), ts("12:00:00"))
	if err != nil || len(all) != 2 || all[0].Version != "v1" || all[1].Version != "v2" {
		t.Fatalf("Deploys = %+v, %v (want v1, v2 ascending)", all, err)
	}
	// to is exclusive, from inclusive.
	if got, _ := s.Deploys(ts("09:00:00"), ts("10:00:00")); len(got) != 1 || got[0].Version != "v1" {
		t.Errorf("half-open range: %+v", got)
	}
	if got, _ := s.Deploys(ts("11:00:00"), ts("12:00:00")); got == nil || len(got) != 0 {
		t.Errorf("no deploys: want empty non-nil slice, got %#v", got)
	}
}
