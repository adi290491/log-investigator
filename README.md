# Log Investigator: an incident-investigation agent built on the official Go MCP SDK

Demo for the lightning talk **"What's it like to build MCP servers with the official Go SDK?"**
(Chicago Golang meetup, October 22, 2026).

You describe a production incident, for example "checkout is failing with 502s at 14:10".
A Go agent hands the problem to Claude. Claude investigates by calling tools on a Go
[MCP](https://modelcontextprotocol.io) server that reads the service logs and deploy history.
It reports a root cause, the evidence, a timeline, and the suspects it ruled out.

The agent loop, the MCP client, and the MCP server use only the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) and the
[Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go). Everything else in the
repo is supporting code: a data generator, a no-LLM fallback client, and a Python server
for comparison.

All logs are synthetic and generated deterministically. No real systems or data are involved.

## How it works

```
 you ──▶ cmd/client ──▶ Claude (Messages API, tool use)
            │   ▲
            │   └── tool results
            ▼
      MCP over stdio
            │
            ▼
       cmd/server ──▶ internal/logstore ──▶ data/*.jsonl
```

1. `cmd/client` launches `bin/server` as a subprocess and connects to it over stdio with `mcp.CommandTransport`.
2. It lists the server's tools and converts their JSON schemas into Anthropic tool definitions (`mcp/`).
3. The agent loop (`agent/`) sends your message to Claude. Whenever Claude asks for a tool, the loop calls it on the MCP server and sends the result back, until Claude gives a final answer.

### Tools

Times are RFC3339 strings in UTC. Ranges are `[from, to)` and at most 6 hours.

| Tool | Arguments | Returns |
|---|---|---|
| `list_services` | none | service names |
| `error_summary` | `from`, `to`, optional `top_n` (default 5) | errors and warnings per minute and service, top error messages, totals |
| `search_logs` | `from`, `to`, optional `service`, `level`, `text`, `limit` (default 20, max 50) | matching log lines, oldest first, plus a `truncated` flag |
| `get_deploys` | `from`, `to` | deploy events |

### The incident in the data

The data covers three services in a chain, `gateway -> orders -> payments -> DB`, on
2026-10-21 from 13:00 to 14:20 UTC:

- **13:15**: orders `v3.8.1` deploy ("bump logging library"). Harmless, but a plausible suspect.
- **13:40-13:47**: a burst of gateway "client rate limited" warnings. A red herring with no downstream effect.
- **14:02**: payments `v2.14.0` deploy, "reduced DB pool size 50 -> 5". **This is the root cause.**
- **14:04 onward**: payments pool exhaustion, then orders timeouts, then gateway 502/503s. The incident is still ongoing at 14:20.

The answer key, including the evidence an ideal investigation would cite, is in
[data/GROUND_TRUTH.md](data/GROUND_TRUTH.md).

## Quick start

Requires Go 1.25.4 and an Anthropic API key.

```sh
cp .env.example .env      # then set ANTHROPIC_API_KEY
make build                # builds every command into bin/
make test                 # go vet + go test
bin/client                # start the agent; run from the repo root
```

Then describe an incident. Include a date and time, because the agent asks for one if you leave it out:

```
PagerDuty alert at 14:10 UTC on 2026-10-21: customers report checkout is failing with 502s.
Find the root cause and cite evidence.
```

Other prompts worth trying:

- "We deployed orders v3.8.1 on 2026-10-21 and now checkout is broken. Can you confirm that deploy caused it?" This tests whether the agent pushes back on a wrong suspect.
- "Around 13:40 UTC on 2026-10-21 we saw a lot of rate-limit warnings at the gateway. Is that related to the checkout outage later that afternoon?"

The dataset in `data/` is committed. `make data` regenerates it, and the output is byte-identical on every run.

## Snapshots

### Agent investigation (`bin/client`)

Abridged from a real run (Claude Sonnet 4.6):

