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

// Package codex adapts the Codex app-server protocol to the runner harness.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

const (
	defaultBinary          = "codex"
	defaultExpectedVersion = "0.147.0"
	maxEventData           = 64 << 10
	maxWireLine            = 2 << 20
	processStopGrace       = 750 * time.Millisecond
	processStopTimeout     = 2 * time.Second
	interruptTimeout       = 1500 * time.Millisecond
)

// Codex creates skills and plugin cache directories in its own home. Plugin
// caches are permitted below, but extensions are explicitly disabled for every
// app-server process. Interactive config, MCP, and hook entries remain rejected.
var unsafeHomeEntries = map[string]struct{}{ //nolint:gochecknoglobals // immutable worker-home policy
	".codex":      {},
	".mcp.json":   {},
	".mcp.toml":   {},
	"config":      {},
	"config.json": {},
	"config.toml": {},
	"hooks":       {},
	"mcp":         {},
	"mcp.json":    {},
	"mcp.toml":    {},
	"plugins":     {},
	"prompts":     {},
}

var versionPattern = regexp.MustCompile(`(?:codex-cli\s+)?v?([0-9]+\.[0-9]+\.[0-9]+)`) //nolint:gochecknoglobals

// Config selects the Codex executable and its isolated worker home.
type Config struct {
	Binary string
	// Home is a runner-owned CODEX_HOME. It must be separate from an
	// interactive user's Codex home and independently signed in by the worker.
	Home string
	// WorktreeRoot permits trust-only configuration for managed task checkouts.
	WorktreeRoot    string
	Model           string
	ExpectedVersion string
}

// Adapter is the Codex implementation of harness.Adapter.
type Adapter struct {
	cfg Config
}

var _ harness.Adapter = (*Adapter)(nil)

// New constructs a Codex harness adapter. The returned adapter starts no
// process until Probe or Run is called.
func New(cfg Config) harness.Adapter {
	if strings.TrimSpace(cfg.Binary) == "" {
		cfg.Binary = defaultBinary
	}
	if strings.TrimSpace(cfg.ExpectedVersion) == "" {
		cfg.ExpectedVersion = defaultExpectedVersion
	}
	return &Adapter{cfg: cfg}
}

