/*
Copyright 2026 The Faros Authors.

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
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/transport"

	"github.com/faroshq/faros/pkg/apiurl"
	pkgversion "github.com/faroshq/faros/pkg/version"
)

// hubRESTTimeout bounds a single non-streaming REST call. Project creation and
// promotion do real work server-side, so this is generous.
const hubRESTTimeout = 2 * time.Minute

// defaultMCPServerName is the aggregate MCPServer every workspace gets.
const defaultMCPServerName = "default"

// cliUserAgent identifies the CLI. The hub sits behind Cloudflare, which
// rejects some default HTTP-library user agents with a 403 that looks like an
// RBAC failure, so every request sets it explicitly.
func cliUserAgent() string {
	return "faros-cli/" + pkgversion.Get()
}

// hubTarget carries the --org / --workspace overrides shared by the commands
// that call hub and provider REST APIs as the logged-in user.
type hubTarget struct {
	org       string
	workspace string
}

func (t *hubTarget) addFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&t.org, "org", "", "Organization display name or UUID (default: the org that owns the kubeconfig's workspace)")
	cmd.PersistentFlags().StringVar(&t.workspace, "workspace", "", "Workspace display name or UUID (default: the workspace the kubeconfig points at)")
}

// hubSession is everything a command needs to call the hub as the user: the
// hub base URL, the kcp cluster of the active workspace, the org and
// workspace UUIDs the tenant headers carry, and an authenticated client.
type hubSession struct {
	Context string
	Hub     string
	Cluster string
	Org     orgView
	WS      workspaceView

	restConfig *rest.Config
	// client authenticates with the kubeconfig's credentials (static token or
	// exec OIDC plugin, refreshed on demand).
	client *http.Client
	// plain carries only the kubeconfig's TLS settings; used for calls that
	// bring their own bearer (the MCP endpoint).
	plain *http.Client
}

// newHubSession loads the faros kubeconfig context and resolves the org and
// workspace UUIDs by matching the context's cluster against the workspaces the
// user can see (or against the --org / --workspace overrides).
func newHubSession(ctx context.Context, target hubTarget) (*hubSession, error) {
	s, err := openHubSession()
	if err != nil {
		return nil, err
	}
	if err := s.resolveTenant(ctx, target); err != nil {
		return nil, err
	}
	return s, nil
}

// openHubSession builds an authenticated session from the faros kubeconfig
// context without resolving the tenant. Commands that only need the hub and
// the caller's identity (whoami, org list) start here.
func openHubSession() (*hubSession, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	raw, err := loadingRules.GetStartingConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	ctxName, kctx, err := resolveFarosContext(raw)
	if err != nil {
		return nil, err
	}
	cluster := raw.Clusters[kctx.Cluster]
	if cluster == nil || cluster.Server == "" {
		return nil, fmt.Errorf("kubeconfig context %q references missing cluster %q", ctxName, kctx.Cluster)
	}
	base, clusterName := apiurl.SplitBaseAndCluster(cluster.Server)

	clientConfig := clientcmd.NewNonInteractiveClientConfig(*raw, ctxName, &clientcmd.ConfigOverrides{}, loadingRules)
	restCfg, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building client config: %w", err)
	}
	if globalInsecureTLS {
		restCfg.Insecure = true
		restCfg.CAData = nil
		restCfg.CAFile = ""
	}
	rt, err := rest.TransportFor(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building HTTP transport: %w", err)
	}
	transportConfig, err := restCfg.TransportConfig()
	if err != nil {
		return nil, err
	}
	tlsConfig, err := transport.TLSConfigFor(transportConfig)
	if err != nil {
		return nil, fmt.Errorf("building TLS config: %w", err)
	}
	plainTransport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		plainTransport.TLSClientConfig = tlsConfig
	}

	return &hubSession{
		Context:    ctxName,
		Hub:        base,
		Cluster:    clusterName,
		restConfig: restCfg,
		client:     &http.Client{Transport: rt},
		plain:      &http.Client{Transport: plainTransport},
	}, nil
}

// resolveOrg fills Org only. With --org it matches by name or UUID; without
// it, the org owning the kubeconfig's workspace wins, then the only org the
// user belongs to, otherwise the caller must disambiguate.
func (s *hubSession) resolveOrg(ctx context.Context, orgFlag string) error {
	listCtx, cancel := context.WithTimeout(ctx, hubRESTTimeout)
	defer cancel()
	orgs, err := fetchOrgs(listCtx, s.client, s.Hub)
	if err != nil {
		return withLoginHint(err)
	}
	if len(orgs) == 0 {
		return fmt.Errorf("you are not a member of any organizations")
	}
	if orgFlag != "" {
		o, err := matchOrg(orgs, orgFlag)
		if err != nil {
			return err
		}
		s.Org = o
		return nil
	}
	if err := s.resolveTenant(ctx, hubTarget{}); err == nil {
		s.WS = workspaceView{} // org-scope commands must not send X-Faros-Workspace
		return nil
	}
	if len(orgs) == 1 {
		s.Org = orgs[0]
		return nil
	}
	names := make([]string, len(orgs))
	for i, o := range orgs {
		names[i] = displayLabel(o.DisplayName, o.UUID)
	}
	return fmt.Errorf("the kubeconfig does not point at a workspace of any of your %d organizations; pass --org (one of: %s)", len(orgs), strings.Join(names, ", "))
}

// orgScoped returns a copy of the session that sends only the org tenant
// header. The hub's tenant middleware requires an exact (org, workspace)
// membership row whenever X-Faros-Workspace is present, which an org admin
// without a row in that workspace would fail; org-scope routes must not
// carry it.
func (s *hubSession) orgScoped() *hubSession {
	c := *s
	c.WS = workspaceView{}
	return &c
}

// resolveTenant fills Org and WS. Without overrides it finds the workspace
// whose clusterName is the kubeconfig's /clusters/<id>; with overrides it
// matches by display name or UUID and retargets Cluster to that workspace.
func (s *hubSession) resolveTenant(ctx context.Context, target hubTarget) error {
	listCtx, cancel := context.WithTimeout(ctx, hubRESTTimeout)
	defer cancel()
	orgs, err := fetchOrgs(listCtx, s.client, s.Hub)
	if err != nil {
		return withLoginHint(err)
	}
	if len(orgs) == 0 {
		return fmt.Errorf("you are not a member of any organizations")
	}
	candidates := orgs
	if target.org != "" {
		o, err := matchOrg(orgs, target.org)
		if err != nil {
			return err
		}
		candidates = []orgView{o}
	}

	type hit struct {
		org        orgView
		workspaces []workspaceView
	}
	var hits []hit
	var lastErr error
	for _, o := range candidates {
		wss, err := fetchWorkspaces(listCtx, s.client, s.Hub, o.UUID)
		if err != nil {
			if target.org != "" {
				return err
			}
			lastErr = err
			continue
		}
		var matched []workspaceView
		for _, ws := range wss {
			if workspaceMatches(ws, target.workspace, s.Cluster) {
				matched = append(matched, ws)
			}
		}
		if len(matched) > 0 {
			hits = append(hits, hit{org: o, workspaces: matched})
		}
	}

	switch {
	case len(hits) == 1:
		ws := hits[0].workspaces[0]
		if target.workspace != "" {
			if ws, err = matchWorkspace(hits[0].workspaces, target.workspace); err != nil {
				return err
			}
		}
		if ws.ClusterName == "" {
			return fmt.Errorf("workspace %q is not ready yet (no cluster assigned); try again shortly", displayLabel(ws.DisplayName, ws.UUID))
		}
		s.Org, s.WS, s.Cluster = hits[0].org, ws, ws.ClusterName
		return nil
	case len(hits) > 1:
		var ids []string
		for _, h := range hits {
			for _, ws := range h.workspaces {
				ids = append(ids, h.org.UUID+"/"+ws.UUID)
			}
		}
		return fmt.Errorf("workspace %q matches in %d organizations; pass --org (org/workspace: %s)", target.workspace, len(hits), strings.Join(ids, ", "))
	case target.workspace != "":
		return fmt.Errorf("no workspace matches %q", target.workspace)
	case lastErr != nil:
		return fmt.Errorf("cluster %s is not a workspace of any org you can list (last error: %w)", s.Cluster, lastErr)
	default:
		return fmt.Errorf("cluster %s (kubeconfig context %q) is not a workspace of any org you belong to; run 'faros use'", s.Cluster, s.Context)
	}
}

// workspaceMatches reports whether ws is selected by the --workspace query, or
// (with no query) whether it is the kubeconfig's cluster.
func workspaceMatches(ws workspaceView, query, cluster string) bool {
	if query == "" {
		return ws.ClusterName != "" && ws.ClusterName == cluster
	}
	return ws.UUID == query || (ws.DisplayName != "" && strings.EqualFold(ws.DisplayName, query))
}

// appStudioURL is the App Studio provider's REST base (routes are mounted
// under /api/projects).
func (s *hubSession) appStudioURL() string {
	return s.Hub + "/services/providers/app-studio"
}

// bearerToken returns the raw bearer the kubeconfig credentials produce. For
// exec-plugin (OIDC) logins rest.Config.BearerToken is empty, so the header is
// captured from the client-go transport stack instead.
func (s *hubSession) bearerToken(ctx context.Context) (string, error) {
	header, _, err := wsAuthFromRest(ctx, s.restConfig, s.Hub)
	if err != nil {
		return "", fmt.Errorf("resolving credentials: %w", err)
	}
	auth := header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("the kubeconfig credentials for context %q do not produce a bearer token; run 'faros login'", s.Context)
	}
	return strings.TrimSpace(token), nil
}

// newRequest builds a request carrying the tenant headers. in is JSON-encoded
// when it is not nil; an io.Reader is sent as is.
func (s *hubSession) newRequest(ctx context.Context, method, url string, in any) (*http.Request, error) {
	var body io.Reader
	switch v := in.(type) {
	case nil:
	case io.Reader:
		body = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", cliUserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.Org.UUID != "" {
		req.Header.Set("X-Faros-Org", s.Org.UUID)
	}
	if s.WS.UUID != "" {
		req.Header.Set("X-Faros-Workspace", s.WS.UUID)
	}
	return req, nil
}

// do performs a bounded REST call and decodes a JSON reply into out (a
// *json.RawMessage receives the body verbatim). Non-2xx answers become
// readable errors.
func (s *hubSession) do(ctx context.Context, method, url string, in, out any) error {
	return s.doWithHeaders(ctx, method, url, in, nil, out)
}

func (s *hubSession) doWithHeaders(ctx context.Context, method, url string, in any, header http.Header, out any) error {
	ctx, cancel := context.WithTimeout(ctx, hubRESTTimeout)
	defer cancel()
	req, err := s.newRequest(ctx, method, url, in)
	if err != nil {
		return err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeAPIError(method, req.URL.Path, resp.StatusCode, body)
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], body...)
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", req.URL.Path, err)
	}
	return nil
}

// stream performs an unbounded call (logs) and returns the open response. The
// caller closes the body.
func (s *hubSession) stream(ctx context.Context, method, url string) (*http.Response, error) {
	req, err := s.newRequest(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close() //nolint:errcheck
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, decodeAPIError(method, req.URL.Path, resp.StatusCode, body)
	}
	return resp, nil
}

// kubeStatus is the Kubernetes Status body the hub and providers answer
// failures with.
type kubeStatus struct {
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

// decodeAPIError turns a failed REST answer into a one-line error: the Status
// message when the body is a Kubernetes Status (or {"error": …}), otherwise
// the trimmed text body.
func decodeAPIError(method, path string, code int, body []byte) error {
	msg, reason := "", ""
	var st kubeStatus
	if json.Unmarshal(body, &st) == nil && st.Message != "" {
		msg, reason = st.Message, st.Reason
	} else {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &e) == nil && (e.Error != "" || e.Message != "") {
			msg = strings.TrimSpace(e.Error + " " + e.Message)
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 2048 {
			msg = msg[:2048] + "…"
		}
	}
	if msg == "" {
		msg = http.StatusText(code)
	}
	var err error = &hubAPIError{Method: method, Path: path, Code: code, Reason: reason, Message: msg}
	if code == http.StatusUnauthorized {
		return withLoginHint(err)
	}
	return err
}

// hubAPIError is a failed REST answer; callers that handle a status code
// (e.g. 409 on create) read it with errors.As. Message is the server's own
// message (the Status message, or the body).
type hubAPIError struct {
	Method  string
	Path    string
	Code    int
	Reason  string
	Message string
}

func (e *hubAPIError) Error() string {
	msg := e.Message
	if e.Reason != "" && !strings.Contains(msg, e.Reason) {
		msg = e.Reason + ": " + msg
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Code, msg)
}

func withLoginHint(err error) error {
	if err == nil || !strings.Contains(err.Error(), "401") {
		return err
	}
	return fmt.Errorf("%w (token missing or expired; run 'faros login')", err)
}

// mcpConnectInfo mirrors the hub's GET …/mcpservers/{name}/connect reply: the
// aggregate MCP endpoint and a long-lived, workspace-scoped token for it.
type mcpConnectInfo struct {
	EndpointURL string `json:"endpointURL"`
	ServerName  string `json:"serverName"`
	Token       string `json:"token"`
	TokenReady  bool   `json:"tokenReady"`
}

func (s *hubSession) mcpConnect(ctx context.Context, server string) (*mcpConnectInfo, error) {
	if server == "" {
		server = defaultMCPServerName
	}
	var info mcpConnectInfo
	url := fmt.Sprintf("%s/api/orgs/%s/workspaces/%s/mcpservers/%s/connect", s.Hub, s.Org.UUID, s.WS.UUID, server)
	if err := s.do(ctx, http.MethodGet, url, nil, &info); err != nil {
		return nil, fmt.Errorf("fetching MCP connection: %w", err)
	}
	if info.EndpointURL == "" {
		return nil, fmt.Errorf("fetching MCP connection: hub returned no endpoint URL")
	}
	return &info, nil
}

// mcpClient calls tools on the workspace's aggregate MCP endpoint.
type mcpClient struct {
	endpoint string
	token    string
	http     *http.Client
}

func (s *hubSession) newMCPClient(ctx context.Context) (*mcpClient, error) {
	info, err := s.mcpConnect(ctx, defaultMCPServerName)
	if err != nil {
		return nil, err
	}
	if info.Token == "" {
		return nil, fmt.Errorf("the workspace MCP token is not ready yet; retry shortly")
	}
	return &mcpClient{endpoint: info.EndpointURL, token: info.Token, http: s.plain}, nil
}

// callTool issues a JSON-RPC tools/call and returns the tool's result JSON.
// The aggregate answers statelessly, so no initialize handshake is needed.
func (c *mcpClient) callTool(ctx context.Context, name string, args any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding %s arguments: %w", name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", cliUserAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", name, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s reply: %w", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeAPIError(http.MethodPost, req.URL.Path, resp.StatusCode, reply)
	}
	out, err := parseMCPToolResult(reply)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

type jsonRPCReply struct {
	ID     json.RawMessage `json:"id"`
	Result *struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

// parseMCPToolResult extracts a tool result from a tools/call reply, which is
// either a JSON body or an SSE stream whose data events carry JSON-RPC
// messages. A failing tool is still HTTP 200 with result.isError, so that is
// turned into an error carrying the tool's text. The result is the
// structuredContent when present, else content[0].text (verbatim when it is
// JSON, JSON-quoted otherwise).
func parseMCPToolResult(body []byte) (json.RawMessage, error) {
	var msg *jsonRPCReply
	for _, payload := range jsonRPCPayloads(body) {
		var r jsonRPCReply
		if err := json.Unmarshal(payload, &r); err != nil {
			continue
		}
		if r.Result != nil || r.Error != nil {
			msg = &r
		}
	}
	if msg == nil {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 512 {
			snippet = snippet[:512] + "…"
		}
		return nil, fmt.Errorf("no JSON-RPC result in MCP reply: %q", snippet)
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", msg.Error.Code, msg.Error.Message)
	}
	var texts []string
	for _, c := range msg.Result.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	if msg.Result.IsError {
		if len(texts) == 0 {
			return nil, fmt.Errorf("tool reported an error")
		}
		return nil, fmt.Errorf("%s", strings.Join(texts, "\n"))
	}
	if sc := bytes.TrimSpace(msg.Result.StructuredContent); len(sc) > 0 && !bytes.Equal(sc, []byte("null")) {
		return json.RawMessage(sc), nil
	}
	if len(texts) == 0 {
		return json.RawMessage("null"), nil
	}
	if json.Valid([]byte(texts[0])) {
		return json.RawMessage(texts[0]), nil
	}
	quoted, _ := json.Marshal(texts[0])
	return json.RawMessage(quoted), nil
}

// jsonRPCPayloads returns the JSON documents in body: each SSE event's joined
// data lines, or the whole body when it is not an event stream.
func jsonRPCPayloads(body []byte) [][]byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return [][]byte{trimmed}
	}
	var out [][]byte
	var data []string
	flush := func() {
		if len(data) > 0 {
			out = append(out, []byte(strings.Join(data, "\n")))
			data = nil
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return out
}

// printJSON pretty-prints raw JSON (or any value) to w.
func printJSON(w io.Writer, v any) error {
	var b []byte
	var err error
	if raw, ok := v.(json.RawMessage); ok {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			_, werr := fmt.Fprintln(w, strings.TrimSpace(string(raw)))
			return werr
		}
		b = buf.Bytes()
	} else if b, err = json.MarshalIndent(v, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// validateOutputFormat accepts the -o values the new commands understand.
func validateOutputFormat(o string) error {
	switch o {
	case "", "json":
		return nil
	default:
		return fmt.Errorf("unsupported output format %q (want json)", o)
	}
}
