package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type stdioProtocol struct {
	r  *bufio.Reader
	w  *bufio.Writer
	mu sync.Mutex
}

func newStdioProtocol(r io.Reader, w io.Writer) *stdioProtocol {
	return &stdioProtocol{r: bufio.NewReader(r), w: bufio.NewWriter(w)}
}

func (p *stdioProtocol) read() (*rpcRequest, error) {
	tp := textproto.NewReader(p.r)
	headers, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	cl := strings.TrimSpace(headers.Get("Content-Length"))
	if cl == "" {
		return nil, fmt.Errorf("missing Content-Length")
	}
	n, err := strconv.Atoi(cl)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("invalid Content-Length: %q", cl)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(p.r, buf); err != nil {
		return nil, err
	}
	var req rpcRequest
	if err := json.Unmarshal(buf, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (p *stdioProtocol) write(resp rpcResponse) error {
	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := fmt.Fprintf(p.w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	if _, err := p.w.Write(payload); err != nil {
		return err
	}
	return p.w.Flush()
}