// Probe checks the binary version and app-server authentication state without
// starting a model thread or turn.
func (a *Adapter) Probe(ctx context.Context) (harness.Info, error) {
	info := harness.Info{Name: "codex"}
	if err := a.ensureHome(); err != nil {
		return info, err
	}
	version, err := a.probeVersion(ctx)
	if err != nil {
		return info, err
	}
	info.Version = version
	if a.cfg.ExpectedVersion != "" && version != a.cfg.ExpectedVersion {
		info.Reasons = append(info.Reasons, fmt.Sprintf("expected Codex %s, found %s", a.cfg.ExpectedVersion, version))
	}

	proc, err := a.startServer(ctx, "")
	if err != nil {
		return info, err
	}
	defer func() { _ = proc.stop() }()
	conn := proc.conn

	handler := func(msg wireMessage) error {
		if isInteractiveRequest(msg.Method) || isAuthNotification(msg.Method) {
			return &needsInputError{method: msg.Method, data: boundedData(msg.Params), message: "Codex requires user input"}
		}
		return nil
	}
	if _, err := conn.call(ctx, "initialize", initializeParams(), handler); err != nil {
		if needsInput(err) || isAuthRPCError(err) {
			info.Reasons = append(info.Reasons, "Codex authentication requires user input")
			return infoWithReady(info), nil
		}
		return info, err
	}
	if err := conn.notify("initialized", map[string]any{}); err != nil {
		return info, err
	}
	account, err := conn.call(ctx, "account/read", map[string]any{"refreshToken": false}, handler)
	if err != nil {
		if needsInput(err) || isAuthRPCError(err) {
			info.Reasons = append(info.Reasons, "Codex authentication requires user input")
			return infoWithReady(info), nil
		}
		return info, err
	}
	var accountResult struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth json.RawMessage `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(account.Result, &accountResult); err != nil {
		return info, fmt.Errorf("decoding Codex account/read response: %w", err)
	}
	requiresOpenAIAuth, validAuthRequirement := parseAuthRequirement(accountResult.RequiresOpenAIAuth)
	accountType, validAccount := parseAccountType(accountResult.Account)
	if !validAuthRequirement || !validAccount {
		info.Reasons = append(info.Reasons, "Codex authentication state is unavailable")
	} else if requiresOpenAIAuth && !isAuthenticatedAccountType(accountType) {
		info.Reasons = append(info.Reasons, "Codex authentication is not configured")
	}
	return infoWithReady(info), nil
}

func parseAuthRequirement(raw json.RawMessage) (bool, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false, false
	}

	var required bool
	if err := json.Unmarshal(raw, &required); err != nil {
		return false, false
	}
	return required, true
}

func parseAccountType(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}

	var account map[string]json.RawMessage
	if err := json.Unmarshal(raw, &account); err != nil || account == nil {
		return "", false
	}
	typeRaw, ok := account["type"]
	if !ok {
		return "", false
	}
	var accountType string
	if err := json.Unmarshal(typeRaw, &accountType); err != nil || accountType == "" {
		return "", false
	}
	return accountType, true
}

func isAuthenticatedAccountType(accountType string) bool {
	switch accountType {
	case "apiKey", "chatgpt":
		return true
	default:
		return false
	}
}

func infoWithReady(info harness.Info) harness.Info {
	info.Ready = len(info.Reasons) == 0
	return info
}

func (a *Adapter) probeVersion(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, a.cfg.Binary, "--version")
	cmd.Env = safeEnv(a.cfg.Home)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running Codex version probe: %w: %s", err, trimOutput(output))
	}
	match := versionPattern.FindSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("parsing Codex version from %q", trimOutput(output))
	}
	return string(match[1]), nil
}

// Run starts one dedicated app-server process, creates or resumes exactly one
// thread, starts one turn, forwards events, and stops the process before it
// returns. A resumed session is never silently replaced with a new thread.
func (a *Adapter) Run(ctx context.Context, launch harness.Launch, emit harness.Emit) (result harness.Result, err error) {
	if err := a.ensureHome(); err != nil {
		return result, err
	}
	if err := validateLaunch(launch); err != nil {
		return result, err
	}
	if err := a.validateLaunchConfiguration(launch.Workdir); err != nil {
		return result, err
	}
	proc, err := a.startServer(ctx, launch.Workdir)
	if err != nil {
		return result, err
	}
	defer func() {
		stopErr := proc.stop()
		if stopErr != nil {
			if err == nil {
				err = stopErr
			} else {
				err = errors.Join(err, stopErr)
			}
		}
		if ctx.Err() != nil && err == nil {
			result.Phase = "cancelled"
		}
	}()

	state := &runState{emit: emit, sessionID: launch.SessionID}
	conn := proc.conn
	if _, callErr := conn.call(ctx, "initialize", initializeParams(), state.handle); callErr != nil {
		return resultForError(callErr, state), normalizeContextError(ctx, callErr)
	}
	if callErr := conn.notify("initialized", map[string]any{}); callErr != nil {
		return result, callErr
	}

	threadMethod := "thread/start"
	threadParams := a.threadStartParams(launch)
	if launch.SessionID != "" {
		threadMethod = "thread/resume"
		threadParams = a.threadResumeParams(launch)
	}
	threadResponse, callErr := conn.call(ctx, threadMethod, threadParams, state.handle)
	if callErr != nil {
		if needsInput(callErr) {
			return needsInputResult(state, callErr), nil
		}
		if isAuthRPCError(callErr) {
			return authResult(state, callErr, emit), nil
		}
		if launch.SessionID != "" && isMissingThreadError(callErr) {
			return missingSessionResult(state, callErr, emit), nil
		}
		return resultForError(callErr, state), normalizeContextError(ctx, callErr)
	}
	responseSessionID := threadID(threadResponse.Result)
	if responseSessionID == "" {
		return resultForError(errors.New("codex thread response did not include an id"), state), nil
	}
	if state.sessionID != "" && state.sessionID != responseSessionID {
		return resultForError(fmt.Errorf("codex thread response referenced foreign session %q", responseSessionID), state), nil
	}
	state.sessionID = responseSessionID
	if err := state.emitEvent(harness.Event{
		Type:      "session",
		SessionID: state.sessionID,
		Message:   "Codex session ready",
		Data:      boundedData(threadResponse.Result),
	}); err != nil {
		return result, err
	}

	turnParams := a.turnStartParams(launch, state.sessionID)
	turnResponse, callErr := conn.call(ctx, "turn/start", turnParams, state.handle)
	if callErr != nil {
		if needsInput(callErr) {
			return needsInputResult(state, callErr), nil
		}
		if isAuthRPCError(callErr) {
			return authResult(state, callErr, emit), nil
		}
		return resultForError(callErr, state), normalizeContextError(ctx, callErr)
	}
	if state.turnID == "" {
		state.turnID = turnID(turnResponse.Result)
	}
	if state.done {
		return state.result(), nil
	}

	for {
		select {
		case <-ctx.Done():
			if state.turnID != "" {
				interruptCtx, cancel := context.WithTimeout(context.Background(), interruptTimeout)
				_, _ = conn.call(interruptCtx, "turn/interrupt", map[string]any{
					"threadId": state.sessionID,
					"turnId":   state.turnID,
				}, state.handle)
				cancel()
			}
			result = harness.Result{Phase: "cancelled", SessionID: state.sessionID}
			return result, nil
		case msg := <-conn.messages:
			if handleErr := state.handle(msg); handleErr != nil {
				if needsInput(handleErr) {
					return needsInputResult(state, handleErr), nil
				}
				return resultForError(handleErr, state), nil
			}
			if state.done {
				return state.result(), nil
			}
		case readErr := <-conn.errors:
			if readErr == nil {
				readErr = io.EOF
			}
			return resultForError(fmt.Errorf("reading Codex app-server: %w", readErr), state), normalizeContextError(ctx, readErr)
		}
	}
}

func (a *Adapter) ensureHome() error {
	home := strings.TrimSpace(a.cfg.Home)
	if home == "" {
		return errors.New("codex runner home is required and must be dedicated to the worker")
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("codex runner home must be absolute: %q", home)
	}
	if info, err := os.Lstat(home); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("codex runner home must not be a symlink: %q", home)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect codex runner home: %w", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("creating codex runner home: %w", err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fmt.Errorf("protecting codex runner home: %w", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return fmt.Errorf("reading codex runner home: %w", err)
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if entry.Name() == "plugins" && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if name == codexConfigName && entry.Name() == codexConfigName {
			if _, err := validateCodexConfig(home, a.cfg.WorktreeRoot); err != nil {
				return err
			}
			continue
		}
		if _, unsafe := unsafeHomeEntries[name]; unsafe {
			return fmt.Errorf("codex runner home contains interactive configuration %q; use a dedicated worker home", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("codex runner home contains symlink %q; use a dedicated worker home", entry.Name())
		}
	}
	return nil
}

func validateLaunch(launch harness.Launch) error {
	if strings.TrimSpace(launch.Workdir) == "" {
		return errors.New("codex launch workdir is required")
	}
	if strings.TrimSpace(launch.Instructions) == "" {
		return errors.New("codex launch instructions are required")
	}
	return nil
}

func normalizeContextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (a *Adapter) threadStartParams(launch harness.Launch) map[string]any {
	params := map[string]any{
		"approvalPolicy": "on-request",
		"cwd":            launch.Workdir,
		"sandbox":        "workspace-write",
	}
	if model := a.model(launch); model != "" {
		params["model"] = model
	}
	return params
}

func (a *Adapter) threadResumeParams(launch harness.Launch) map[string]any {
	params := map[string]any{
		"approvalPolicy": "on-request",
		"cwd":            launch.Workdir,
		"sandbox":        "workspace-write",
		"threadId":       launch.SessionID,
	}
	if model := a.model(launch); model != "" {
		params["model"] = model
	}
	return params
}

func (a *Adapter) turnStartParams(launch harness.Launch, sessionID string) map[string]any {
	params := map[string]any{
		"approvalPolicy": "on-request",
		"cwd":            launch.Workdir,
		"input": []map[string]string{{
			"type": "text",
			"text": launch.Instructions,
		}},
		"sandboxPolicy": map[string]any{
			"networkAccess": false,
			"type":          "workspaceWrite",
			"writableRoots": []string{launch.Workdir},
		},
		"threadId": sessionID,
	}
	if model := a.model(launch); model != "" {
		params["model"] = model
	}
	return params
}

func (a *Adapter) model(launch harness.Launch) string {
	if strings.TrimSpace(launch.Model) != "" {
		return launch.Model
	}
	return a.cfg.Model
}

func initializeParams() map[string]any {
	return map[string]any{
		"clientInfo": map[string]string{
			"name":    "faros-runner",
			"title":   "Faros Runner",
			"version": "0.1.0",
		},
	}
}

func safeEnv(home string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if blockedEnvKey(upper) {
			continue
		}
		env = append(env, item)
	}
	if strings.TrimSpace(home) != "" {
		cacheHome := filepath.Join(home, "cache")
		env = append(env, "HOME="+home)
		env = append(env, "CODEX_HOME="+home)
		env = append(env, "XDG_CONFIG_HOME="+home)
		env = append(env, "XDG_DATA_HOME="+home)
		env = append(env, "XDG_CACHE_HOME="+cacheHome)
		env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull)
		env = append(env, "GIT_CONFIG_SYSTEM="+os.DevNull)
		env = append(env, "GIT_CONFIG_NOSYSTEM=1")
	}
	return env
}

func blockedEnvKey(key string) bool {
	if key == "HOME" || key == "CODEX_HOME" || strings.HasPrefix(key, "XDG_") {
		return true
	}
	if key == "GH_TOKEN" || strings.HasPrefix(key, "GH_") || key == "GITHUB_TOKEN" || strings.HasPrefix(key, "GITHUB_") {
		return true
	}
	if key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_NOSYSTEM" || strings.HasPrefix(key, "GIT_CONFIG_") {
		return true
	}
	if key == "GIT_SSH" || key == "GIT_SSH_COMMAND" || key == "GIT_ASKPASS" || key == "SSH_AUTH_SOCK" || key == "SSH_AGENT_PID" {
		return true
	}
	return key == "OPENAI_API_KEY" || key == "CODEX_API_KEY" || key == "CHATGPT_API_KEY"
}

type serverProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	conn    *rpcConn
	done    chan error
	stopMu  sync.Mutex
	stopped bool
}

func (a *Adapter) startServer(ctx context.Context, workdir string) (*serverProcess, error) {
	// Keep extension execution policy independent of whether Codex has created
	// its cache directories. Apply it to probes, new sessions, and resumed ones.
	cmd := exec.Command(a.cfg.Binary, "app-server", "--listen", "stdio://",
		"--disable", "apps", "--disable", "plugins", "--disable", "hooks")
	cmd.Env = safeEnv(a.cfg.Home)
	if workdir != "" {
		cmd.Dir = workdir
	}
	configureProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening codex app-server stdout: %w", err)
	}
	cmd.Stderr = &limitedBuffer{limit: 32 << 10}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening codex app-server stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("starting codex app-server: %w", err)
	}
	proc := &serverProcess{cmd: cmd, stdin: stdin, done: make(chan error, 1)}
	proc.conn = newRPCConn(stdin, stdout)
	go func() { proc.done <- cmd.Wait() }()
	if ctx.Err() != nil {
		_ = proc.stop()
		return nil, ctx.Err()
	}
	return proc, nil
}

func (p *serverProcess) stop() error {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	if p.stopped {
		return nil
	}
	p.stopped = true
	closeErr := p.stdin.Close()
	select {
	case waitErr := <-p.done:
		return errors.Join(closeErr, waitErr)
	case <-time.After(processStopGrace):
	}
	killErr := killProcessGroup(p.cmd)
	select {
	case waitErr := <-p.done:
		// A process-group kill is expected to report an interrupted wait. The
		// cleanup itself succeeded when the group was signalled and the child
		// exited within the bounded wait below.
		if killErr == nil {
			return closeErr
		}
		return errors.Join(closeErr, killErr, waitErr)
	case <-time.After(processStopTimeout):
		return errors.Join(closeErr, killErr, errors.New("timed out stopping codex app-server process"))
	}
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		_, _ = b.buf.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

type wireMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

type rpcConn struct {
	stdin    io.Writer
	writeMu  sync.Mutex
	nextID   uint64
	messages chan wireMessage
	errors   chan error
}

func newRPCConn(stdin io.Writer, stdout io.Reader) *rpcConn {
	c := &rpcConn{
		stdin:    stdin,
		messages: make(chan wireMessage, 64),
		errors:   make(chan error, 1),
	}
	go c.read(stdout)
	return c
}

func (c *rpcConn) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxWireLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg wireMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.errors <- fmt.Errorf("decoding Codex app-server message: %w", err)
			return
		}
		c.messages <- msg
	}
	if err := scanner.Err(); err != nil {
		c.errors <- err
		return
	}
	c.errors <- io.EOF
}

func (c *rpcConn) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

func (c *rpcConn) call(ctx context.Context, method string, params any, handle func(wireMessage) error) (wireMessage, error) {
	c.nextID++
	id := c.nextID
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return wireMessage{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return wireMessage{}, ctx.Err()
		case msg := <-c.messages:
			if hasID(msg.ID) && string(bytes.TrimSpace(msg.ID)) == strconv.FormatUint(id, 10) {
				if msg.Error != nil {
					return msg, msg.Error
				}
				return msg, nil
			}
			if msg.Method != "" && handle != nil {
				if err := handle(msg); err != nil {
					return wireMessage{}, err
				}
			}
		case err := <-c.errors:
			if err == nil {
				err = io.EOF
			}
			return wireMessage{}, err
		}
	}
}

func (c *rpcConn) send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(data)
	return err
}

func hasID(id json.RawMessage) bool {
	id = bytes.TrimSpace(id)
	return len(id) > 0 && !bytes.Equal(id, []byte("null"))
}

type needsInputError struct {
	method  string
	data    json.RawMessage
	message string
}

func (e *needsInputError) Error() string {
	if e.message != "" {
		return e.message
	}
	return "codex requires user input"
}

func needsInput(err error) bool {
	var target *needsInputError
	return errors.As(err, &target)
}

type runState struct {
	emit      harness.Emit
	sessionID string
	turnID    string
	phase     string
	blocker   string
	done      bool
}

func (s *runState) handle(msg wireMessage) error {
	if msg.Method == "" {
		return nil
	}
	if isInteractiveRequest(msg.Method) {
		if err := s.observeSessionID(threadIDFromParams(msg.Params)); err != nil {
			return err
		}
		if err := s.emitEvent(harness.Event{
			Type:      msg.Method,
			SessionID: s.sessionID,
			TurnID:    turnIDFromParams(msg.Params, s.turnID),
			Message:   interactiveMessage(msg.Method, msg.Params),
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
		return &needsInputError{method: msg.Method, data: boundedData(msg.Params), message: "Codex requires user input"}
	}
	if isAuthNotification(msg.Method) {
		if err := s.observeSessionID(threadIDFromParams(msg.Params)); err != nil {
			return err
		}
		if err := s.emitEvent(harness.Event{
			Type:      "auth_failure",
			SessionID: s.sessionID,
			TurnID:    turnIDFromParams(msg.Params, s.turnID),
			Message:   "Codex authentication requires user input",
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
		return &needsInputError{method: msg.Method, data: boundedData(msg.Params), message: "Codex authentication requires user input"}
	}

	switch msg.Method {
	case "thread/started":
		if err := s.observeSessionID(threadID(msg.Params)); err != nil {
			return err
		}
	case "turn/started":
		if err := s.observeSessionID(threadIDFromParams(msg.Params)); err != nil {
			return err
		}
		if id := turnID(msg.Params); id != "" {
			s.turnID = id
		}
		if err := s.emitEvent(harness.Event{
			Type:      "turn_started",
			SessionID: s.sessionID,
			TurnID:    s.turnID,
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
	case "item/agentMessage/delta":
		var params struct {
			Delta    string `json:"delta"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return fmt.Errorf("decoding Codex message delta: %w", err)
		}
		if err := s.observeSessionID(params.ThreadID); err != nil {
			return err
		}
		if err := s.emitEvent(harness.Event{
			Type:      "message",
			SessionID: s.sessionID,
			TurnID:    firstNonEmpty(params.TurnID, s.turnID),
			Message:   params.Delta,
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
	case "error":
		if err := s.observeSessionID(threadIDFromParams(msg.Params)); err != nil {
			return err
		}
		if authData(msg.Params) {
			if err := s.emitEvent(harness.Event{
				Type:      "auth_failure",
				SessionID: s.sessionID,
				TurnID:    turnIDFromParams(msg.Params, s.turnID),
				Message:   "Codex authentication requires user input",
				Data:      boundedData(msg.Params),
			}); err != nil {
				return err
			}
			return &needsInputError{method: msg.Method, data: boundedData(msg.Params), message: "Codex authentication requires user input"}
		}
		if err := s.emitEvent(harness.Event{
			Type:      msg.Method,
			SessionID: s.sessionID,
			TurnID:    turnIDFromParams(msg.Params, s.turnID),
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
	case "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return fmt.Errorf("decoding Codex turn completion: %w", err)
		}
		if err := s.observeSessionID(params.ThreadID); err != nil {
			return err
		}
		s.done = true
		if params.Turn.ID != "" {
			s.turnID = params.Turn.ID
		}
		s.phase = "completed"
		if params.Turn.Status == "failed" || len(bytes.TrimSpace(params.Turn.Error)) > 0 && !bytes.Equal(bytes.TrimSpace(params.Turn.Error), []byte("null")) {
			s.phase = "failed"
			s.blocker = "Codex turn failed"
			if authData(params.Turn.Error) {
				s.phase = "needs_input"
				s.blocker = "Codex authentication requires user input"
			}
		} else if params.Turn.Status == "interrupted" {
			s.phase = "cancelled"
		}
		if err := s.emitEvent(harness.Event{
			Type:      "turn_completed",
			SessionID: s.sessionID,
			TurnID:    s.turnID,
			Message:   s.phase,
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
	default:
		if err := s.observeSessionID(threadIDFromParams(msg.Params)); err != nil {
			return err
		}
		if err := s.emitEvent(harness.Event{
			Type:      msg.Method,
			SessionID: s.sessionID,
			TurnID:    turnIDFromParams(msg.Params, s.turnID),
			Data:      boundedData(msg.Params),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *runState) emitEvent(event harness.Event) error {
	if s.emit == nil {
		return nil
	}
	return s.emit(event)
}

func (s *runState) observeSessionID(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if s.sessionID == "" {
		s.sessionID = sessionID
		return nil
	}
	if s.sessionID != sessionID {
		return fmt.Errorf("codex event referenced foreign session %q", sessionID)
	}
	return nil
}

func (s *runState) result() harness.Result {
	phase := s.phase
	if phase == "" {
		phase = "completed"
	}
	return harness.Result{Phase: phase, SessionID: s.sessionID, Blocker: s.blocker}
}

func resultForError(err error, state *runState) harness.Result {
	result := state.result()
	result.Phase = "failed"
	if result.Blocker == "" {
		result.Blocker = err.Error()
	}
	return result
}

func needsInputResult(state *runState, err error) harness.Result {
	result := state.result()
	result.Phase = "needs_input"
	var target *needsInputError
	if errors.As(err, &target) && target.message != "" {
		result.Blocker = target.message
	} else {
		result.Blocker = "Codex requires user input"
	}
	return result
}

func authResult(state *runState, err error, emit harness.Emit) harness.Result {
	data := json.RawMessage(nil)
	var target *rpcError
	if errors.As(err, &target) {
		data = boundedData(target.Data)
	}
	if emit != nil {
		_ = emit(harness.Event{
			Type:      "auth_failure",
			SessionID: state.sessionID,
			TurnID:    state.turnID,
			Message:   "Codex authentication requires user input",
			Data:      data,
		})
	}
	return harness.Result{Phase: "needs_input", SessionID: state.sessionID, Blocker: "Codex authentication requires user input"}
}

func missingSessionResult(state *runState, err error, emit harness.Emit) harness.Result {
	data := json.RawMessage(nil)
	var target *rpcError
	if errors.As(err, &target) {
		data = boundedData(target.Data)
	}
	if emit != nil {
		_ = emit(harness.Event{
			Type:      "session_error",
			SessionID: state.sessionID,
			Message:   "Codex session was not found",
			Data:      data,
		})
	}
	return harness.Result{Phase: "needs_input", SessionID: state.sessionID, Blocker: "Codex session was not found"}
}

func isInteractiveRequest(method string) bool {
	method = strings.ToLower(method)
	return strings.Contains(method, "requestapproval") || strings.Contains(method, "requestuserinput") || strings.Contains(method, "request_user_input") || strings.Contains(method, "approval")
}

func isAuthNotification(method string) bool {
	method = strings.ToLower(method)
	if strings.Contains(method, "login") || strings.Contains(method, "reauth") {
		return true
	}
	if strings.Contains(method, "auth") {
		return strings.Contains(method, "refresh") || strings.Contains(method, "recovery") || strings.Contains(method, "required") || strings.Contains(method, "failure") || strings.Contains(method, "error")
	}
	return false
}

func isAuthRPCError(err error) bool {
	var target *rpcError
	if !errors.As(err, &target) {
		return false
	}
	message := strings.ToLower(target.Message)
	return target.Code == 401 || target.Code == 403 || strings.Contains(message, "auth") || strings.Contains(message, "unauthorized") || strings.Contains(message, "login") || strings.Contains(message, "credential")
}

func isMissingThreadError(err error) bool {
	var target *rpcError
	if !errors.As(err, &target) {
		return false
	}
	message := strings.ToLower(target.Message)
	return strings.Contains(message, "thread") && (strings.Contains(message, "not found") || strings.Contains(message, "unknown") || strings.Contains(message, "missing") || strings.Contains(message, "invalid session"))
}

func authData(data json.RawMessage) bool {
	message := strings.ToLower(string(data))
	return strings.Contains(message, "auth") || strings.Contains(message, "unauthorized") || strings.Contains(message, "credential") || strings.Contains(message, "login")
}

func interactiveMessage(method string, params json.RawMessage) string {
	if len(params) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		return method
	}
	return method + " requires a user decision"
}

func boundedData(data json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if len(data) <= maxEventData {
		return append(json.RawMessage(nil), data...)
	}
	return json.RawMessage(fmt.Sprintf(`{"truncated":true,"bytes":%d}`, len(data)))
}

func threadID(data json.RawMessage) string {
	var value struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return ""
	}
	return value.Thread.ID
}

func threadIDFromParams(data json.RawMessage) string {
	var value struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return ""
	}
	return value.ThreadID
}

func turnID(data json.RawMessage) string {
	var value struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return ""
	}
	return value.Turn.ID
}

func turnIDFromParams(data json.RawMessage, fallback string) string {
	var value struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return fallback
	}
	return firstNonEmpty(value.TurnID, fallback)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func trimOutput(data []byte) string {
	return strings.TrimSpace(string(bytes.TrimSpace(data)))
}
