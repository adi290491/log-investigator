// Package logstore loads synthetic JSONL logs into memory and answers the
// queries behind the MCP tools: Services, Search, ErrorSummary and Deploys.
package logstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one log line.
type Entry struct {
	Time      time.Time `json:"ts"`
	Service   string    `json:"service"`
	Level     string    `json:"level"` // "info" | "warn" | "error"
	Msg       string    `json:"msg"`
	TraceID   string    `json:"trace_id,omitempty"`
	Route     string    `json:"route,omitempty"`
	Status    int       `json:"status,omitempty"`
	LatencyMS int       `json:"latency_ms,omitempty"`
	PoolInUse int       `json:"pool_in_use,omitempty"`
	PoolSize  int       `json:"pool_size,omitempty"`
}

// Deploy is one line of deploys.jsonl.
type Deploy struct {
	Time    time.Time `json:"ts"`
	Service string    `json:"service"`
	Version string    `json:"version"`
	Change  string    `json:"change"`
}

// SearchParams filters Search results.
type SearchParams struct {
	Service string    // exact match, "" = all services
	Level   string    // exact match, "" = all levels
	Text    string    // case-insensitive substring of Msg, "" = no filter
	From    time.Time // inclusive
	To      time.Time // exclusive
	Limit   int       // default 20, hard max 50
}

// MinuteBucket counts errors and warnings for one service in one minute.
type MinuteBucket struct {
	Minute  time.Time
	Service string
	Errors  int
	Warns   int
}

// MessageCount is how often one error message occurred in one service.
type MessageCount struct {
	Service string
	Msg     string
	Count   int
}

// ErrorSummary aggregates errors and warnings over a time range.
type ErrorSummary struct {
	From, To    time.Time
	ByMinute    []MinuteBucket // non-zero buckets only, sorted by minute then service
	TopMessages []MessageCount // top N error messages per service
	TotalErrors map[string]int // per service
}

const (
	DeploysFile  = "deploys.jsonl"
	DefaultLimit = 20
	MaxLimit     = 50
	DefaultTopN  = 5
	MaxRange     = 6 * time.Hour
	MaxMsgLen    = 200
)

// Store holds all entries in memory, sorted ascending by time.
type Store struct {
	services []string
	entries  []Entry
	deploys  []Deploy
}

// Open loads dir/*.jsonl. Each file other than deploys.jsonl holds one
// service's logs and is named after it, e.g. payments.jsonl.
func Open(dir string) (*Store, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	s := &Store{}
	for _, p := range paths {
		name := filepath.Base(p)
		if name == DeploysFile {
			if err := readJSONL(p, &s.deploys); err != nil {
				return nil, err
			}
			continue
		}
		s.services = append(s.services, strings.TrimSuffix(name, ".jsonl"))
		if err := readJSONL(p, &s.entries); err != nil {
			return nil, err
		}
	}
	if len(s.services) == 0 {
		return nil, fmt.Errorf("logstore: no service log files (*.jsonl) found in %s", dir)
	}
	sort.Strings(s.services)
	sort.SliceStable(s.entries, func(i, j int) bool { return s.entries[i].Time.Before(s.entries[j].Time) })
	sort.SliceStable(s.deploys, func(i, j int) bool { return s.deploys[i].Time.Before(s.deploys[j].Time) })
	return s, nil
}

func readJSONL[T any](path string, out *[]T) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return fmt.Errorf("logstore: %s line %d: %w", path, line, err)
		}
		*out = append(*out, v)
	}
	return sc.Err()
}

// Services returns the sorted service names, derived from file names.
func (s *Store) Services() []string {
	return append([]string(nil), s.services...)
}
