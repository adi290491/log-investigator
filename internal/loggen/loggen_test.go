package loggen

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"log-investigator/internal/logstore"
)

func TestDeterministicBytes(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := Write(a); err != nil {
		t.Fatal(err)
	}
	if err := Write(b); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gateway.jsonl", "orders.jsonl", "payments.jsonl", "deploys.jsonl", "GROUND_TRUTH.md"} {
		x, err := os.ReadFile(filepath.Join(a, name))
		if err != nil {
			t.Fatal(err)
		}
		y, _ := os.ReadFile(filepath.Join(b, name))
		if len(x) == 0 || !bytes.Equal(x, y) {
			t.Errorf("%s differs between runs (or is empty)", name)
		}
	}
}

// The committed data/ must match what the generator produces today.
func TestCommittedDataUpToDate(t *testing.T) {
	for name, want := range Generate().Files() {
		got, err := os.ReadFile(filepath.Join("..", "..", "data", name))
		if err != nil {
			t.Skipf("data/ not generated yet: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("data/%s is stale; run `make data`", name)
		}
	}
}

func TestSortedAndInWindow(t *testing.T) {
	ds := Generate()
	for name, es := range map[string][]logstore.Entry{"gateway": ds.Gateway, "orders": ds.Orders, "payments": ds.Payments} {
		if !slices.IsSortedFunc(es, func(a, b logstore.Entry) int { return a.Time.Compare(b.Time) }) {
			t.Errorf("%s not sorted by time", name)
		}
		for _, e := range es {
			if e.Service != name {
				t.Fatalf("%s file has entry for %q", name, e.Service)
			}
			// Requests started before 14:20 may complete a couple of seconds after it.
			if e.Time.Before(WindowStart) || !e.Time.Before(WindowEnd.Add(3*time.Second)) {
				t.Fatalf("%s entry outside window: %v", name, e.Time)
			}
			if e.Time.Location() != time.UTC {
				t.Fatalf("non-UTC time %v", e.Time)
			}
		}
	}
}

func firstWith(es []logstore.Entry, msg string) time.Time {
	for _, e := range es {
		if e.Msg == msg {
			return e.Time
		}
	}
	return time.Time{}
}

func TestIncidentStartTimes(t *testing.T) {
	ds := Generate()
	for _, c := range []struct {
		name string
		es   []logstore.Entry
		msg  string
		from time.Time
	}{
		{"payments pool exhausted", ds.Payments, MsgPoolExhausted, PaymentErrorsFrom},
		{"payments near capacity", ds.Payments, MsgPoolNearCapacity, PoolWarnFrom},
		{"orders timeout", ds.Orders, MsgPaymentTimeout, OrderErrorsFrom},
		{"gateway upstream timeout", ds.Gateway, MsgUpstreamTimeout, GatewayErrorsFrom},
	} {
		first := firstWith(c.es, c.msg)
		if first.IsZero() {
			t.Errorf("%s: never occurs", c.name)
			continue
		}
		if first.Before(c.from) {
			t.Errorf("%s: first at %v, before %v", c.name, first, c.from)
		}
		// Must start within the specified minute, not just "eventually".
		if !first.Before(c.from.Add(time.Minute)) {
			t.Errorf("%s: first at %v, expected within the minute starting %v", c.name, first, c.from)
		}
	}
}

func TestNoIncidentErrorsBefore1403(t *testing.T) {
	ds := Generate()
	incident := []string{MsgPoolNearCapacity, MsgPoolExhausted, MsgPaymentTimeout, MsgUpstreamTimeout}
	for _, es := range [][]logstore.Entry{ds.Gateway, ds.Orders, ds.Payments} {
		for _, e := range es {
			if e.Time.Before(PoolWarnFrom) && slices.Contains(incident, e.Msg) {
				t.Errorf("incident message before 14:03: %+v", e)
			}
		}
	}
}

func TestPoolSizeChange(t *testing.T) {
	for _, e := range Generate().Payments {
		if e.Msg != "pool stats" {
			continue
		}
		want := 50
		if !e.Time.Before(PaymentsDeployAt) {
			want = 5
		}
		if e.PoolSize != want {
			t.Errorf("pool stats at %v: pool_size %d, want %d", e.Time, e.PoolSize, want)
		}
	}
}

func TestRedHerring(t *testing.T) {
	var warns, errs int
	for _, e := range Generate().Gateway {
		if e.Time.Before(RateLimitFrom) || !e.Time.Before(RateLimitTo) {
			if e.Msg == MsgRateLimited {
				t.Errorf("rate-limit warn outside 13:40-13:47: %v", e.Time)
			}
			continue
		}
		switch e.Level {
		case "warn":
			warns++
		case "error":
			errs++
		}
	}
	if warns < 100 {
		t.Errorf("gateway warns 13:40-13:47 = %d, want >= 100", warns)
	}
	if errs != 0 {
		t.Errorf("gateway errors 13:40-13:47 = %d, want 0", errs)
	}
}

func TestSharedTraceIDs(t *testing.T) {
	ds := Generate()
	ids := func(es []logstore.Entry) map[string]bool {
		m := map[string]bool{}
		for _, e := range es {
			m[e.TraceID] = true
		}
		return m
	}
	gw, ord := ids(ds.Gateway), ids(ds.Orders)
	for _, e := range ds.Payments {
		if e.Msg == "charge succeeded" || e.Msg == MsgPoolExhausted {
			if !gw[e.TraceID] || !ord[e.TraceID] {
				t.Fatalf("payments trace %s not found in gateway and orders", e.TraceID)
			}
		}
	}
}

func TestNoiseBudget(t *testing.T) {
	ds := Generate()
	noise := map[string]bool{
		"request validation failed":                 true,
		"failed to publish order event, will retry": true,
		"fraud check returned malformed response":   true,
	}
	for _, es := range [][]logstore.Entry{ds.Gateway, ds.Orders, ds.Payments} {
		perBlock := map[time.Time]int{}
		for _, e := range es {
			if e.Level == "error" && noise[e.Msg] {
				b := WindowStart.Add(e.Time.Sub(WindowStart) / (15 * time.Minute) * (15 * time.Minute))
				if perBlock[b]++; perBlock[b] > 1 {
					t.Errorf("%s: more than one noise error in block %v", e.Service, b)
				}
			}
		}
	}
}

func TestDeploysOverWindow(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir); err != nil {
		t.Fatal(err)
	}
	s, err := logstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Services(); !slices.Equal(got, []string{"gateway", "orders", "payments"}) {
		t.Errorf("Services() = %v", got)
	}
	got, err := s.Deploys(WindowStart, WindowEnd)
	if err != nil {
		t.Fatal(err)
	}
	want := []logstore.Deploy{
		{Time: OrdersDeployAt, Service: "orders", Version: "v3.8.1", Change: "bump logging library"},
		{Time: PaymentsDeployAt, Service: "payments", Version: "v2.14.0", Change: "reduced DB pool size 50 -> 5"},
	}
	if len(got) != len(want) {
		t.Fatalf("Deploys = %+v", got)
	}
	for i := range want {
		if !got[i].Time.Equal(want[i].Time) || got[i].Service != want[i].Service || got[i].Version != want[i].Version || got[i].Change != want[i].Change {
			t.Errorf("deploy %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Pool errors via the query API: none before 14:04, some right after.
	before, _, _ := s.Search(logstore.SearchParams{Service: "payments", Text: "pool exhausted", From: WindowStart, To: PaymentErrorsFrom})
	after, _, _ := s.Search(logstore.SearchParams{Service: "payments", Level: "error", Text: "pool exhausted", From: PaymentErrorsFrom, To: PaymentErrorsFrom.Add(time.Minute)})
	if len(before) != 0 || len(after) == 0 {
		t.Errorf("pool exhausted: %d before 14:04, %d in 14:04", len(before), len(after))
	}
}
