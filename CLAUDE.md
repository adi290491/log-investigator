# Claude Code brief: log-investigator (delegated parts)

Save as `log-investigator/CLAUDE.md`, or paste into a Claude Code session started in the repo root.

## Context

I'm giving a 10-15 minute lightning talk at the Chicago Golang meetup on Oct 22, 2026. Title idea: "What's it like to build MCP servers with the official Go SDK?" The demo is a log investigator: a Go agent loop plus MCP client that asks Claude to find the root cause of an incident by calling tools on a Go MCP server, which reads synthetic JSONL logs.

The talk is about the agent loop, the MCP client, and the MCP server. **I write those myself.** You build everything around them. Everything lives in `log-investigator/`, next to my existing `weather/` demo.

## Ground rules

- Do not touch anything outside `log-investigator/`. In particular, leave `weather/` alone.
- Look at how `weather/` is set up (go.mod placement, Go version, layout) and follow the same conventions. Do not bump the Go version.
- Prefer the standard library. The only allowed Go dependency is `github.com/modelcontextprotocol/go-sdk` (needed for `cmd/scripted` and `cmd/bench`).
- Everything is synthetic data. Never read real logs or real company data.
- Never hardcode or print API keys. `.env` is gitignored and `.env.example` documents `ANTHROPIC_API_KEY=`.
- All timestamps are UTC, RFC3339.
- Before coding, restate your plan and list any ambiguity. Then work in the order at the bottom. After each milestone run `go vet ./... && go test ./...` and summarize what changed.

## Layout

```
log-investigator/
  go.mod                  (follow weather/ conventions)
  Makefile
  README.md
  .env.example
  .gitignore              (.env, bin/)
  cmd/loggen/             YOU: writes the synthetic data
  cmd/scripted/           YOU: no-LLM fallback client
  cmd/bench/              YOU: benchmark harness
  cmd/server/             ME: MCP server (do not create)
  cmd/agent/              ME: agent loop + MCP client (do not create)
  internal/logstore/      YOU: read and query the JSONL
  internal/loggen/        YOU: data generator
  internal/render/        YOU: terminal output helpers
  compare/python/         YOU: Python MCP server with the same tools
  data/                   generated JSONL (commit it, so the demo does not depend on generation)
```

## 1. internal/logstore (blocking, do this first)

Plain Go, no MCP code. I will call it from my tool handlers, so keep these exact signatures.

```go
package logstore

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

type Deploy struct {
    Time    time.Time `json:"ts"`
    Service string    `json:"service"`
    Version string    `json:"version"`
    Change  string    `json:"change"`
}

type SearchParams struct {
    Service string    // exact match, "" = all services
    Level   string    // exact match, "" = all levels
    Text    string    // case-insensitive substring of Msg, "" = no filter
    From    time.Time // inclusive
    To      time.Time // exclusive
    Limit   int       // default 20, hard max 50
}

type MinuteBucket struct {
    Minute  time.Time
    Service string
    Errors  int
    Warns   int
}

type MessageCount struct {
    Service string
    Msg     string
    Count   int
}

type ErrorSummary struct {
    From, To    time.Time
    ByMinute    []MinuteBucket   // non-zero buckets only, sorted by minute then service
    TopMessages []MessageCount   // top N error messages per service
    TotalErrors map[string]int   // per service
}

func Open(dir string) (*Store, error)  // loads data/*.jsonl into memory; one file per service, plus deploys.jsonl
func (s *Store) Services() []string    // sorted, derived from file names
func (s *Store) Search(p SearchParams) (entries []Entry, truncated bool, err error)
func (s *Store) ErrorSummary(from, to time.Time, topN int) (ErrorSummary, error) // topN default 5
func (s *Store) Deploys(from, to time.Time) ([]Deploy, error)
```

Behavior rules:
- Results are ascending by time. `truncated` is true when more entries matched than were returned.
- Truncate any `Msg` longer than 200 chars when returning it.
- Return a clear error if `To` is before `From`, or the range is wider than 6 hours.
- Unit tests for every function, using a tiny fixture rather than the big dataset.

## 2. internal/loggen and cmd/loggen (blocking)

A deterministic generator: fixed seed (`rand.New(rand.NewSource(42))`), fixed date (2026-10-21 UTC), byte-identical output on every run. Writes `data/gateway.jsonl`, `data/orders.jsonl`, `data/payments.jsonl`, `data/deploys.jsonl`, each sorted by time.

Services form a chain: `gateway -> orders -> payments -> (DB)`. Window: 13:00 to 14:20 UTC. The incident is still ongoing at 14:20.

Baseline, per minute: gateway about 20 requests (`info`, "request completed", route, status 200, latency 40-80 ms); orders about 10 ("order created", "payment requested"); payments about 10 ("charge succeeded" with latency; plus one "pool stats" line per minute with `pool_in_use` about 5-15 and `pool_size` 50). Add light noise: at most one unrelated error per service per 15 minutes, for example gateway 400 "request validation failed". Requests flowing through all three services share a `trace_id`.

Incident script (the ground truth, implement exactly):

