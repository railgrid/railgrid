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

package claude

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// The permission round-trip, Claude Code side.
//
// Headless Claude Code will ask a HOST before denying a tool call its
// permission mode does not pre-approve, and the host it will ask is an MCP tool
// named with --permission-prompt-tool. The contract below was confirmed against
// the 2.1.281 binary by running it, not inferred:
//
//	--permission-prompts host          who answers; "none" (the old behaviour)
//	                                   denies instead of asking
//	--permission-prompt-tool <name>    must be an MCP tool, named
//	                                   mcp__<server>__<tool> where <server> is
//	                                   the --mcp-config mcpServers key
//
// The tool is called with a flat object — tool_name (string), input (the
// harness tool's own arguments, as an object) and tool_use_id (string) — and
// must answer with ONE content block of type "text" whose text is a JSON
// document:
//
//	{"behavior":"allow"}                     run it, with the input as proposed
//	{"behavior":"allow","updatedInput":{…}}  run it with different arguments
//	{"behavior":"deny","message":"…"}        do not run it; the message is what
//	                                         the model is told
//
// A denial is NOT a failed turn: the harness receives the message as the tool's
// error result and carries on, which is the whole point — a person who says no
// gets an explanation rather than a dead run. Measured: the call is not
// abandoned while it waits (a 400-second answer was still accepted and acted
// on), so a park that lasts as long as a person takes is safe.
//
// This adapter never sends updatedInput. A human said yes to the call they were
// shown, and rewriting its arguments on the way through would make the approval
// a lie.
//
// # What it is reachable by
//
// The server listens on 127.0.0.1 with a port the kernel picks, at a
// random path, and every request must carry a per-launch secret header. All
// three are minted for ONE launch and die with it. The secret reaches the child
// through a 0600 config file rather than argv, because argv is world-readable
// on every platform this runs on.
//
// # What it carries
//
// A tool name and a bounded rendering of that tool's input. No credential, no
// session token, no caller identity, no workspace path — nothing that is not
// the call being asked about. The server exposes exactly one tool and answers
// nothing else.
//
// # The --safe-mode trade
//
// Measured against 2.1.281: --safe-mode turns off MCP servers, INCLUDING ones
// passed with --mcp-config ("Available MCP tools: none"), so a permission
// prompt tool and --safe-mode cannot both exist. A launch that can ask
// therefore drops --safe-mode and keeps the narrower flags that cover most of
// what it did; see the comment on Adapter.args for exactly what is and is not
// still enforced.
const (
	// permissionServerName is the --mcp-config key, and permissionToolName the
	// tool on it. Together they are the --permission-prompt-tool argument.
	permissionServerName = "railgrid"
	permissionToolName   = "permission_prompt"
	// permissionToolRef is what --permission-prompt-tool is given. Claude Code
	// refuses anything that is not mcp__<server>__<tool>.
	permissionToolRef = "mcp__" + permissionServerName + "__" + permissionToolName

	// mcpProtocolVersion is answered when the child proposes nothing we know.
	mcpProtocolVersion = "2025-06-18"

	// permissionSecretHeader carries the per-launch secret.
	permissionSecretHeader = "X-Railgrid-Permission" //nolint:gosec // header name, not a credential

	maxPermissionBody = 1 << 20
)

// permissionServer is the one-tool MCP server a launch hosts for its own child.
type permissionServer struct {
	asker harness.PermissionAsker
	// scope makes a request id stable for one attempt without carrying the
	// attempt id anywhere a human can see it.
	scope string

	listener net.Listener
	server   *http.Server
	secret   string
	url      string

	configPath string

	// seq disambiguates two calls the harness did not give distinct ids to.
	mu  sync.Mutex
	seq uint64
}

