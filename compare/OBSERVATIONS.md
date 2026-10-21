# Go vs Python MCP server: observations

Run on 2026-10-20 on an Apple M3 (24 GB RAM, macOS 26.3) with Go 1.25.4 and go-sdk v1.8.0, and Python 3.13.3 with mcp 2.3.0.
The raw numbers and methodology are in [RESULTS.md](RESULTS.md), produced by `make bench`.
This file adds interpretation, a check that both servers return the same data, and an end-to-end run with the real agent.

## Protocol-level benchmark (`make bench`, 9 measured runs per server)

| Metric | Go | Python | Python / Go |
|---|---|---|---|
| Cold start: spawn to `tools/list` (median ms) | 11.7 | 291.2 | about 25x |
| Workload: spawn, init, list, 20 `error_summary` (median ms) | 27.6 | 334.3 | about 12x |
| 20 `error_summary` calls only (median ms) | 16.0 | 44.9 | about 2.8x |
| Per call (calls only / 20, ms) | 0.8 | 2.2 | about 2.8x |
| Peak RSS (median MB) | 18.2 | 72.7 | about 4x |
| On disk (MB) | 10.3 (7.1 stripped) | 32.1 venv, plus the system interpreter | |

Observations:

- Startup accounts for most of the gap. About 280 ms of Python's 334 ms workload is spent before the first response (interpreter start, imports, SDK setup). Per call, Python is about 2.8x slower, against about 25x at startup.
- Go varies less from run to run. Its ranges are tight; for example, calls only runs 15.6-17.6 ms. Python had one calls-only run at 133.9 ms against a 44.9 ms median, likely a GC pause or a scheduling hiccup. With 9 runs, that is not enough to tell.
- Memory is flat for both. Each loads about 4,500 log lines at startup and stays near that footprint. The 4x difference is mostly the baseline cost of the Python runtime and SDK, not the data.
- Distribution: Go ships as a single 7-10 MB static binary. Python needs a 32 MB venv plus a matching interpreter on the host.
- These results are from one machine, a small dataset, and sequential calls on one stdio connection. They don't cover concurrency, large payloads, or long-running memory growth.

## Output parity

I sent the same JSON-RPC requests to both servers (all four tools, plus an invalid time range) and compared the parsed results.

- The data is identical: same counts, buckets, top messages, deploys, entries, and `truncated` flag.
- Formatting differs in two ways, neither of which matters to the agent:
  - Timestamps: Python always writes milliseconds (`13:15:00.000Z`). Go trims trailing zeros (`13:15:00Z`, `14:05:28.63Z`).
  - Tool errors: Python prefixes the message with `Error executing tool <name>: `. Go returns the bare message.

## End to end: real agent (`bin/client`) against each server

I used the same prompt for every run: "PagerDuty alert at 14:10 UTC on 2026-10-21: customers report checkout is failing with 502s. Find the root cause and cite evidence."
The model was Claude Sonnet 4.6, with 2 runs per server.

| Run | Server | Wall time (s) | Tool calls | Tool errors | Root cause correct |
|---|---|---|---|---|---|
| 1 | Go | 27.4 | 5 | 0 | yes |
| 1 | Python | 30.3 | 7 | 0 | yes |
| 2 | Go | 25.8 | 5 | 0 | yes |
| 2 | Python | 32.3 | 5 | 0 | yes |

Observations:

- All four runs named the payments `v2.14.0` deploy at 14:02 UTC (DB pool 50 -> 5) as the root cause.
- The agent uses both servers the same way, with no client changes and no tool errors. The only difference is the `-server` path.
- The Go runs were 3-6 s faster, but server choice doesn't explain that. The servers' total compute is under 0.35 s per run, and Python's startup cost is paid once. Model latency dominates, and how many tool calls the model chooses varies from run to run (Python run 1 made 7 calls; the other three runs made 5). With 2 runs per server, this is noise.
- In this demo, an investigation takes about 25-30 s and less than 2% of that is server time. Server language choice matters for startup, memory, and distribution, not for perceived agent speed.

## Code size (`compare/loc.sh`, non-blank, non-comment lines)

| Part | Go | Python |
|---|---|---|
| MCP server: tool definitions, I/O types, registration | 128 (`cmd/server/main.go`) | 143 (`server.py`, everything) |
| Log query logic | 201 (`internal/logstore`) | included above |
| Total | 329 | 143 |

- The Python server is one file with the query logic inline, and tool schemas come from type hints and docstrings.
- The Go server declares explicit input and output structs, which account for much of its 128 lines. In exchange, the output schema is checked at compile time.
- The Go query logic is a separate package with its own unit tests, which accounts for some of its extra size.

## Caveats

- The Python server is a re-implementation written for this comparison, not tuned. The Go logstore isn't tuned either.
- RSS is sampled every 20 ms with `ps`, so short spikes can be missed.
- The end-to-end runs call a live API, and their timings include network and model variance.
