// Package render prints agent progress for a projector: short lines, a few
// ANSI colors (disabled when NO_COLOR is set), and truncated tool output.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Out is where everything is printed. Tests replace it.
var Out io.Writer = os.Stdout

const (
	// Width is the maximum line width, sized for a large projector font.
	Width = 72
	// PreviewChars is how much of an argument list or tool result is shown.
	PreviewChars = 220
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

// color wraps s in the given ANSI codes unless NO_COLOR is set (https://no-color.org).
func color(s string, codes ...string) string {
	if os.Getenv("NO_COLOR") != "" || len(codes) == 0 {
		return s
	}
	return strings.Join(codes, "") + s + reset
}

// Step prints a step header, e.g. "step 2/8".
func Step(n, max int) {
	fmt.Fprintf(Out, "\n%s\n", color(fmt.Sprintf("step %d/%d", n, max), bold, cyan))
}

// ToolCall prints the tool name and its arguments as compact one-line JSON.
func ToolCall(name string, args any) {
	fmt.Fprintf(Out, "%s %s\n", color("→ "+name, bold, yellow), indent(truncate(compactJSON(args)), len(name)+3))
}

// ToolResult prints a truncated preview of a tool result with its elapsed
// time. Errors are shown in red.
func ToolResult(summary string, isErr bool, d time.Duration) {
	label := color("← "+elapsed(d), green)
	if isErr {
		label = color("← error "+elapsed(d), bold, red)
	}
	body := wrap(truncate(oneLine(summary)), "  ")
	if isErr {
		body = color(body, red)
	}
	fmt.Fprintf(Out, "%s\n%s\n", label, body)
}

// Final prints the investigation's answer, set apart from the tool trace.
func Final(text string) {
	fmt.Fprintf(Out, "\n%s\n\n", color("ANSWER", bold, green))
	for _, para := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(para) == "" {
			fmt.Fprintln(Out)
			continue
		}
		fmt.Fprintln(Out, color(wrapWords(para, "  "), bold))
	}
	fmt.Fprintln(Out)
}

func compactJSON(args any) string {
	var raw []byte
	switch v := args.(type) {
	case nil:
		return "{}"
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return fmt.Sprintf("%v", v)
		}
		if out := strings.TrimSpace(buf.String()); out != "null" { // typed nil map or pointer
			return out
		}
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return oneLine(string(raw)) // not JSON; show as text
	}
	return buf.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncate shortens s to PreviewChars runes, noting how much was cut.
func truncate(s string) string {
	r := []rune(s)
	if len(r) <= PreviewChars {
		return s
	}
	return string(r[:PreviewChars]) + fmt.Sprintf("... (%d more chars)", len(r)-PreviewChars)
}

func elapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// wrap wraps s at Width, prefixing every line.
func wrap(s, prefix string) string {
	return prefix + breakLines(s, Width-len(prefix), Width-len(prefix), "\n"+prefix)
}

// breakLines splits s into lines of at most first (then rest) runes,
// preferring to break after a comma or space so JSON values and timestamps
// stay intact. A token longer than a line is hard-broken.
func breakLines(s string, first, rest int, sep string) string {
	r := []rune(s)
	var out []string
	n := max(first, 20)
	for len(r) > n {
		cut := n
		for i := n; i > n/2; i-- {
			if r[i-1] == ',' || r[i-1] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		r = r[cut:]
		n = max(rest, 20)
	}
	return strings.Join(append(out, string(r)), sep)
}

// wrapWords wraps prose at word boundaries.
func wrapWords(s, prefix string) string {
	var lines []string
	line := prefix
	for _, w := range strings.Fields(s) {
		if len(line) > len(prefix) && len([]rune(line))+1+len([]rune(w)) > Width {
			lines = append(lines, line)
			line = prefix
		}
		if len(line) > len(prefix) {
			line += " "
		}
		line += w
	}
	return strings.Join(append(lines, line), "\n")
}

// indent wraps s so it fits after a col-wide label, aligning continuation
// lines under the start of s.
func indent(s string, col int) string {
	return breakLines(s, Width-col, Width-col, "\n"+strings.Repeat(" ", col))
}
