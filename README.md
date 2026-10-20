# log-investigator

Demo for the talk "What's it like to build MCP servers with the official Go SDK?"
(Chicago Golang meetup, Oct 22, 2026).

A Go agent asks Claude to find the root cause of an incident. Claude investigates by
calling tools on a Go MCP server, which reads synthetic JSONL logs from three services
(`gateway -> orders -> payments -> DB`). The answer key is
[data/GROUND_TRUTH.md](data/GROUND_TRUTH.md).

All data is synthetic and generated deterministically. No real logs are involved.

## Quick start

```sh
cp .env.example .env      # add ANTHROPIC_API_KEY for the agent
make build                # builds every command into bin/
make test                 # go vet + go test
```

Requires Go 1.25.4. The dataset in `data/` is committed, so the demo does not depend on
regenerating it. To regenerate (output is byte-identical every run):

```sh
make data
```

## Running

| Command | What it does |
|---|---|
| `bin/agent` | The real demo: agent loop + MCP client talking to Claude. |
| `bin/server` | The MCP server over stdio (normally launched by a client). |
| `bin/scripted` | No-LLM fallback for when wifi or the API fails. Launches `bin/server`, makes four fixed tool calls, prints a canned conclusion. Use `-server PATH`, and pass server arguments after `--`. |
| `bin/loggen` | Writes the dataset (`-out data`). |
| `make bench` | Measures the Go and Python servers and writes `compare/RESULTS.md`. |
| `compare/loc.sh PATH...` | Non-blank, non-comment line counts, for comparing server sizes. |

Run everything from this directory so `data/` resolves.

## Tool contract

Times are RFC3339 strings in UTC. Ranges are `[from, to)` and at most 6 hours.

| Tool | Arguments |
|---|---|
| `list_services` | none |
| `error_summary` | `from`, `to`, optional `top_n` (default 5) |
| `search_logs` | optional `service`, `level`, `text`; `from`, `to`; optional `limit` (default 20, max 50) |
| `get_deploys` | `from`, `to` |

## The incident (2026-10-21, 13:00-14:20 UTC)

Baseline traffic, plus two decoys: a harmless orders deploy at 13:15 and a burst of
gateway rate-limit warnings at 13:40-13:47. At 14:02 a payments deploy shrinks the DB
pool from 50 to 5. Payments starts failing at 14:04, orders times out from 14:04, and
gateway returns 502/503 from 14:05. The incident is still ongoing at 14:20. See
[data/GROUND_TRUTH.md](data/GROUND_TRUTH.md) for the full timeline and evidence.

## Layout

Parts marked **(talk)** are the subject of the talk and are written by hand. Everything
else is supporting code.

```
log-investigator/
  go.mod
  Makefile
  README.md
  .env.example
  .gitignore
  cmd/loggen/             writes the synthetic data
  cmd/scripted/           no-LLM fallback client
  cmd/bench/              benchmark harness (Go and Python servers)
  cmd/server/             (talk) MCP server
  cmd/agent/              (talk) agent loop + MCP client
  internal/logstore/      reads and queries the JSONL (used by the server's tool handlers)
  internal/loggen/        deterministic data generator (seed 42) + GROUND_TRUTH.md
  internal/render/        terminal output for the agent (respects NO_COLOR)
  compare/python/         Python MCP server with the same four tools (mcp 2.3.0)
  compare/bench.sh        runs cmd/bench, writes compare/RESULTS.md
  compare/loc.sh          line counter
  data/                   generated JSONL + GROUND_TRUTH.md (committed)
```

## Python comparison server

```sh
make python-env           # compare/python/.venv, via uv if installed, else venv + pip
compare/python/.venv/bin/python compare/python/server.py [DATA_DIR]
```

Dependencies are pinned in `compare/python/requirements.txt`, compiled from
`requirements.in`.
