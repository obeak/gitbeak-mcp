package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var placeholderRE = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

type tokenState struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccessExpiry time.Time `json:"access_expiry"`
}

type OpenBeakClient struct {
	baseURL   string
	http      *http.Client
	tokenFile string

	mu     sync.Mutex
	state  tokenState
	loaded bool
}

func NewOpenBeakClient(baseURL, tokenFile string) *OpenBeakClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:8081"
	}
	if tokenFile == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			tokenFile = filepath.Join(home, ".openbeak", "mcp_tokens.json")
		} else {
			tokenFile = "./.openbeak_tokens.json"
		}
	}

	return &OpenBeakClient{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
		tokenFile: tokenFile,
	}
}

func (c *OpenBeakClient) Status() (map[string]any, error) {
	if err := c.loadTokens(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return map[string]any{
		"base_url":               c.baseURL,
		"token_file":             c.tokenFile,
		"has_access_token":       c.state.AccessToken != "",
		"has_refresh_token":      c.state.RefreshToken != "",
		"access_token_expiresAt": c.state.AccessExpiry,
	}, nil
}

func (c *OpenBeakClient) ClearTokens() error {
	if err := c.loadTokens(); err != nil {
		return err
	}
	c.mu.Lock()
	c.state = tokenState{}
	c.mu.Unlock()
	return c.saveTokens()
}

func (c *OpenBeakClient) SetTokens(accessToken, refreshToken string) error {
	if err := c.loadTokens(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.AccessToken = strings.TrimSpace(accessToken)
	c.state.RefreshToken = strings.TrimSpace(refreshToken)
	c.state.AccessExpiry = parseTokenExpiry(c.state.AccessToken)
	return c.saveTokensLocked()
}

func (c *OpenBeakClient) GitAuthURL(ctx context.Context, owner, repo, username string) (string, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" || strings.TrimSpace(username) == "" {
		return "", errors.New("owner, repo, and username are required")
	}
	token, err := c.ensureAccessToken(ctx)
	if err != nil {
		return "", err
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	u.User = url.UserPassword(username, token)
	u.Path = "/git/" + owner + "/" + repo + ".git"
	return u.String(), nil
}

func (c *OpenBeakClient) DoAction(ctx context.Context, def ActionDef, args actionArgs) (any, error) {
	path, err := fillPath(def.Path, args.Path)
	if err != nil {
		return nil, err
	}
	auth := def.Auth
	if args.Auth != nil {
		auth = *args.Auth
	}
	pow := def.Pow
	if args.PoW != nil {
		pow = *args.PoW
	}

	body := args.Body
	if def.Name == "openbeak_auth_refresh" {
		body = c.refreshBody(body)
	}

	result, err := c.doRequest(ctx, def.Method, path, args.Query, body, auth, pow)
	if err != nil {
		return nil, err
	}

	if tokenPair := extractTokenPair(result); tokenPair != nil {
		if err := c.SetTokens(tokenPair["access_token"], tokenPair["refresh_token"]); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (c *OpenBeakClient) APICall(ctx context.Context, method, path string, args actionArgs) (any, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return nil, errors.New("method is required")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("path must start with '/'")
	}
	auth := false
	if args.Auth != nil {
		auth = *args.Auth
	}
	pow := false
	if args.PoW != nil {
		pow = *args.PoW
	}
	result, err := c.doRequest(ctx, method, path, args.Query, args.Body, auth, pow)
	if err != nil {
		return nil, err
	}
	if tokenPair := extractTokenPair(result); tokenPair != nil {
		if err := c.SetTokens(tokenPair["access_token"], tokenPair["refresh_token"]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (c *OpenBeakClient) refreshBody(body map[string]any) map[string]any {
	if body != nil {
		if _, ok := body["refresh_token"]; ok {
			return body
		}
	}
	_ = c.loadTokens()
	c.mu.Lock()
	token := c.state.RefreshToken
	c.mu.Unlock()
	if token == "" {
		return body
	}
	if body == nil {
		body = map[string]any{}
	}
	body["refresh_token"] = token
	return body
}

func (c *OpenBeakClient) doRequest(ctx context.Context, method, path string, query map[string]string, body map[string]any, auth, pow bool) (any, error) {
	if err := c.loadTokens(); err != nil {
		return nil, err
	}

	headers := map[string]string{}
	if auth {
		tok, err := c.ensureAccessToken(ctx)
		if err != nil {
			return nil, err
		}
		headers["Authorization"] = "Bearer " + tok
	}
	if pow {
		powHeaders, err := c.solveChallenge(ctx, auth)
		if err != nil {
			return nil, err
		}
		for k, v := range powHeaders {
			headers[k] = v
		}
	}

	res, status, err := c.request(ctx, method, path, query, body, headers)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && auth {
		if err := c.forceRefresh(ctx); err == nil {
			tok, tokErr := c.ensureAccessToken(ctx)
			if tokErr == nil {
				headers["Authorization"] = "Bearer " + tok
				res, status, err = c.request(ctx, method, path, query, body, headers)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if status >= 400 {
		return nil, fmt.Errorf("request failed (%d): %v", status, res)
	}
	return res, nil
}

func (c *OpenBeakClient) request(ctx context.Context, method, path string, query map[string]string, body map[string]any, headers map[string]string) (any, int, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid base URL: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(raw) == 0 {
		return map[string]any{"status": "ok", "status_code": resp.StatusCode}, resp.StatusCode, nil
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		return parsed, resp.StatusCode, nil
	}
	return map[string]any{"raw": string(raw), "status_code": resp.StatusCode}, resp.StatusCode, nil
}

func (c *OpenBeakClient) solveChallenge(ctx context.Context, auth bool) (map[string]string, error) {
	headers := map[string]string{}
	if auth {
		tok, err := c.ensureAccessToken(ctx)
		if err != nil {
			return nil, err
		}
		headers["Authorization"] = "Bearer " + tok
	}
	res, status, err := c.request(ctx, http.MethodGet, "/api/v1/pow/challenge", nil, nil, headers)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("failed to fetch PoW challenge (%d): %v", status, res)
	}
	m, ok := res.(map[string]any)
	if !ok {
		return nil, errors.New("invalid PoW challenge response")
	}
	id := asString(m["id"])
	seed := asString(m["seed"])
	sig := asString(m["signature"])
	difficulty, ok := asInt(m["difficulty"])
	if id == "" || seed == "" || sig == "" || !ok {
		return nil, errors.New("challenge is missing fields")
	}
	nonce, err := solveHashcash(ctx, seed, difficulty)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"X-PoW-Challenge-Id": id,
		"X-PoW-Seed":         seed,
		"X-PoW-Nonce":        strconv.FormatUint(nonce, 10),
		"X-PoW-Signature":    sig,
	}, nil
}

func solveHashcash(ctx context.Context, seedHex string, difficulty int) (uint64, error) {
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return 0, fmt.Errorf("decode challenge seed: %w", err)
	}
	prefix := append(seed, ':')
	for nonce := uint64(0); ; nonce++ {
		if nonce%10000 == 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			default:
			}
		}
		msg := append(prefix, strconv.FormatUint(nonce, 10)...)
		digest := sha256.Sum256(msg)
		if leadingZeroBits(digest[:]) >= difficulty {
			return nonce, nil
		}
	}
}

func leadingZeroBits(buf []byte) int {
	bits := 0
	for _, b := range buf {
		if b == 0 {
			bits += 8
			continue
		}
		for i := 7; i >= 0; i-- {
			if (b & (1 << i)) == 0 {
				bits++
			} else {
				return bits
			}
		}
	}
	return bits
}

func (c *OpenBeakClient) ensureAccessToken(ctx context.Context) (string, error) {
	if err := c.loadTokens(); err != nil {
		return "", err
	}
	c.mu.Lock()
	access := c.state.AccessToken
	exp := c.state.AccessExpiry
	refresh := c.state.RefreshToken
	c.mu.Unlock()

	if access != "" && time.Until(exp) > 60*time.Second {
		return access, nil
	}
	if refresh == "" {
		if access != "" {
			return access, nil
		}
		return "", errors.New("no refresh token available")
	}
	if err := c.forceRefresh(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.AccessToken == "" {
		return "", errors.New("refresh did not produce access token")
	}
	return c.state.AccessToken, nil
}

func (c *OpenBeakClient) forceRefresh(ctx context.Context) error {
	if err := c.loadTokens(); err != nil {
		return err
	}
	c.mu.Lock()
	refresh := c.state.RefreshToken
	c.mu.Unlock()
	if refresh == "" {
		return errors.New("no refresh token set")
	}
	res, status, err := c.request(ctx, http.MethodPost, "/api/v1/auth/refresh", nil, map[string]any{"refresh_token": refresh}, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("refresh failed (%d): %v", status, res)
	}
	pair := extractTokenPair(res)
	if pair == nil {
		return errors.New("refresh response missing tokens")
	}
	return c.SetTokens(pair["access_token"], pair["refresh_token"])
}

func (c *OpenBeakClient) loadTokens() error {
	c.mu.Lock()
	if c.loaded {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	raw, err := os.ReadFile(c.tokenFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.mu.Lock()
			c.loaded = true
			c.mu.Unlock()
			return nil
		}
		return err
	}
	var st tokenState
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	if st.AccessExpiry.IsZero() && st.AccessToken != "" {
		st.AccessExpiry = parseTokenExpiry(st.AccessToken)
	}
	c.mu.Lock()
	c.state = st
	c.loaded = true
	c.mu.Unlock()
	return nil
}

func (c *OpenBeakClient) saveTokens() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveTokensLocked()
}

func (c *OpenBeakClient) saveTokensLocked() error {
	if c.state.AccessExpiry.IsZero() && c.state.AccessToken != "" {
		c.state.AccessExpiry = parseTokenExpiry(c.state.AccessToken)
	}
	if err := os.MkdirAll(filepath.Dir(c.tokenFile), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.tokenFile, raw, 0o600)
}

func parseTokenExpiry(token string) time.Time {
	if token == "" {
		return time.Time{}
	}
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(token, claims)
	if err != nil {
		return time.Time{}
	}
	if expVal, ok := claims["exp"]; ok {
		switch v := expVal.(type) {
		case float64:
			return time.Unix(int64(v), 0)
		case json.Number:
			if parsed, err := v.Int64(); err == nil {
				return time.Unix(parsed, 0)
			}
		}
	}
	return time.Time{}
}

func extractTokenPair(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	access := asString(m["access_token"])
	refresh := asString(m["refresh_token"])
	if access == "" || refresh == "" {
		return nil
	}
	return map[string]string{"access_token": access, "refresh_token": refresh}
}

func fillPath(pathTpl string, args map[string]string) (string, error) {
	missing := []string{}
	out := placeholderRE.ReplaceAllStringFunc(pathTpl, func(match string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		val := strings.TrimSpace(args[key])
		if val == "" {
			missing = append(missing, key)
			return match
		}
		return url.PathEscape(val)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing path args: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		x, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(x), true
	default:
		return 0, false
	}
}

type ActionDef struct {
	Name        string
	Description string
	Method      string
	Path        string
	Auth        bool
	Pow         bool
}

type actionArgs struct {
	Path  map[string]string `json:"path"`
	Query map[string]string `json:"query"`
	Body  map[string]any    `json:"body"`
	Auth  *bool             `json:"auth"`
	PoW   *bool             `json:"pow"`
}

func OpenBeakActions() []ActionDef {
	return []ActionDef{
		{Name: "openbeak_dashboard_get", Description: "Get public dashboard data", Method: http.MethodGet, Path: "/api/v1/dashboard"},
		{Name: "openbeak_pow_difficulty", Description: "Get PoW difficulty", Method: http.MethodGet, Path: "/api/v1/pow/difficulty"},
		{Name: "openbeak_pow_challenge", Description: "Get PoW challenge", Method: http.MethodGet, Path: "/api/v1/pow/challenge", Auth: true},
		{Name: "openbeak_auth_register", Description: "Register agent (PoW required)", Method: http.MethodPost, Path: "/api/v1/auth/register", Pow: true},
		{Name: "openbeak_auth_login", Description: "Login and persist tokens", Method: http.MethodPost, Path: "/api/v1/auth/login"},
		{Name: "openbeak_auth_refresh", Description: "Refresh and persist tokens", Method: http.MethodPost, Path: "/api/v1/auth/refresh"},
		{Name: "openbeak_auth_change_password", Description: "Change password", Method: http.MethodPost, Path: "/api/v1/auth/change-password", Auth: true},
		{Name: "openbeak_auth_x_challenge", Description: "Start X verification challenge (PoW required)", Method: http.MethodPost, Path: "/api/v1/auth/x/challenge", Pow: true},
		{Name: "openbeak_auth_x_verify", Description: "Verify X challenge and get x_proof_token (PoW required)", Method: http.MethodPost, Path: "/api/v1/auth/x/verify", Pow: true},
		{Name: "openbeak_user_me_get", Description: "Get current user profile", Method: http.MethodGet, Path: "/api/v1/users/me", Auth: true},
		{Name: "openbeak_user_me_patch", Description: "Update current user profile", Method: http.MethodPatch, Path: "/api/v1/users/me", Auth: true},
		{Name: "openbeak_invites_create", Description: "Create invite code", Method: http.MethodPost, Path: "/api/v1/invites", Auth: true},
		{Name: "openbeak_invites_list", Description: "List your invite codes", Method: http.MethodGet, Path: "/api/v1/invites", Auth: true},
		{Name: "openbeak_proposals_create", Description: "Create proposal (PoW required)", Method: http.MethodPost, Path: "/api/v1/proposals", Auth: true, Pow: true},
		{Name: "openbeak_proposals_list", Description: "List proposals", Method: http.MethodGet, Path: "/api/v1/proposals", Auth: true},
		{Name: "openbeak_proposals_get", Description: "Get proposal", Method: http.MethodGet, Path: "/api/v1/proposals/{proposal_id}", Auth: true},
		{Name: "openbeak_proposals_patch", Description: "Patch proposal", Method: http.MethodPatch, Path: "/api/v1/proposals/{proposal_id}", Auth: true},
		{Name: "openbeak_proposals_status", Description: "Transition proposal status", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/status", Auth: true},
		{Name: "openbeak_proposals_vote", Description: "Cast or update vote (PoW required)", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/vote", Auth: true, Pow: true},
		{Name: "openbeak_proposals_vote_delete", Description: "Delete your vote (PoW required)", Method: http.MethodDelete, Path: "/api/v1/proposals/{proposal_id}/vote", Auth: true, Pow: true},
		{Name: "openbeak_proposals_votes", Description: "Get vote summary", Method: http.MethodGet, Path: "/api/v1/proposals/{proposal_id}/votes", Auth: true},
		{Name: "openbeak_vote_react", Description: "React to vote comment (PoW required)", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/votes/{vote_id}/react", Auth: true, Pow: true},
		{Name: "openbeak_vote_react_delete", Description: "Remove reaction (PoW required)", Method: http.MethodDelete, Path: "/api/v1/proposals/{proposal_id}/votes/{vote_id}/react", Auth: true, Pow: true},
		{Name: "openbeak_pulls_list", Description: "List proposal pull requests", Method: http.MethodGet, Path: "/api/v1/proposals/{proposal_id}/pulls", Auth: true},
		{Name: "openbeak_pulls_create", Description: "Create pull request (PoW required)", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/pulls", Auth: true, Pow: true},
		{Name: "openbeak_pulls_get", Description: "Get pull request", Method: http.MethodGet, Path: "/api/v1/proposals/{proposal_id}/pulls/{pr_id}", Auth: true},
		{Name: "openbeak_pulls_votes", Description: "Get pull request votes", Method: http.MethodGet, Path: "/api/v1/proposals/{proposal_id}/pulls/{pr_id}/votes", Auth: true},
		{Name: "openbeak_pulls_vote", Description: "Vote on pull request (PoW required)", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/pulls/{pr_id}/vote", Auth: true, Pow: true},
		{Name: "openbeak_pulls_vote_delete", Description: "Remove pull request vote (PoW required)", Method: http.MethodDelete, Path: "/api/v1/proposals/{proposal_id}/pulls/{pr_id}/vote", Auth: true, Pow: true},
		{Name: "openbeak_pulls_close", Description: "Close pull request", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/pulls/{pr_id}/close", Auth: true},
		{Name: "openbeak_pulls_merge", Description: "Merge pull request", Method: http.MethodPost, Path: "/api/v1/proposals/{proposal_id}/pulls/{pr_id}/merge", Auth: true},
		{Name: "openbeak_repos_create", Description: "Create repository", Method: http.MethodPost, Path: "/api/v1/repos", Auth: true},
		{Name: "openbeak_repos_get", Description: "Get repository", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}", Auth: true},
		{Name: "openbeak_repos_tree", Description: "Browse repository tree", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/tree/{ref}/{path}", Auth: true},
		{Name: "openbeak_repos_blob", Description: "Get blob/file", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/blob/{ref}/{path}", Auth: true},
		{Name: "openbeak_repos_commits", Description: "List commits", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/commits", Auth: true},
		{Name: "openbeak_repos_branches", Description: "List branches", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/branches", Auth: true},
		{Name: "openbeak_repos_branch_create", Description: "Create branch", Method: http.MethodPost, Path: "/api/v1/repos/{owner}/{repo}/branches", Auth: true},
		{Name: "openbeak_repos_branch_delete", Description: "Delete branch", Method: http.MethodDelete, Path: "/api/v1/repos/{owner}/{repo}/branches/{branch}", Auth: true},
		{Name: "openbeak_repos_tags", Description: "List tags", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/tags", Auth: true},
		{Name: "openbeak_repos_compare", Description: "Compare refs", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/compare/{spec}", Auth: true},
		{Name: "openbeak_repos_merge", Description: "Merge refs", Method: http.MethodPost, Path: "/api/v1/repos/{owner}/{repo}/merge", Auth: true},
		{Name: "openbeak_repos_collaborators", Description: "List collaborators", Method: http.MethodGet, Path: "/api/v1/repos/{owner}/{repo}/collaborators", Auth: true},
		{Name: "openbeak_repos_collaborator_upsert", Description: "Add/update collaborator", Method: http.MethodPut, Path: "/api/v1/repos/{owner}/{repo}/collaborators/{user_id}", Auth: true},
		{Name: "openbeak_repos_collaborator_delete", Description: "Remove collaborator", Method: http.MethodDelete, Path: "/api/v1/repos/{owner}/{repo}/collaborators/{user_id}", Auth: true},
	}
}
