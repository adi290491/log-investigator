package main

import (
	"bufio"
	"context"
	"fmt"

	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/joho/godotenv"
)

type Agent struct {
	client         *anthropic.Client
	getUserMessage func() (string, bool)
}

var scanner *bufio.Scanner

func main() {

	if err := godotenv.Load(); err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}

	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	if err := validateAPIKey(anthropicAPIKey); err != nil {
		log.Fatal(err)
	}
	client := anthropic.NewClient(
		option.WithAPIKey(anthropicAPIKey),
	)

	scanner = bufio.NewScanner(os.Stdin)

	agent := NewAgent(&client, getUserMessage)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
	}

}

func getUserMessage() (string, bool) {
	if !scanner.Scan() {
		return "", false
	}
	return scanner.Text(), true
}

func NewAgent(client *anthropic.Client, getUserMessage func() (string, bool)) *Agent {
	return &Agent{
		client:         client,
		getUserMessage: getUserMessage,
	}
}

func (a *Agent) Run(ctx context.Context) error {
	conversation := make([]anthropic.MessageParam, 0)
	fmt.Println("Chat with Claude (use 'ctrl-c' to quit)")

	for {
		fmt.Print("\u001b[94mYou\u001b[0m: ")
		userInput, ok := a.getUserMessage()
		if !ok {
			break
		}

		if strings.TrimSpace(userInput) == "" {
			continue
		}
		userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
		conversation = append(conversation, userMessage)

		message, err := a.runInference(ctx, conversation)
		if err != nil {
			return err
		}
		conversation = append(conversation, message.ToParam())

		for _, content := range message.Content {
			switch content.Type {
			case "text":
				fmt.Printf("\u001b[93mClaude\u001b[0m: %s\n", content.Text)
			}
		}
	}
	return nil
}

func (a *Agent) runInference(ctx context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	message, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeSonnet4_6,
		MaxTokens: int64(1024),
		Messages:  conversation,
	})
	return message, err
}

func validateAPIKey(apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("no API key was found - please add an api key in the environment variables.")
	}

	if !strings.HasPrefix(apiKey, "sk-ant-") {
		return fmt.Errorf("an API key was found, but it doesn't start sk-proj-; please check you're using the right key - see troubleshooting notebook")
	}

	if strings.TrimSpace(apiKey) != apiKey {
		return fmt.Errorf("an API key was found, but it looks like it might have space or tab characters at the start or end - please remove them - see troubleshooting notebook")
	}

	slog.Info("API key found and looks good so far!")
	return nil
}
