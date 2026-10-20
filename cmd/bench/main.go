// Command bench measures MCP servers over stdio with the go-sdk client.
//
// For each -server, it runs the workload -runs times, discards the first run
// (warm-up), and reports median, min and max of:
//
//   - cold start: process spawn until tools/list returns
//   - workload:   process spawn, initialize, tools/list, then -calls error_summary calls
//   - calls only: the -calls error_summary calls alone
//   - peak RSS:   max of `ps -o rss= -p <pid>` sampled every 20ms
//
// Usage:
//
//	bench -server "go=bin/server" -server "python=compare/python/.venv/bin/python compare/python/server.py"
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type target struct {
	name string
	argv []string
}

type targets []target

func (t *targets) String() string { return fmt.Sprint(*t) }
func (t *targets) Set(v string) error {
	name, cmdline, ok := strings.Cut(v, "=")
	argv := strings.Fields(cmdline)
	if !ok || name == "" || len(argv) == 0 {
		return fmt.Errorf("want name=command [args...], got %q", v)
	}
	*t = append(*t, target{name, argv})
	return nil
}

type sample struct {
	coldStart, workload, calls time.Duration
	peakRSSKB                  int
}

var errorSummaryArgs = map[string]any{"from": "2026-10-21T13:00:00Z", "to": "2026-10-21T14:20:00Z"}

func main() {
	var ts targets
	flag.Var(&ts, "server", `server to measure as name=command [args...] (repeatable)`)
	runs := flag.Int("runs", 10, "runs per server, the first is discarded")
	calls := flag.Int("calls", 20, "error_summary calls per workload")
	flag.Parse()
	if len(ts) == 0 || *runs < 2 {
		flag.Usage()
		os.Exit(2)
	}

	fmt.Printf("%d runs per server, first discarded; median (min-max). %d error_summary calls over %s to %s.\n\n",
		*runs, *calls, errorSummaryArgs["from"], errorSummaryArgs["to"])
	fmt.Println("| Server | Cold start (ms) | Workload (ms) | Calls only (ms) | Peak RSS (MB) |")
	fmt.Println("|---|---|---|---|---|")
	failed := false
	for _, t := range ts {
		var ss []sample
		for i := range *runs {
			s, err := measure(t, *calls)
			if err != nil {
				fmt.Fprintf(os.Stderr, "bench: %s run %d: %v\n", t.name, i+1, err)
				failed = true
				break
			}
			ss = append(ss, s)
		}
		if len(ss) < *runs {
			fmt.Printf("| %s | failed | | | |\n", t.name)
			continue
		}
		ss = ss[1:]
		fmt.Printf("| %s | %s | %s | %s | %s |\n", t.name,
			stat(ss, func(s sample) float64 { return ms(s.coldStart) }),
			stat(ss, func(s sample) float64 { return ms(s.workload) }),
			stat(ss, func(s sample) float64 { return ms(s.calls) }),
			stat(ss, func(s sample) float64 { return float64(s.peakRSSKB) / 1024 }))
	}
	if failed {
		os.Exit(1)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func stat(ss []sample, f func(sample) float64) string {
	vs := make([]float64, len(ss))
	for i, s := range ss {
		vs[i] = f(s)
	}
	slices.Sort(vs)
	med := vs[len(vs)/2]
	if len(vs)%2 == 0 {
		med = (vs[len(vs)/2-1] + vs[len(vs)/2]) / 2
	}
	return fmt.Sprintf("%.1f (%.1f-%.1f)", med, vs[0], vs[len(vs)-1])
}

func measure(t target, calls int) (sample, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.Command(t.argv[0], t.argv[1:]...)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return sample{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return sample{}, err
	}

	var s sample
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return sample{}, err
	}
	defer cmd.Process.Kill() // no-op once the process has exited

	stopRSS := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.peakRSSKB = samplePeakRSS(cmd.Process.Pid, stopRSS)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "log-investigator-bench", Version: "v1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		return sample{}, fmt.Errorf("initialize: %w", err)
	}
	if _, err := session.ListTools(ctx, nil); err != nil {
		return sample{}, fmt.Errorf("tools/list: %w", err)
	}
	s.coldStart = time.Since(start)

	callsStart := time.Now()
	for range calls {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "error_summary", Arguments: errorSummaryArgs})
		if err != nil {
			return sample{}, fmt.Errorf("error_summary: %w", err)
		}
		if res.IsError {
			return sample{}, fmt.Errorf("error_summary returned a tool error: %v", res.Content)
		}
	}
	s.calls = time.Since(callsStart)
	s.workload = time.Since(start)

	session.Close() // closes stdin; the server should exit
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case <-waitErr:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-waitErr
	}
	close(stopRSS)
	wg.Wait()
	return s, nil
}

// samplePeakRSS polls ps every 20ms until stop is closed or the process is
// gone, returning the maximum RSS in KB.
func samplePeakRSS(pid int, stop <-chan struct{}) int {
	peak := 0
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
		if kb, convErr := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && convErr == nil {
			peak = max(peak, kb)
		}
		select {
		case <-stop:
			return peak
		case <-tick.C:
		}
	}
}
