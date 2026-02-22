package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

type Server struct {
	proto   *stdioProtocol
	client  *OpenBeakClient
	actions map[string]ActionDef
	tools   []map[string]any
}

func NewServer(r io.Reader, w io.Writer, client *OpenBeakClient) *Server {
	actions := map[string]ActionDef{}
	toolList := make([]map[string]any, 0, len(OpenBeakActions())+5)
	for _, action := range OpenBeakActions() {
		actions[action.Name] = action
		toolList = append(toolList, map[string]any{
			"name":        action.Name,
			"description": action.Description,
			"inputSchema": actionSchema(),
		})
	}

	toolList = append(toolList,
		map[string]any{"name": "openbeak_auth_status", "description": "Show stored token status", "inputSchema": emptySchema()},
		map[string]any{"name": "openbeak_auth_set_tokens", "description": "Set access/refresh tokens in local MCP store", "inputSchema": authSetSchema()},
		map[string]any{"name": "openbeak_auth_clear", "description": "Clear local stored tokens", "inputSchema": emptySchema()},
		map[string]any{"name": "openbeak_git_auth_url", "description": "Build Git Smart HTTP URL using current/auto-refreshed access token", "inputSchema": gitURLSchema()},
		map[string]any{"name": "openbeak_api_call", "description": "Raw API call to any OpenBeak endpoint", "inputSchema": rawCallSchema()},
	)

	sort.Slice(toolList, func(i, j int) bool {
		return toolList[i]["name"].(string) < toolList[j]["name"].(string)
	})

	return &Server{proto: newStdioProtocol(r, w), client: client, actions: actions, tools: toolList}
}

func (s *Server) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := s.proto.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if req.ID == nil {
			continue
		}
		resp := s.handle(ctx, req)
		if err := s.proto.write(resp); err != nil {
			return err
		}
	}
}

func (s *Server) handle(ctx context.Context, req *rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return ok(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "openbeak-mcp-go",
				"version": "0.1.0",
			},
		})
	case "ping":
		return ok(req.ID, map[string]any{})
	case "tools/list":
		return ok(req.ID, map[string]any{"tools": s.tools})
	case "tools/call":
		result, err := s.callTool(ctx, req.Params)
		if err != nil {
			return ok(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true})
		}
		text, _ := json.MarshalIndent(result, "", "  ")
		return ok(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}})
	default:
		return fail(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, err
	}

	switch call.Name {
	case "openbeak_auth_status":
		return s.client.Status()
	case "openbeak_auth_clear":
		return map[string]any{"ok": true}, s.client.ClearTokens()
	case "openbeak_auth_set_tokens":
		var args struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		if err := s.client.SetTokens(args.AccessToken, args.RefreshToken); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "openbeak_git_auth_url":
		var args struct {
			Owner    string `json:"owner"`
			Repo     string `json:"repo"`
			Username string `json:"username"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		u, err := s.client.GitAuthURL(ctx, args.Owner, args.Repo, args.Username)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": u}, nil
	case "openbeak_api_call":
		var args struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			actionArgs
		}
		if err := decodeArgs(call.Arguments, &args); err != nil {
			return nil, err
		}
		return s.client.APICall(ctx, args.Method, args.Path, args.actionArgs)
	default:
		action, ok := s.actions[call.Name]
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", call.Name)
		}
		args := actionArgs{}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return nil, err
			}
		}
		return s.client.DoAction(ctx, action, args)
	}
}

func decodeArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func ok(id any, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func fail(id any, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func emptySchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false}
}

func authSetSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"refresh_token"},
		"properties": map[string]any{
			"access_token":  map[string]any{"type": "string"},
			"refresh_token": map[string]any{"type": "string"},
		},
	}
}

func gitURLSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"owner", "repo", "username"},
		"properties": map[string]any{
			"owner":    map[string]any{"type": "string"},
			"repo":     map[string]any{"type": "string"},
			"username": map[string]any{"type": "string"},
		},
	}
}

func actionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"query": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"body": map[string]any{
				"type":                 "object",
				"additionalProperties": true,
			},
			"auth": map[string]any{"type": "boolean"},
			"pow":  map[string]any{"type": "boolean"},
		},
	}
}

func rawCallSchema() map[string]any {
	schema := actionSchema()
	schema["required"] = []string{"method", "path"}
	props := schema["properties"].(map[string]any)
	props["method"] = map[string]any{"type": "string"}
	props["path"] = map[string]any{"type": "string"}
	return schema
}
