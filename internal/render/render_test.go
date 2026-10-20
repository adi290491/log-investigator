package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func capture(t *testing.T, noColor bool, f func()) string {
	t.Helper()
	if noColor {
		t.Setenv("NO_COLOR", "1")
	} else {
		t.Setenv("NO_COLOR", "")
	}
	var buf bytes.Buffer
	old := Out
	Out = &buf
	defer func() { Out = old }()
	f()
	return buf.String()
}

func TestStep(t *testing.T) {
	if got := capture(t, true, func() { Step(2, 8) }); got != "\nstep 2/8\n" {
		t.Errorf("Step = %q", got)
	}
}

func TestNoColor(t *testing.T) {
	plain := capture(t, true, func() {
		Step(1, 2)
		ToolCall("x", nil)
		ToolResult("boom", true, time.Second)
		Final("done")
	})
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("NO_COLOR set but output has ANSI codes: %q", plain)
	}
	colored := capture(t, false, func() { ToolResult("boom", true, time.Second) })
	if !strings.Contains(colored, red) {
		t.Errorf("error result should be red: %q", colored)
	}
}

func TestToolCallCompactJSON(t *testing.T) {
	cases := []struct {
		args any
		want string
	}{
		{nil, "→ list_services {}\n"},
		{map[string]any(nil), "→ list_services {}\n"},
		{map[string]any{"to": "2026-10-21T14:15:00Z", "from": "2026-10-21T13:55:00Z"}, // 74 chars: wraps after the comma, aligned under the args.
			"→ list_services {\"from\":\"2026-10-21T13:55:00Z\",\n                \"to\":\"2026-10-21T14:15:00Z\"}\n"},
		{json.RawMessage("{\n  \"service\": \"payments\"\n}"), `→ list_services {"service":"payments"}` + "\n"},
		{struct {
			Change string `json:"change"`
		}{"50 -> 5"}, `→ list_services {"change":"50 -> 5"}` + "\n"},
	}
	for _, c := range cases {
		if got := capture(t, true, func() { ToolCall("list_services", c.args) }); got != c.want {
			t.Errorf("ToolCall(%v)\n got %q\nwant %q", c.args, got, c.want)
		}
	}
}

func TestToolResult(t *testing.T) {
	got := capture(t, true, func() { ToolResult("{\n \"services\": [\"a\"]\n}", false, 85*time.Millisecond) })
	if got != "← 85ms\n  { \"services\": [\"a\"] }\n" {
		t.Errorf("ToolResult = %q", got)
	}
	got = capture(t, true, func() { ToolResult("bad range", true, 1500*time.Millisecond) })
	if got != "← error 1.5s\n  bad range\n" {
		t.Errorf("error ToolResult = %q", got)
	}
}

func TestTruncationAndWidth(t *testing.T) {
	long := strings.Repeat("abcdefghij", 100) // 1000 chars
	got := capture(t, true, func() {
		ToolResult(long, false, time.Second)
		ToolCall("search_logs", map[string]string{"text": long})
	})
	if !strings.Contains(got, "... (780 more chars)") {
		t.Errorf("result not truncated with count: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > Width+len("... (999 more chars)") {
			t.Errorf("line too long (%d): %q", n, line)
		}
	}
}

func TestFinalWrapsWords(t *testing.T) {
	text := "Root cause: " + strings.Repeat("payments pool exhausted ", 10) + "\n\nFix: roll back."
	got := capture(t, true, func() { Final(text) })
	if !strings.HasPrefix(got, "\nANSWER\n\n") {
		t.Errorf("missing header: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len(line) > Width {
			t.Errorf("line over %d: %q", Width, line)
		}
		if strings.HasSuffix(line, "payme") {
			t.Errorf("word split across lines: %q", line)
		}
	}
	if !strings.Contains(got, "\n\n  Fix: roll back.\n") {
		t.Errorf("paragraph break lost: %q", got)
	}
}
