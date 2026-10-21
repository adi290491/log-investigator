package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log-investigator/agent"
	"log-investigator/mcp"

	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/joho/godotenv"
)

func main() {

	serverPath := flag.String("server", "./bin/server", "path to the MCP server binary")
	flag.Parse()

	apiKey, err := loadAPIKey()
	if err != nil {
		log.Fatal(err)
	}

	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	ctx := context.Background()
	session, err := mcp.ConnectMCP(ctx, *serverPath)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	toolsList, err := session.ListTools(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}

	for _, t := range toolsList.Tools {
		fmt.Printf("— %s: %s\n", t.Name, t.Description)
	}

	tools, err := mcp.MCPToolsToAnthropicTools(toolsList.Tools)
	if err != nil {
		log.Fatal(err)
	}

	agent := agent.NewAgent(&client, session, tools, stdinReader())
	if err := agent.Run(ctx); err != nil {
		log.Fatalf("Error: %v", err)
	}

}

func stdinReader() func() (string, bool) {
	scanner := bufio.NewScanner(os.Stdin)
	return func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}
		return scanner.Text(), true
	}
}

func loadAPIKey() (string, error) {

	if err := godotenv.Load(); err != nil {
		return "", fmt.Errorf("Error loading .env file: %w", err)
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	switch {
	case apiKey == "":
		return "", errors.New("ANTHROPIC_API_KEY is not set - add it to .env or the environment")
	case strings.TrimSpace(apiKey) != apiKey:
		return "", errors.New("ANTHROPIC_API_KEY has leading or trailing whitespace")
	case !strings.HasPrefix(apiKey, "sk-ant-"):
		return "", errors.New("ANTHROPIC_API_KEY doesn't start with sk-ant- - check you're using an Anthropic key")
	}

	slog.Info("API key found and looks good so far!")
	return apiKey, nil
}