```
User: PagerDuty alert at 14:10 UTC on 2026-10-21: customers report checkout is failing
      with 502s. Find the root cause and cite evidence.
Claude: Starting broad: listing services and summarizing errors around the alert time.
tool: list_services({})
tool: error_summary({"from":"2026-10-21T13:40:00Z","to":"2026-10-21T14:40:00Z","top_n":5})
Claude: Payments errors started first, then orders (14:04), then gateway (14:05). Now I'll
        check deploys around the onset and pull actual log lines from payments.
tool: get_deploys({"from":"2026-10-21T12:00:00Z","to":"2026-10-21T14:20:00Z"})
tool: search_logs({"from":"2026-10-21T13:50:00Z","to":"2026-10-21T14:10:00Z","service":"payments","level":"error","limit":20})

Root cause:
The payments deploy at 14:02 UTC shrunk the DB connection pool from 50 to 5. That starved
payments of connections, causing cascading timeouts into orders and then 502s at the gateway.

Evidence:
Deploy: payments v2.14.0 at 14:02:00Z — "reduced DB pool size 50 -> 5"
Payments logs from 14:04 onward show pool_size=5, pool_in_use=5 on every error:
  "connection pool exhausted, waited 1500ms" — first hit 14:04:12Z, then non-stop.
  orders:   "payment call timed out after 2000ms" from 14:04 (64 total).
  gateway:  "upstream timeout (orders)" from 14:05 (106 total).

Ruled out:
Orders v3.8.1 (13:15) — a logging library bump; orders errors don't start until 14:04,
aligned with the payments failure, not the orders deploy.

Next step:
Roll back payments to the previous version, or set the pool back to 50 and redeploy.
```

### No-LLM fallback (`bin/scripted`)

This runs a fixed sequence of tool calls against the same server, with no network needed.
It's the backup plan if the wifi or the API fails during the demo.

```
step 3/4
→ get_deploys {"from":"2026-10-21T13:00:00Z",
              "to":"2026-10-21T14:20:00Z"}
← 0ms
  {"deploys":[{"change":"bump logging library","service":"orders",
  "ts":"2026-10-21T13:15:00Z","version":"v3.8.1"},{"change":"reduced DB
  pool size 50 -> 5","service":"payments",
  "ts":"2026-10-21T14:02:00Z","version":"v2... (9 more chars)

step 4/4
→ search_logs {"from":"2026-10-21T14:00:00Z","level":"error",
              "service":"payments","to":"2026-10-21T14:20:00Z"}
← 0ms
  {"entries":[{"latency_ms":1500,"level":"error","msg":"connection pool
  exhausted, waited 1500ms","pool_in_use":5,"pool_size":5, ... (3769 more chars)

ANSWER

  Root cause: the payments deploy v2.14.0 at 14:02:00 UTC ("reduced DB
  pool size 50 -> 5") shrank the payments DB connection pool to 5.
  ...
```

## Go and Python side by side

`compare/python/server.py` implements the same four tools, with the same names and
arguments, using the official Python MCP SDK (`mcp` 2.3.0). It reads the same `data/` files.
I compared the two to get a feel for the developer experience and the runtime footprint.
This isn't a verdict on either language.

Both servers return the same data for the same requests. The agent works against either one
without code changes:

```sh
make python-env
printf '#!/bin/sh\nexec compare/python/.venv/bin/python compare/python/server.py "$@"\n' > /tmp/pyserver
chmod +x /tmp/pyserver
bin/client -server /tmp/pyserver   # -server takes a single executable, hence the wrapper
```

### Numbers from one machine

These are from an Apple M3 running macOS 26.3, with Go 1.25.4 and Python 3.13.3. Each figure is the median of 9 runs after one warm-up run.
Full methodology and ranges are in [compare/RESULTS.md](compare/RESULTS.md), and commentary is in
[compare/OBSERVATIONS.md](compare/OBSERVATIONS.md).

| | Go | Python |
|---|---|---|
| Spawn to first `tools/list` response | ~12 ms | ~290 ms |
| 20 `error_summary` calls (after startup) | ~16 ms | ~45 ms |
| Peak memory (RSS) | ~18 MB | ~73 MB |
| On disk | 10.3 MB binary (7.1 MB stripped) | 32 MB venv, plus a Python interpreter |
| Server code (non-blank, non-comment lines) | 128 (server) + 201 (log queries) | 143 (one file) |
| End-to-end agent investigation, 2 runs each | 26-27 s | 30-32 s |

