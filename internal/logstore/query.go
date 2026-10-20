package logstore

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

var levels = []string{"info", "warn", "error"}

// checkRange validates a [from, to) range.
func checkRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() {
		return fmt.Errorf("both from and to are required (RFC3339, UTC)")
	}
	if to.Before(from) {
		return fmt.Errorf("invalid range: to (%s) is before from (%s)", to.UTC().Format(time.RFC3339), from.UTC().Format(time.RFC3339))
	}
	if to.Sub(from) > MaxRange {
		return fmt.Errorf("range too wide: %s exceeds the maximum of %s; narrow from/to", to.Sub(from), MaxRange)
	}
	return nil
}

// window returns the entries with from <= Time < to, using binary search.
func (s *Store) window(from, to time.Time) []Entry {
	lo := sort.Search(len(s.entries), func(i int) bool { return !s.entries[i].Time.Before(from) })
	hi := sort.Search(len(s.entries), func(i int) bool { return !s.entries[i].Time.Before(to) })
	return s.entries[lo:hi]
}

func truncateMsg(m string) string {
	r := []rune(m)
	if len(r) <= MaxMsgLen {
		return m
	}
	return string(r[:MaxMsgLen-3]) + "..."
}

// Search returns matching entries ascending by time, at most p.Limit of them
// (the earliest ones). truncated reports whether more entries matched.
func (s *Store) Search(p SearchParams) (entries []Entry, truncated bool, err error) {
	if err := checkRange(p.From, p.To); err != nil {
		return nil, false, err
	}
	if p.Service != "" && !slices.Contains(s.services, p.Service) {
		return nil, false, fmt.Errorf("unknown service %q; known services: %s", p.Service, strings.Join(s.services, ", "))
	}
	if p.Level != "" && !slices.Contains(levels, p.Level) {
		return nil, false, fmt.Errorf("unknown level %q; use one of: %s", p.Level, strings.Join(levels, ", "))
	}
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	text := strings.ToLower(p.Text)

	entries = []Entry{}
	for _, e := range s.window(p.From, p.To) {
		if (p.Service != "" && e.Service != p.Service) ||
			(p.Level != "" && e.Level != p.Level) ||
			(text != "" && !strings.Contains(strings.ToLower(e.Msg), text)) {
			continue
		}
		if len(entries) == limit {
			return entries, true, nil
		}
		e.Msg = truncateMsg(e.Msg)
		entries = append(entries, e)
	}
	return entries, false, nil
}

// ErrorSummary counts errors and warnings per minute and service, and lists
// the most frequent error messages per service. topN <= 0 means DefaultTopN.
func (s *Store) ErrorSummary(from, to time.Time, topN int) (ErrorSummary, error) {
	if err := checkRange(from, to); err != nil {
		return ErrorSummary{}, err
	}
	if topN <= 0 {
		topN = DefaultTopN
	}
	type key struct {
		minute  time.Time
		service string
	}
	buckets := map[key]*MinuteBucket{}
	msgs := map[[2]string]int{} // {service, msg} -> count
	sum := ErrorSummary{From: from, To: to, ByMinute: []MinuteBucket{}, TopMessages: []MessageCount{}, TotalErrors: map[string]int{}}
	for _, svc := range s.services {
		sum.TotalErrors[svc] = 0
	}

	for _, e := range s.window(from, to) {
		if e.Level != "error" && e.Level != "warn" {
			continue
		}
		k := key{e.Time.UTC().Truncate(time.Minute), e.Service}
		b := buckets[k]
		if b == nil {
			b = &MinuteBucket{Minute: k.minute, Service: k.service}
			buckets[k] = b
		}
		if e.Level == "warn" {
			b.Warns++
			continue
		}
		b.Errors++
		sum.TotalErrors[e.Service]++
		msgs[[2]string{e.Service, truncateMsg(e.Msg)}]++
	}

	for _, b := range buckets {
		sum.ByMinute = append(sum.ByMinute, *b)
	}
	slices.SortFunc(sum.ByMinute, func(a, b MinuteBucket) int {
		return cmp.Or(a.Minute.Compare(b.Minute), cmp.Compare(a.Service, b.Service))
	})

	var all []MessageCount
	for k, n := range msgs {
		all = append(all, MessageCount{Service: k[0], Msg: k[1], Count: n})
	}
	slices.SortFunc(all, func(a, b MessageCount) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(b.Count, a.Count), cmp.Compare(a.Msg, b.Msg))
	})
	perService := map[string]int{}
	for _, m := range all {
		if perService[m.Service] < topN {
			perService[m.Service]++
			sum.TopMessages = append(sum.TopMessages, m)
		}
	}
	return sum, nil
}

// Deploys returns deploys with from <= Time < to, ascending by time.
func (s *Store) Deploys(from, to time.Time) ([]Deploy, error) {
	if err := checkRange(from, to); err != nil {
		return nil, err
	}
	out := []Deploy{}
	for _, d := range s.deploys {
		if !d.Time.Before(from) && d.Time.Before(to) {
			out = append(out, d)
		}
	}
	return out, nil
}
