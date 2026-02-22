package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/obeak/gitbeak-mcp/internal/mcp"
)

func main() {
	baseURL := os.Getenv("OPENBEAK_BASE_URL")
	tokenFile := os.Getenv("OPENBEAK_TOKEN_FILE")

	client := mcp.NewOpenBeakClient(baseURL, tokenFile)
	server := mcp.NewServer(os.Stdin, os.Stdout, client)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := server.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "gitbeak mcp server error:", err)
		os.Exit(1)
	}
}
