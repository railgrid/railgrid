/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/railgrid/railgrid/pkg/apiurl"
)

// mcpProxyErrorCode is the JSON-RPC error code the proxy answers a request
// with when it cannot get the hub's reply (implementation-defined server
// error range).
const mcpProxyErrorCode = -32000

// mcpProxyMaxReply caps one non-streamed reply from the hub. The aggregate
// caps a provider's reply at 96 MiB; this leaves room for the envelope.
const mcpProxyMaxReply = 128 << 20

type mcpProxyOptions struct {
	target        hubTarget
	mcpserverName string
}

func newMCPProxyCommand() *cobra.Command {
	o := &mcpProxyOptions{}
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Serve the workspace MCP endpoint over stdio, authenticated as you",
		Long: `Serves the workspace's aggregate MCP endpoint to an MCP client over stdio.

Every message the client writes is forwarded to the hub with your own railgrid
login — the same credentials kubectl uses, OIDC tokens refreshed as they
expire — and the hub's replies are written back. Nothing needs to be pasted
into the client's configuration, and the hub's CA from your kubeconfig is
trusted, so a local hub needs no extra certificate settings.

Because the calls are made as you rather than with the workspace's MCP
ServiceAccount token ('railgrid mcp url'), org-owned providers — for example
an organization's self-hosted infrastructure — are federated too.

The workspace is the one the railgrid context points at when the client starts
the proxy; after 'railgrid use', restart the client's MCP connection. Pass
--org / --workspace to pin one instead.`,
		Example: `  # Claude Code
  claude mcp add railgrid -- railgrid mcp proxy

  # Codex
  codex mcp add railgrid -- railgrid mcp proxy

  # Claude Desktop, Cursor and other mcpServers configurations
  { "mcpServers": { "railgrid": { "command": "railgrid", "args": ["mcp", "proxy"] } } }`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			p := newMCPProxy(o.resolveTarget, cmd.OutOrStdout(), cmd.ErrOrStderr())
			return p.run(ctx, cmd.InOrStdin())
		},
	}
	o.target.addFlags(cmd)
	cmd.Flags().StringVar(&o.mcpserverName, "mcpserver-name", defaultMCPServerName, "Name of the aggregate MCPServer to serve")
	return cmd
}

// resolveTarget builds the endpoint and authenticated client from the railgrid
// kubeconfig context. Without --org / --workspace it needs no hub call.
func (o *mcpProxyOptions) resolveTarget(ctx context.Context) (*mcpProxyTarget, error) {
	var s *hubSession
	var err error
	if o.target.org == "" && o.target.workspace == "" {
		s, err = openHubSession()
	} else {
		s, err = newHubSession(ctx, o.target)
	}
	if err != nil {
		return nil, err
	}
	if s.Cluster == "" || s.Cluster == "default" {
		return nil, fmt.Errorf("kubeconfig context %q does not point at a workspace; run 'railgrid use'", s.Context)
	}
	return &mcpProxyTarget{url: apiurl.MCPServerURL(s.Hub, s.Cluster, o.mcpserverName), client: s.client}, nil
}

// mcpProxyTarget is where messages go and the client that authenticates them.
type mcpProxyTarget struct {
	url    string
	client *http.Client
}

// mcpProxy relays MCP's stdio transport (one JSON-RPC message per line) to
// the streamable HTTP transport of the hub's aggregate endpoint. Each message
// is one POST; requests are relayed concurrently, so a long tool call does not
// hold up the next message.
type mcpProxy struct {
	resolve func(context.Context) (*mcpProxyTarget, error)
	stderr  io.Writer

	resolveMu sync.Mutex // serializes resolve
	mu        sync.Mutex // guards the fields below
	target    *mcpProxyTarget
	inflight  map[string]context.CancelFunc
	// protocolVersion and sessionID are what the hub negotiated / issued;
	// streamable HTTP expects both back on every later request.
	protocolVersion string
	sessionID       string

	outMu sync.Mutex
	out   io.Writer
}

func newMCPProxy(resolve func(context.Context) (*mcpProxyTarget, error), out, stderr io.Writer) *mcpProxy {
	return &mcpProxy{resolve: resolve, out: out, stderr: stderr, inflight: map[string]context.CancelFunc{}}
}

// run relays messages from in until it is closed, then waits for the replies
// still in flight.
func (p *mcpProxy) run(ctx context.Context, in io.Reader) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	br := bufio.NewReader(in)
	for {
		line, err := br.ReadBytes('\n')
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			wg.Go(func() { p.handle(ctx, msg) })
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
	}
}

// jsonRPCMessage is the part of a client message the proxy looks at.
type jsonRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (p *mcpProxy) handle(ctx context.Context, raw []byte) {
	var msg jsonRPCMessage
	_ = json.Unmarshal(raw, &msg) // anything else is forwarded as is
	isRequest := msg.Method != "" && len(msg.ID) > 0 && string(msg.ID) != "null"

	// The hub's endpoint is stateless, so it has no request to cancel;
	// cancelling our POST is what stops the work.
	if msg.Method == "notifications/cancelled" {
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(msg.Params, &params) == nil {
			p.cancel(params.RequestID)
		}
		return
	}

	reqCtx := ctx
	if isRequest {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		p.track(msg.ID, cancel)
		defer p.untrack(msg.ID)
	}

	replies, err := p.forward(reqCtx, raw, msg.Method == "initialize")
	switch {
	case err != nil && reqCtx.Err() != nil && ctx.Err() == nil:
		// Cancelled by the client, which ignores any reply to it.
	case err != nil && isRequest:
		p.writeError(msg.ID, err)
	case err != nil:
		p.logf("%s: %v", describeMessage(msg), err)
	case isRequest && replies == 0:
		p.writeError(msg.ID, errors.New("the hub sent no reply"))
	}
}

