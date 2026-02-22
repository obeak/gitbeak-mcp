package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func BuildSDKServer(client *OpenBeakClient) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "gitbeak-mcp-go",
		Version: "0.2.0",
	}, nil)

	for _, action := range OpenBeakActions() {
		registerActionTool(server, client, action)
	}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "openbeak_auth_status",
		Description: "Show stored token status",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args struct{}) (*mcpsdk.CallToolResult, any, error) {
		result, err := client.Status()
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolJSON(result), nil, nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "openbeak_auth_clear",
		Description: "Clear local stored tokens",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args struct{}) (*mcpsdk.CallToolResult, any, error) {
		if err := client.ClearTokens(); err != nil {
			return toolError(err), nil, nil
		}
		return toolJSON(map[string]any{"ok": true}), nil, nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "openbeak_auth_set_tokens",
		Description: "Set access/refresh tokens in local MCP store",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args authSetTokensArgs) (*mcpsdk.CallToolResult, any, error) {
		if err := client.SetTokens(args.AccessToken, args.RefreshToken); err != nil {
			return toolError(err), nil, nil
		}
		return toolJSON(map[string]any{"ok": true}), nil, nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "openbeak_git_auth_url",
		Description: "Build Git Smart HTTP URL using current/auto-refreshed access token",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args gitAuthURLArgs) (*mcpsdk.CallToolResult, any, error) {
		u, err := client.GitAuthURL(ctx, args.Owner, args.Repo, args.Username)
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolJSON(map[string]any{"url": u}), nil, nil
	})

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "openbeak_api_call",
		Description: "Raw API call to any OpenBeak endpoint",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args rawAPICallArgs) (*mcpsdk.CallToolResult, any, error) {
		result, err := client.APICall(ctx, args.Method, args.Path, actionArgs{
			Path:  args.PathParams,
			Query: args.Query,
			Body:  args.Body,
			Auth:  args.Auth,
			PoW:   args.PoW,
		})
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolJSON(result), nil, nil
	})

	return server
}

func registerActionTool(server *mcpsdk.Server, client *OpenBeakClient, action ActionDef) {
	def := action
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        def.Name,
		Description: def.Description,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args actionArgs) (*mcpsdk.CallToolResult, any, error) {
		result, err := client.DoAction(ctx, def, args)
		if err != nil {
			return toolError(err), nil, nil
		}
		return toolJSON(result), nil, nil
	})
}

type authSetTokensArgs struct {
	AccessToken  string `json:"access_token" jsonschema:"Stored access token (optional)"`
	RefreshToken string `json:"refresh_token" jsonschema:"Stored refresh token"`
}

type gitAuthURLArgs struct {
	Owner    string `json:"owner" jsonschema:"Repository owner"`
	Repo     string `json:"repo" jsonschema:"Repository name"`
	Username string `json:"username" jsonschema:"Git username for basic auth URL"`
}

type rawAPICallArgs struct {
	Method     string            `json:"method" jsonschema:"HTTP method (GET/POST/PATCH/PUT/DELETE)"`
	Path       string            `json:"path" jsonschema:"Absolute API path beginning with /"`
	PathParams map[string]string `json:"path_params,omitempty" jsonschema:"Optional path template values"`
	Query      map[string]string `json:"query,omitempty" jsonschema:"Optional query string map"`
	Body       map[string]any    `json:"body,omitempty" jsonschema:"Optional JSON body"`
	Auth       *bool             `json:"auth,omitempty" jsonschema:"Force auth behavior"`
	PoW        *bool             `json:"pow,omitempty" jsonschema:"Force PoW behavior"`
}

func toolJSON(v any) *mcpsdk.CallToolResult {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("failed to encode tool response: %w", err))
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: string(raw)},
		},
	}
}

func toolError(err error) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: err.Error()},
		},
	}
}
