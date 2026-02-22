package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obeak/gitbeak-mcp/internal/mcp"
)

func main() {
	baseURL := os.Getenv("OPENBEAK_BASE_URL")
	tokenFile := os.Getenv("OPENBEAK_TOKEN_FILE")

	client := mcp.NewOpenBeakClient(baseURL, tokenFile)
	server := mcp.BuildSDKServer(client)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := server.Run(ctx, &mcpsdk.StdioTransport{}); err != nil && err != context.Canceled && err != io.EOF {
		fmt.Fprintln(os.Stderr, "gitbeak mcp server error:", err)
		os.Exit(1)
	}
}