// forward POSTs one message and relays every JSON-RPC message of the reply,
// returning how many it wrote. A 401 is retried once with credentials
// reloaded from the kubeconfig (a fresh 'railgrid login' or a rotated token).
func (p *mcpProxy) forward(ctx context.Context, raw []byte, initialize bool) (int, error) {
	for attempt := 0; ; attempt++ {
		t, err := p.currentTarget(ctx)
		if err != nil {
			return 0, err
		}
		resp, err := p.post(ctx, t, raw)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			p.dropTarget(t)
			if attempt == 0 {
				continue
			}
			return 0, errors.New("the hub rejected your credentials (HTTP 401); run 'railgrid login'")
		}
		defer resp.Body.Close() //nolint:errcheck
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			p.mu.Lock()
			p.sessionID = sid
			p.mu.Unlock()
		}
		switch {
		case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
			return 0, nil
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			return 0, decodeAPIError(http.MethodPost, resp.Request.URL.Path, resp.StatusCode, body)
		}
		return p.relay(resp, initialize)
	}
}

func (p *mcpProxy) post(ctx context.Context, t *mcpProxyTarget, raw []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", cliUserAgent())
	p.mu.Lock()
	if p.protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", p.protocolVersion)
	}
	if p.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", p.sessionID)
	}
	p.mu.Unlock()
	return t.client.Do(req)
}

// relay writes the JSON-RPC messages of one reply: the body of a JSON reply,
// or each event of an SSE stream as it arrives.
func (p *mcpProxy) relay(resp *http.Response, initialize bool) (int, error) {
	n := 0
	emit := func(payload []byte) {
		if p.emit(payload, initialize) {
			n++
		}
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, mcpProxyMaxReply))
		if err != nil {
			return n, fmt.Errorf("reading the hub's reply: %w", err)
		}
		if len(bytes.TrimSpace(body)) > 0 {
			emit(body)
		}
		return n, nil
	}
	br := bufio.NewReader(resp.Body)
	var data []string
	for {
		line, err := br.ReadString('\n')
		switch line = strings.TrimRight(line, "\r\n"); {
		case line == "" && len(data) > 0:
			emit([]byte(strings.Join(data, "\n")))
			data = nil
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if err != nil {
			if len(data) > 0 {
				emit([]byte(strings.Join(data, "\n")))
			}
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, fmt.Errorf("reading the hub's event stream: %w", err)
		}
	}
}

// emit writes one JSON-RPC message as a single line. The first reply to
// initialize carries the protocol version later requests must declare; it is
// recorded before the reply is written, because the client only sends its
// next request after reading it.
func (p *mcpProxy) emit(payload []byte, initialize bool) bool {
	var line bytes.Buffer
	if err := json.Compact(&line, payload); err != nil {
		p.logf("dropping a reply that is not JSON: %v", err)
		return false
	}
	if initialize {
		var reply struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if json.Unmarshal(line.Bytes(), &reply) == nil && reply.Result.ProtocolVersion != "" {
			p.mu.Lock()
			p.protocolVersion = reply.Result.ProtocolVersion
			p.mu.Unlock()
		}
	}
	line.WriteByte('\n')
	p.outMu.Lock()
	defer p.outMu.Unlock()
	_, _ = p.out.Write(line.Bytes())
	return true
}

func (p *mcpProxy) writeError(id json.RawMessage, err error) {
	reply, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": mcpProxyErrorCode, "message": err.Error()},
	})
	p.emit(reply, false)
}

// currentTarget returns the resolved endpoint, resolving it on first use —
// and again after a 401 or a failed resolution, so a client started before
// 'railgrid login' recovers without a restart.
func (p *mcpProxy) currentTarget(ctx context.Context) (*mcpProxyTarget, error) {
	p.mu.Lock()
	t := p.target
	p.mu.Unlock()
	if t != nil {
		return t, nil
	}
	p.resolveMu.Lock()
	defer p.resolveMu.Unlock()
	p.mu.Lock()
	t = p.target
	p.mu.Unlock()
	if t != nil {
		return t, nil
	}
	t, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	p.logf("forwarding to %s as the logged-in user", t.url)
	p.mu.Lock()
	p.target = t
	p.mu.Unlock()
	return t, nil
}

func (p *mcpProxy) dropTarget(t *mcpProxyTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.target == t {
		p.target = nil
	}
}

func (p *mcpProxy) track(id json.RawMessage, cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight[string(id)] = cancel
}

func (p *mcpProxy) untrack(id json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inflight, string(id))
}

func (p *mcpProxy) cancel(id json.RawMessage) {
	p.mu.Lock()
	cancel := p.inflight[string(id)]
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// logf writes a diagnostic to stderr, which MCP clients keep as the server's
// log; stdout carries only JSON-RPC.
func (p *mcpProxy) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.stderr, "railgrid mcp proxy: "+format+"\n", args...)
}

func describeMessage(msg jsonRPCMessage) string {
	if msg.Method != "" {
		return msg.Method
	}
	if len(msg.ID) > 0 {
		return "reply " + string(msg.ID)
	}
	return "message"
}
