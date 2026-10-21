package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	m "log-investigator/mcp"
)

const (
	MODEL     = anthropic.ModelClaudeSonnet4_6
	MaxTokens = 16000

	systemPrompt = `You are an on-call site reliability engineer investigating a production incident.
You have read-only tools that query the service logs and deploy history. Base every claim on what those tools return.

How to investigate:
- If the user has not given a date and time window, ask for one instead of guessing.
- Start broad: list the services, then summarize errors over the window to find when and where errors began.
- Check deploys around the time errors began.
- Read the actual log lines for the earliest failing service before drawing conclusions.
- Times are UTC in RFC3339. A single query can span at most 6 hours.

How to reason:
- The root cause is usually the earliest failure. Services that fail later are often victims of it.
- Only claim a causal link when the logs show one, for example matching timing, shared trace IDs, or a change that explains the error. Timing alone is a hint, not proof.
- If the user suggests a cause, test it against the evidence and say plainly if it is wrong.
- Use numbers taken from the tool results. Do not estimate or round to "all".
- Mention unrelated issues briefly and keep them separate from the root cause.

How to answer:
Keep it short and in plain text with short lines; it will be read on a projector. No tables or emoji. Use these sections:
Root cause: one or two sentences.
Evidence: the specific log lines, counts, and deploys you relied on, with timestamps.
Timeline: key events in order.
Ruled out: other suspects and why.
Next step: the fix or the next thing to check.`
)

const (
	blue   = "\u001b[94m"
	yellow = "\u001b[93m"
	green  = "\u001b[92m"
	reset  = "\u001b[0m"
)

type Agent struct {
	client         *anthropic.Client
	session        *mcp.ClientSession
	tools          []anthropic.ToolUnionParam
	getUserMessage func() (string, bool)
}

func NewAgent(client *anthropic.Client, session *mcp.ClientSession, tools []anthropic.ToolUnionParam, getUserMessage func() (string, bool)) *Agent {
	return &Agent{
		client:         client,
		session:        session,
		tools:          tools,
		getUserMessage: getUserMessage,
	}
}

func (a *Agent) Run(ctx context.Context) error {
	conversation := make([]anthropic.MessageParam, 0)
	fmt.Println("Log Investigator with Claude (use 'ctrl-c' to quit)")

	for {
		fmt.Print(blue + "User" + reset + ": ")
		userInput, ok := a.getUserMessage()
		if !ok {
			break
		}

		if strings.TrimSpace(userInput) == "" {
			continue
		}
		userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
		conversation = append(conversation, userMessage)

		var err error
		conversation, err = a.runTurn(ctx, conversation)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) runTurn(ctx context.Context, conversation []anthropic.MessageParam) ([]anthropic.MessageParam, error) {
	for {
		message, err := a.runInference(ctx, conversation)
		if err != nil {
			return conversation, err
		}

		conversation = append(conversation, message.ToParam())

		var toolResult []anthropic.ContentBlockParamUnion

		for _, content := range message.Content {
			switch block := content.AsAny().(type) {
			case anthropic.TextBlock:
				fmt.Printf(yellow+"Claude"+reset+": %s\n", block.Text)

			case anthropic.ToolUseBlock:
				toolResult = append(toolResult, a.callMCPTool(ctx, block))
			}
		}

		if message.StopReason != anthropic.StopReasonToolUse {
			return conversation, nil
		}
	}
}

func (a *Agent) runInference(ctx context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	message, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeSonnet4_6,
		MaxTokens: int64(1024),
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Tools:     a.tools,
		Messages:  conversation,
	})
	return message, err
}

func (a *Agent) callMCPTool(ctx context.Context, block anthropic.ToolUseBlock) anthropic.ContentBlockParamUnion {
	fmt.Printf(green+"tool"+reset+": %s(%s)\n", block.Name, block.JSON.Input.Raw())

	res, err := a.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      block.Name,
		Arguments: block.Input,
	})

	if err != nil {
		return anthropic.NewToolResultBlock(block.ID, err.Error(), true)
	}

	return anthropic.NewToolResultBlock(block.ID, m.TextContent(res), res.IsError)
}