// startPermissionServer brings up the loopback MCP server and writes the
// --mcp-config file the child is pointed at. The returned server must be closed
// by the caller, which also removes the config file.
func startPermissionServer(asker harness.PermissionAsker, scope, home string) (*permissionServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("opening the permission prompt listener: %w", err)
	}
	secret, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	path, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	p := &permissionServer{
		asker:    asker,
		scope:    scope,
		listener: listener,
		secret:   secret,
		url:      "http://" + listener.Addr().String() + "/" + path,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+path, p.handle)
	p.server = &http.Server{
		Handler: mux,
		// A header deadline is a denial-of-service bound, not a call bound.
		// There is deliberately no WriteTimeout: a permission call is answered
		// by a person and may take hours, and a write deadline would hang up on
		// them.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = p.server.Serve(listener) }()
	if err := p.writeConfig(home); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

// writeConfig writes the --mcp-config document. It goes in a 0600 file in the
// runner-owned home rather than on the command line, because the secret in it
// would otherwise be readable by every process on the machine.
func (p *permissionServer) writeConfig(home string) error {
	document := map[string]any{
		"mcpServers": map[string]any{
			permissionServerName: map[string]any{
				"type":    "http",
				"url":     p.url,
				"headers": map[string]string{permissionSecretHeader: p.secret},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encoding the permission prompt configuration: %w", err)
	}
	file, err := os.CreateTemp(home, "permission-prompt-*.json")
	if err != nil {
		return fmt.Errorf("creating the permission prompt configuration: %w", err)
	}
	p.configPath = file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protecting the permission prompt configuration: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing the permission prompt configuration: %w", err)
	}
	return file.Close()
}

// ConfigPath is the --mcp-config argument for this launch.
func (p *permissionServer) ConfigPath() string { return p.configPath }

// Close stops serving and removes the configuration file. Requests still in
// flight are ended, which is correct: the launch is over, so nobody is left to
// answer them.
func (p *permissionServer) Close() error {
	err := p.server.Close()
	if p.configPath != "" {
		_ = os.Remove(p.configPath)
	}
	return err
}

// ---- MCP over loopback HTTP --------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handle answers one JSON-RPC message. Anything that is not our child speaking
// our protocol is refused without a hint about what it got wrong.
func (p *permissionServer) handle(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(permissionSecretHeader)), []byte(p.secret)) != 1 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPermissionBody)).Decode(&request); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// A notification has no id and takes no answer.
	if len(request.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = p.initializeResult(request.Params)
	case "tools/list":
		response.Result = map[string]any{"tools": []any{permissionToolDescriptor()}}
	case "tools/call":
		result, err := p.call(r.Context(), request.Params)
		if err != nil {
			response.Error = &rpcError{Code: -32603, Message: err.Error()}
		} else {
			response.Result = result
		}
	case "ping":
		response.Result = map[string]any{}
	default:
		response.Error = &rpcError{Code: -32601, Message: "method not found"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (p *permissionServer) initializeResult(params json.RawMessage) map[string]any {
	version := mcpProtocolVersion
	var decoded struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &decoded) == nil && decoded.ProtocolVersion != "" {
		version = decoded.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": permissionServerName, "version": "1"},
	}
}

// permissionToolDescriptor is the ONLY tool this server has. The schema matches
// what Claude Code sends, which is a flat tool_name/input/tool_use_id object.
func permissionToolDescriptor() map[string]any {
	return map[string]any{
		"name":        permissionToolName,
		"description": "Ask the person who owns this machine whether a tool call may proceed.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_name":   map[string]any{"type": "string"},
				"input":       map[string]any{"type": "object"},
				"tool_use_id": map[string]any{"type": "string"},
			},
			"required": []string{"tool_name", "input"},
		},
	}
}

type toolCallParams struct {
	Name      string `json:"name"`
	Arguments struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	} `json:"arguments"`
}

// call turns one permission prompt into a question for a human and the human's
// verdict back into the answer Claude Code expects.
func (p *permissionServer) call(ctx context.Context, params json.RawMessage) (map[string]any, error) {
	var decoded toolCallParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, errors.New("malformed tool call")
	}
	if decoded.Name != permissionToolName {
		return nil, errors.New("unknown tool")
	}
	tool := strings.TrimSpace(decoded.Arguments.ToolName)
	if tool == "" {
		return nil, errors.New("permission prompt named no tool")
	}
	request := harness.PermissionRequest{
		ID:    p.requestID(decoded.Arguments.ToolUseID, tool, decoded.Arguments.Input),
		Tool:  tool,
		Input: boundedPermissionInput(decoded.Arguments.Input),
	}
	verdict, err := p.asker.AskPermission(ctx, request)
	if err != nil {
		// Nobody could be asked. Deny rather than erroring: an error here would
		// end the turn, and the harness can do something useful with a refusal.
		verdict = harness.PermissionVerdict{Allow: false, Message: "This machine could not reach anyone to approve the call."}
	}
	return permissionResult(verdict), nil
}

// permissionResult renders a verdict in the shape Claude Code validates
// against: one text block whose text is the JSON permission result.
func permissionResult(verdict harness.PermissionVerdict) map[string]any {
	decision := map[string]any{"behavior": "deny", "message": verdict.Message}
	if verdict.Allow {
		// No updatedInput: the call runs exactly as it was shown to the person
		// who approved it.
		decision = map[string]any{"behavior": "allow"}
	} else if strings.TrimSpace(verdict.Message) == "" {
		decision["message"] = "A person declined this tool call."
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		encoded = []byte(`{"behavior":"deny","message":"the verdict could not be encoded"}`)
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(encoded)}}}
}

// requestID is stable for one attempt and one tool call, so a replayed resume
// is recognized as answering the same request rather than a new one. The
// harness's own tool_use_id is used when it sent one; a sequence number stands
// in when it did not, because two identical calls in one turn are still two
// questions.
func (p *permissionServer) requestID(toolUseID, tool string, input json.RawMessage) string {
	key := strings.TrimSpace(toolUseID)
	if key == "" {
		p.mu.Lock()
		p.seq++
		key = fmt.Sprintf("%s\x00%s\x00%d", tool, string(input), p.seq)
		p.mu.Unlock()
	}
	digest := sha256.Sum256([]byte(p.scope + "\x00" + key))
	return "permission-" + hex.EncodeToString(digest[:])
}

// boundedPermissionInput renders the tool's arguments for a human. It is capped
// here as well as in the runner because this is where untrusted bytes enter:
// a tool input routinely holds a whole file.
func boundedPermissionInput(input json.RawMessage) string {
	text := strings.TrimSpace(string(input))
	if text == "" || text == "null" {
		return ""
	}
	if len(text) > harness.MaxPermissionInputBytes {
		// Cut on a rune boundary; the result is a rendering for a person, not
		// JSON to be parsed again.
		cut := harness.MaxPermissionInputBytes
		for cut > 0 && !utf8RuneStart(text[cut]) {
			cut--
		}
		return text[:cut] + "…[truncated]"
	}
	return text
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting a permission prompt secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
