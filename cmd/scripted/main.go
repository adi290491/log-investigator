// Command scripted is the no-LLM demo fallback: it launches the MCP server
// over stdio, makes a fixed sequence of tool calls, and prints a canned
// conclusion that matches data/GROUND_TRUTH.md.
//
// Usage:
//
//	scripted [-server bin/server] [-- server args...]
//
// TODO(server): this is written against the tool contract in CLAUDE.md
// (list_services, error_summary, get_deploys, search_logs). It can only be
// run end to end once cmd/server exists; build it with `make build`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"log-investigator/internal/render"
)

type call struct {
	tool string
	args map[string]any
}

var script = []call{
	{"list_services", nil},
	{"error_summary", map[string]any{"from": "2026-10-21T13:55:00Z", "to": "2026-10-21T14:15:00Z"}},
	{"get_deploys", map[string]any{"from": "2026-10-21T13:00:00Z", "to": "2026-10-21T14:20:00Z"}},
	{"search_logs", map[string]any{"service": "payments", "level": "error", "from": "2026-10-21T14:00:00Z", "to": "2026-10-21T14:20:00Z"}},
}

const conclusion = `Root cause: the payments deploy v2.14.0 at 14:02:00 UTC ("reduced DB pool size 50 -> 5") shrank the payments DB connection pool to 5. Normal load needs 5-15 connections, so the pool saturated.

Timeline: pool stats show pool_size 5 from 14:02. Payments warns "connection pool near capacity" at 14:03, then errors "connection pool exhausted, waited 1500ms" from 14:04 on about 70% of charges. Orders times out calling payments from 14:04 (about 40% of orders), and gateway returns 502/503 "upstream timeout (orders)" from 14:05 (about 35% of requests). Still ongoing at 14:20.

Ruled out: the orders v3.8.1 deploy at 13:15 (no errors for 50 minutes after it) and the gateway rate-limit warnings at 13:40-13:47 (warnings only, ended before the incident).

Fix: roll back payments to the previous version or restore the pool size to 50.`

func main() {
	server := flag.String("server", "bin/server", "path to the MCP server binary")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [-server path] [-- server args...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := run(*server, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "scripted:", err)
		os.Exit(1)
	}
}

func run(server string, serverArgs []string) error {
	if _, err := os.Stat(server); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s not found; build the server first (make build)", server)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cmd := exec.Command(server, serverArgs...)
	cmd.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "log-investigator-scripted", Version: "v1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", server, err)
	}
	defer session.Close()

	for i, c := range script {
		render.Step(i+1, len(script))
		render.ToolCall(c.tool, c.args)
		start := time.Now()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil {
			render.ToolResult(err.Error(), true, time.Since(start))
			continue
		}
		render.ToolResult(resultText(res), res.IsError, time.Since(start))
	}
	render.Final(conclusion)
	return nil
}

// resultText joins the text content of a tool result, falling back to the
// structured content when there is no text.
func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}
