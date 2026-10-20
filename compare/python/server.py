"""Python MCP server with the same four tools as the Go server, for the talk's
scorecard. Reads the same data/*.jsonl and mirrors internal/logstore.

Usage: python server.py [DATA_DIR]   (default: ../../data relative to this file)
"""

import functools
import json
import sys
from collections import Counter
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Annotated, Any

from mcp.server import MCPServer
from pydantic import Field
from mcp.server.mcpserver.exceptions import ToolError

DEFAULT_LIMIT, MAX_LIMIT, DEFAULT_TOP_N = 20, 50, 5
MAX_RANGE = timedelta(hours=6)
MAX_MSG_LEN = 200

# "from" is a Python keyword; the alias keeps the wire name identical to the Go server.
From = Annotated[str, Field(alias="from", description="Range start, RFC3339 UTC, inclusive")]
To = Annotated[str, Field(description="Range end, RFC3339 UTC, exclusive")]
LEVELS = ("info", "warn", "error")


def parse_ts(s: str) -> datetime:
    return datetime.fromisoformat(s).astimezone(timezone.utc)


def fmt_ts(t: datetime) -> str:
    return t.isoformat(timespec="milliseconds").replace("+00:00", "Z")


def load(data_dir: Path) -> tuple[list[str], list[dict], list[dict]]:
    services, entries, deploys = [], [], []
    for path in sorted(data_dir.glob("*.jsonl")):
        rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
        for r in rows:
            r["_t"] = parse_ts(r["ts"])
        if path.name == "deploys.jsonl":
            deploys.extend(rows)
        else:
            services.append(path.stem)
            entries.extend(rows)
    if not services:
        sys.exit(f"no service log files (*.jsonl) found in {data_dir}")
    entries.sort(key=lambda r: r["_t"])
    deploys.sort(key=lambda r: r["_t"])
    return services, entries, deploys


DATA_DIR = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[2] / "data"
SERVICES, ENTRIES, DEPLOYS = load(DATA_DIR)


def check_range(from_: From, to: To) -> tuple[datetime, datetime]:
    try:
        f, t = parse_ts(from_), parse_ts(to)
    except ValueError as e:
        raise ToolError(f"from and to must be RFC3339 timestamps: {e}")
    if t < f:
        raise ToolError(f"invalid range: to ({to}) is before from ({from_})")
    if t - f > MAX_RANGE:
        raise ToolError(f"range too wide: {t - f} exceeds the maximum of 6h; narrow from/to")
    return f, t


def window(rows: list[dict], f: datetime, t: datetime):
    return (r for r in rows if f <= r["_t"] < t)


def truncate_msg(m: str) -> str:
    return m if len(m) <= MAX_MSG_LEN else m[: MAX_MSG_LEN - 3] + "..."


def public(r: dict) -> dict:
    out = {k: v for k, v in r.items() if k != "_t"}
    if "msg" in out:
        out["msg"] = truncate_msg(out["msg"])
    return out


mcp = MCPServer("log-investigator-python")


def tool(fn):
    """Register fn as a tool. The SDK builds the schema from fn's signature (so the
    wire name is the alias "from") but then calls the function with from=..., which
    cannot be a Python parameter name; map it back to from_."""

    @functools.wraps(fn)
    def call(**kwargs):
        if "from" in kwargs:
            kwargs["from_"] = kwargs.pop("from")
        return fn(**kwargs)

    return mcp.tool()(call)


@tool
def list_services() -> dict[str, Any]:
    """List the services that have logs."""
    return {"services": SERVICES}


@tool
def error_summary(from_: From, to: To, top_n: int = DEFAULT_TOP_N) -> dict[str, Any]:
    """Count errors and warnings per minute and service between from and to (RFC3339, UTC,
    max 6h), and list the most frequent error messages per service."""
    f, t = check_range(from_, to)
    top_n = top_n if top_n > 0 else DEFAULT_TOP_N
    buckets: dict[tuple[datetime, str], dict] = {}
    msgs: Counter = Counter()
    totals = {s: 0 for s in SERVICES}
    for r in window(ENTRIES, f, t):
        if r["level"] not in ("error", "warn"):
            continue
        minute = r["_t"].replace(second=0, microsecond=0)
        b = buckets.setdefault((minute, r["service"]), {"minute": fmt_ts(minute), "service": r["service"], "errors": 0, "warns": 0})
        if r["level"] == "warn":
            b["warns"] += 1
            continue
        b["errors"] += 1
        totals[r["service"]] += 1
        msgs[(r["service"], truncate_msg(r["msg"]))] += 1
    top, per = [], Counter()
    for (svc, msg), n in sorted(msgs.items(), key=lambda kv: (kv[0][0], -kv[1], kv[0][1])):
        if per[svc] < top_n:
            per[svc] += 1
            top.append({"service": svc, "msg": msg, "count": n})
    return {
        "from": fmt_ts(f),
        "to": fmt_ts(t),
        "by_minute": [buckets[k] for k in sorted(buckets)],
        "top_messages": top,
        "total_errors": totals,
    }


@tool
def search_logs(
    from_: From,
    to: To,
    service: str = "",
    level: str = "",
    text: str = "",
    limit: int = DEFAULT_LIMIT,
) -> dict[str, Any]:
    """Search log entries between from and to (RFC3339, UTC, max 6h), oldest first.
    Optional filters: service (exact), level (info|warn|error), text (case-insensitive
    substring of msg). limit defaults to 20, max 50."""
    f, t = check_range(from_, to)
    if service and service not in SERVICES:
        raise ToolError(f"unknown service {service!r}; known services: {', '.join(SERVICES)}")
    if level and level not in LEVELS:
        raise ToolError(f"unknown level {level!r}; use one of: {', '.join(LEVELS)}")
    limit = min(limit if limit > 0 else DEFAULT_LIMIT, MAX_LIMIT)
    needle = text.lower()
    out, truncated = [], False
    for r in window(ENTRIES, f, t):
        if (service and r["service"] != service) or (level and r["level"] != level) or (needle and needle not in r["msg"].lower()):
            continue
        if len(out) == limit:
            truncated = True
            break
        out.append(public(r))
    return {"entries": out, "truncated": truncated}


@tool
def get_deploys(from_: From, to: To) -> dict[str, Any]:
    """List deploys between from and to (RFC3339, UTC, max 6h)."""
    f, t = check_range(from_, to)
    return {"deploys": [public(d) for d in window(DEPLOYS, f, t)]}


if __name__ == "__main__":
    mcp.run()
