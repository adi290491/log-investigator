package main

import (
	"context"
	"flag"
	"log"
	"log-investigator/internal/logstore"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ServicesOutput struct {
	Services []string `json:"services"`
}

type ErrorSummaryInput struct {
	TimeRange
	TopN int `json:"top_n,omitempty" jsonschema:"error messages per service, default 5"`
}

type TimeRange struct {
	From time.Time `json:"from" jsonschema:"range start, RFC3339 UTC, inclusive"`
	To   time.Time `json:"to" jsonschema:"range end, RFC3339 UTC, exclusive; max 6h after from"`
}

type ErrorSummaryOutput struct {
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	ByMinute    []MinuteBucket `json:"by_minute"`
	TopMessages []MessageCount `json:"top_messages"`
	TotalErrors map[string]int `json:"total_errors"`
}

type MinuteBucket struct {
	Minute  time.Time `json:"minute"`
	Service string    `json:"service"`
	Errors  int       `json:"errors"`
	Warns   int       `json:"warns"`
}

type MessageCount struct {
	Service string `json:"service"`
	Msg     string `json:"msg"`
	Count   int    `json:"count"`
}

func toErrorSummaryOutput(s logstore.ErrorSummary) ErrorSummaryOutput {
	out := ErrorSummaryOutput{
		From:        s.From,
		To:          s.To,
		ByMinute:    make([]MinuteBucket, len(s.ByMinute)),
		TopMessages: make([]MessageCount, len(s.TopMessages)),
		TotalErrors: s.TotalErrors,
	}
	for i, b := range s.ByMinute {
		out.ByMinute[i] = MinuteBucket{Minute: b.Minute, Service: b.Service, Errors: b.Errors, Warns: b.Warns}
	}
	for i, m := range s.TopMessages {
		out.TopMessages[i] = MessageCount{Service: m.Service, Msg: m.Msg, Count: m.Count}
	}
	return out
}

type SearchLogsInput struct {
	TimeRange
	Service string `json:"service,omitempty" jsonschema:"exact service name from list_services; omit for all services"`
	Level   string `json:"level,omitempty" jsonschema:"info, warn or error; omit for all levels"`
	Text    string `json:"text,omitempty" jsonschema:"case-insensitive substring of the log message"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max entries to return, default 20, max 50"`
}

type SearchLogsOutput struct {
	Entries   []logstore.Entry `json:"entries"`
	Truncated bool             `json:"truncated"`
}

type DeploysOutput struct {
	Deploys []logstore.Deploy `json:"deploys"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "log-investigator-server",
		Version: "1.0.0",
	}, nil)

	dataDir := flag.String("data", "data", "directory containing the JSONL logs")
	flag.Parse()

	store, err := logstore.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_services",
		Description: "Returns the names of the services that have logs, so the agent knows what it can investigate.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ServicesOutput, error) {
		return nil, ServicesOutput{
			Services: store.Services(),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "error_summary",
		Description: "Gives error and warning counts per service per minute for a time range, plus the most common error messages, so the agent can spot when and where things broke.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ErrorSummaryInput) (*mcp.CallToolResult, ErrorSummaryOutput, error) {
		summary, err := store.ErrorSummary(in.From, in.To, in.TopN)
		if err != nil {
			return nil, ErrorSummaryOutput{}, err
		}
		return nil, toErrorSummaryOutput(summary), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_logs",
		Description: "Returns matching log lines filtered by service, level, text, and time range, so the agent can read the details behind a spike.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in SearchLogsInput) (*mcp.CallToolResult, SearchLogsOutput, error) {
		entries, truncated, err := store.Search(logstore.SearchParams{
			Service: in.Service,
			Level:   in.Level,
			Text:    in.Text,
			From:    in.From,
			To:      in.To,
			Limit:   in.Limit,
		})
		if err != nil {
			return nil, SearchLogsOutput{}, err
		}
		return nil, SearchLogsOutput{Entries: entries, Truncated: truncated}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_deploys",
		Description: "Lists deployment events in a time range, so the agent can check whether a change lines up with the start of the problem.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in TimeRange) (*mcp.CallToolResult, DeploysOutput, error) {
		deploys, err := store.Deploys(in.From, in.To)
		if err != nil {
			return nil, DeploysOutput{}, err
		}
		return nil, DeploysOutput{Deploys: deploys}, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