| Time (UTC) | Event |
|---|---|
| 13:15 | Deploy: orders `v3.8.1`, change "bump logging library" (harmless, a plausible-looking suspect) |
| 13:40-13:47 | Red herring: gateway `warn` "client rate limited", about 30 per minute, no downstream effect, no errors |
| 14:02:00 | Deploy: payments `v2.14.0`, change "reduced DB pool size 50 -> 5" |
| 14:03 | payments `warn` "connection pool near capacity" (`pool_in_use` 5, `pool_size` 5), latency climbing |
| 14:04 onward | payments `error` "connection pool exhausted, waited 1500ms" on about 70% of charges; `pool_size` 5 in stats lines |
| 14:04 onward | orders `error` "payment call timed out after 2000ms" on about 40% of orders |
| 14:05 onward | gateway `error` "upstream timeout (orders)" with status 502/503 on about 35% of requests |

No rollback. No incident-related message may appear before its start time.

Also write `data/GROUND_TRUTH.md`: root cause, timeline, and the evidence an ideal investigation would cite (this is my eval answer key).

Tests (go test): running the generator twice produces identical bytes; payments pool errors start at 14:04 and not earlier; `Deploys` over the window returns exactly the two deploys above; gateway has at least 100 warns during 13:40-13:47 and zero errors in that span; no error from the incident patterns exists before 14:03 in any service.

## 3. internal/render

Small terminal helpers for the agent to call (stdlib only, ANSI colors, respect `NO_COLOR`):

```go
func Step(n, max int)                                          // "step 2/8"
func ToolCall(name string, args any)                           // name plus compact one-line JSON args
func ToolResult(summary string, isErr bool, d time.Duration)   // truncated preview, error in red, elapsed time
func Final(text string)                                        // the answer, visually distinct and readable when projected
```

Readable at a large font on a projector: short lines, no box-drawing clutter, long results truncated with "... (N more chars)".

## 4. cmd/scripted (no-LLM fallback)

A tiny client that launches `bin/server` over stdio with the official go-sdk (`mcp.CommandTransport`), then runs a fixed sequence of tool calls and prints each with `internal/render`: `list_services`, `error_summary` (13:55-14:15), `get_deploys` (13:00-14:20), `search_logs` (service payments, level error). It ends with a canned conclusion matching `GROUND_TRUTH.md`. Purpose: a demo fallback when wifi or the API fails. Write it against the tool contract below. It can only be tested once my `cmd/server` exists, so leave a clear TODO if it isn't there yet.

## 5. compare/ (lowest priority, do last)

A Python MCP server with the same four tools, same names and argument names, for the talk's scorecard.

- `compare/python/server.py`: use the current official Python MCP SDK. Check its current docs for the server API; do not write from memory. Re-implement the log logic in Python against the same `data/` files.
- `compare/python/`: pinned requirements, run via `uv` if installed, else venv.
- `cmd/bench`: a Go harness that spawns a server (Go binary or the Python one), connects with the go-sdk client, and measures: time from process spawn until `tools/list` returns (cold start); total time for a fixed workload (initialize, list tools, 20 `error_summary` calls); peak RSS of the server process (sample `ps -o rss= -p <pid>` every 20 ms; target macOS first). Run 10 times, discard the first, report median and min/max.
- Also report: Go binary size (normal and `-ldflags="-s -w"`), and the installed size of the Python environment (`du` of the venv, noting the interpreter is excluded if system-provided).
- `compare/loc.sh`: counts non-blank, non-comment lines for paths I pass in, so I can compare the MCP surface (tool definitions and registration) and total server size. Don't hardcode paths for my `cmd/server`.
- Write results to `compare/RESULTS.md` including machine, OS, Go and Python versions, and methodology. State numbers factually; do not editorialize.

## 6. Housekeeping

- `Makefile`: `build` (`go build -o bin/ ./cmd/...`), `data`, `test`, `bench`, `clean`.
- `README.md`: what this is, how to generate data, build, run, and the layout above. Mark which parts are mine.
- `.env.example`, `.gitignore`.

## Tool contract (reference; I implement the server and agent)

Tool names and arguments are fixed so your code and mine fit together. Times are RFC3339 strings.

| Tool | Arguments |
|---|---|
| `list_services` | none |
| `error_summary` | `from`, `to`, optional `top_n` |
| `search_logs` | optional `service`, `level`, `text`; `from`, `to`; optional `limit` |
| `get_deploys` | `from`, `to` |

## Do NOT build

- Anything in `cmd/server` or `cmd/agent`: MCP tool registration, input/output structs for tools, the agent loop, the MCP client glue, the system prompt.
- A frontend, HTTP API, auth, or database.
- Extra tools or features beyond this brief. If you think something is missing, ask first.

## Work order and done criteria

1. `internal/logstore` types plus `internal/loggen` and `cmd/loggen`, with tests. **Done when** `make data` produces the files and tests pass. Everything else of mine is blocked on this.
2. `logstore` queries with tests.
3. `internal/render`.
4. `cmd/scripted`.
5. `compare/` and `cmd/bench` (optional, last).
6. Makefile, README, env files.

At the end, give me a summary: what exists, what's untested, and anything I should review closely, especially whether the generated logs tell the incident story as specified.