How to read these:

- The workload is small: about 4,500 log lines and sequential calls on one connection. The
  numbers say little about behavior under concurrency or with large datasets.
- In the end-to-end runs, model latency and the number of tool calls Claude chooses dominate
  the wall time. Server time is a small fraction of each investigation, so the end-to-end gap
  reflects run-to-run variance more than the servers.
- Both servers were written for this demo and neither is tuned.

### Where Go took more work

Go wasn't simpler at everything. These were the places where Python was quicker to write:

- **Tool definitions take more code.** The Python SDK builds each tool's schema from the
  function's type hints and docstring, so one decorated function is the whole tool. In Go,
  each tool needs an input struct with `json` and `jsonschema` tags, an output struct, and an
  `mcp.AddTool` call with a typed handler. The result is explicit and checked by the compiler, but it's
  more code for the same four tools.
- **Duplicate types.** The query package has its own result types, so the server defines
  parallel output structs with JSON tags and a conversion function between them. Python
  returns a `dict` and moves on.
- **`nil` versus empty slices.** A `nil` slice encodes as `null`, not `[]`. The SDK's
  generated output schema allows both, so nothing breaks, but the model sees
  `"deploys": null` for "no results" unless every empty path deliberately returns a
  non-nil slice. Python lists don't have this distinction.
- **Bridging two SDKs.** Converting MCP tool schemas to Anthropic tool parameters means
  marshaling to JSON and back into typed structs. Reading results means type switches over
  content blocks (`*mcp.TextContent`, `anthropic.ToolUseBlock`). In Python, the same glue
  mostly passes dictionaries through.
- **Time formatting.** `time.Time` encodes as RFC3339 with trailing zeros trimmed, so
  `14:05:28.63Z` appears next to `14:05:28.201Z`. In Python, a fixed width is one argument:
  `isoformat(timespec="milliseconds")`. Getting the same in Go means a custom layout and a
  custom JSON type or marshaller. Neither breaks anything.
- **A newer ecosystem.** The Python MCP SDK has been around longer and has more examples and
  community answers to lean on. With the Go SDK, the package docs and source are often the
  main reference.

On the other hand, Python's `from` is a reserved word, so the Python server needs a `from_`
parameter with an alias to match the tool contract. Go has no such clash.

## Repository layout

Parts marked **(talk)** are the subject of the talk and were written by hand. Everything else
is supporting code.

```
log-investigator/
  cmd/client/             (talk) entry point: connects to the MCP server, starts the agent
  agent/                  (talk) agent loop and system prompt
  mcp/                    (talk) MCP client glue: connect, convert tool schemas
  cmd/server/             (talk) MCP server: the four tools
  internal/logstore/      reads and queries the JSONL (used by the server's tool handlers)
  internal/loggen/        deterministic data generator (seed 42) + GROUND_TRUTH.md
  cmd/loggen/             writes data/
  internal/render/        projector-friendly terminal output (used by cmd/scripted)
  cmd/scripted/           no-LLM fallback client
  cmd/bench/              benchmark harness for the Go and Python servers
  compare/python/         Python MCP server with the same tools
  compare/bench.sh        runs cmd/bench, writes compare/RESULTS.md
  compare/loc.sh          line counter
  data/                   generated JSONL + GROUND_TRUTH.md (committed)
```

## Commands

| Command | What it does |
|---|---|
| `make build` | Builds every command into `bin/` |
| `make test` | `go vet ./...` and `go test ./...` |
| `make data` | Regenerates `data/` (deterministic) |
| `make python-env` | Creates `compare/python/.venv`, with `uv` if it's installed, otherwise venv + pip |
| `make bench` | Benchmarks both servers and writes `compare/RESULTS.md` |
| `make clean` | Removes `bin/` and the Python venv |
| `bin/client [-server PATH]` | Starts the agent (default server: `./bin/server`) |
| `bin/scripted [-server PATH]` | Starts the no-LLM fallback |
| `compare/loc.sh PATH...` | Counts non-blank, non-comment lines |

Run commands from the repo root so the relative paths (`bin/server`, `data/`, `.env`) resolve.

## License

MIT. See [LICENSE](LICENSE).
