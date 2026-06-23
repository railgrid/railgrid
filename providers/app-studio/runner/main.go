/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultControlAddr = ":7070"

type runnerConfig struct {
	WorkDir              string
	StartCommand         string
	Port                 string
	ControlToken         string
	AllowInsecureControl bool
}

type syncFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type syncRequest struct {
	Files       []syncFile `json:"files"`
	DeletePaths []string   `json:"deletePaths"`
	Restart     string     `json:"restart"`
}

type syncResponse struct {
	Phase     string   `json:"phase"`
	Changed   []string `json:"changed"`
	Deleted   []string `json:"deleted,omitempty"`
	Restarted bool     `json:"restarted"`
}

type runnerServer struct {
	mux        *http.ServeMux
	config     *runnerConfig
	supervisor *supervisor
	logs       *ringLog
}

func main() {
	cfg := configFromEnv()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := newRunnerServerWithContext(ctx, cfg)
	if cfg.StartCommand != "" {
		if err := srv.supervisor.start(ctx); err != nil {
			log.Printf("initial process start failed: %v", err)
		}
	}
	httpSrv := &http.Server{
		Addr:              defaultControlAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("sandbox runner control listening on %s (workdir=%s)", defaultControlAddr, cfg.WorkDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.supervisor.stop()
	_ = httpSrv.Shutdown(shutdown)
}

func configFromEnv() *runnerConfig {
	workdir := strings.TrimSpace(os.Getenv("SANDBOX_WORKDIR"))
	if workdir == "" {
		workdir = "/workspace"
	}
	controlToken := strings.TrimSpace(os.Getenv("SANDBOX_CONTROL_TOKEN"))
	_ = os.Unsetenv("SANDBOX_CONTROL_TOKEN")
	return &runnerConfig{
		WorkDir:              workdir,
		StartCommand:         strings.TrimSpace(os.Getenv("SANDBOX_START_COMMAND")),
		Port:                 strings.TrimSpace(os.Getenv("SANDBOX_PORT")),
		ControlToken:         controlToken,
		AllowInsecureControl: strings.EqualFold(strings.TrimSpace(os.Getenv("SANDBOX_ALLOW_INSECURE_CONTROL")), "true"),
	}
}

func newRunnerServer(cfg *runnerConfig) *runnerServer {
	return newRunnerServerWithContext(context.Background(), cfg)
}

func newRunnerServerWithContext(ctx context.Context, cfg *runnerConfig) *runnerServer {
	if cfg == nil {
		cfg = configFromEnv()
	}
	logs := newRingLog(500)
	s := &runnerServer{
		config: cfg,
		logs:   logs,
	}
	s.supervisor = newSupervisor(ctx, cfg, logs)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/sync", s.handleSync)
	mux.HandleFunc("/restart", s.handleRestart)
	mux.HandleFunc("/logs", s.handleLogs)
	s.mux = mux
	return s
}

func (s *runnerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *runnerServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *runnerServer) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	var req syncRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()

	changed := make([]string, 0, len(req.Files))
	startupChanged := false
	for _, f := range req.Files {
		clean, err := cleanWorkspacePath(f.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content := []byte(f.Content)
		if isStartupAffectingPath(clean) && workspaceFileContentChanged(root, clean, content) {
			startupChanged = true
		}
		if err := writeWorkspaceFile(root, clean, content); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusInternalServerError)
			return
		}
		changed = append(changed, clean)
	}
	deleted := make([]string, 0, len(req.DeletePaths))
	for _, raw := range req.DeletePaths {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := root.RemoveAll(clean); err != nil {
			http.Error(w, fmt.Sprintf("delete %q: %v", clean, err), http.StatusInternalServerError)
			return
		}
		if isStartupAffectingPath(clean) {
			startupChanged = true
		}
		deleted = append(deleted, clean)
	}
	restarted := false
	if s.shouldRestartAfterSync(req.Restart, startupChanged) {
		if err := s.supervisor.restart(r.Context()); err != nil {
			http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
			return
		}
		restarted = true
	}
	writeJSON(w, http.StatusOK, syncResponse{Phase: "Synced", Changed: changed, Deleted: deleted, Restarted: restarted})
}

func (s *runnerServer) shouldRestartAfterSync(policy string, startupChanged bool) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always":
		return true
	case "auto":
		return s.supervisor.hasCommand() && (startupChanged || !s.supervisor.isRunning())
	default:
		return false
	}
}

func openWorkspaceRoot(workdir string) (*os.Root, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenRoot(workdir)
}

func writeWorkspaceFile(root *os.Root, clean string, content []byte) error {
	parent := path.Dir(clean)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create parent: %w", err)
		}
	}
	return root.WriteFile(clean, content, 0o644)
}

func workspaceFileContentChanged(root *os.Root, clean string, next []byte) bool {
	current, err := root.ReadFile(clean)
	return err != nil || !bytes.Equal(current, next)
}

func isStartupAffectingPath(clean string) bool {
	switch clean {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "server.js":
		return true
	}
	return strings.HasPrefix(clean, "vite.config.")
}

func (s *runnerServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	if err := s.supervisor.restart(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarted": true})
}

func (s *runnerServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, strings.Join(s.logs.lines(), "\n"))
}

func (s *runnerServer) authorizeControl(w http.ResponseWriter, r *http.Request) bool {
	token := strings.TrimSpace(s.config.ControlToken)
	if token == "" {
		if s.config.AllowInsecureControl {
			return true
		}
		http.Error(w, "runner control token is not configured", http.StatusUnauthorized)
		return false
	}
	if subtleConstantTimeCompare(r.Header.Get("X-Sandbox-Control-Token"), token) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func subtleConstantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func cleanWorkspacePath(raw string) (string, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if raw == "" {
		return "", errors.New("path is required")
	}
	if path.IsAbs(raw) {
		return "", fmt.Errorf("absolute path %q is not allowed", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes workspace", raw)
	}
	return clean, nil
}

type supervisor struct {
	config *runnerConfig
	logs   *ringLog
	ctx    context.Context
	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
}

func newSupervisor(ctx context.Context, cfg *runnerConfig, logs *ringLog) *supervisor {
	if ctx == nil {
		ctx = context.Background()
	}
	return &supervisor{config: cfg, logs: logs, ctx: ctx}
}

func (s *supervisor) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.StartCommand == "" {
		return nil
	}
	return s.startLocked(ctx)
}

func (s *supervisor) restart(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stopLocked(); err != nil {
		return err
	}
	if s.config.StartCommand == "" {
		return nil
	}
	return s.startLocked(s.ctx)
}

func (s *supervisor) hasCommand() bool {
	return strings.TrimSpace(s.config.StartCommand) != ""
}

func (s *supervisor) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *supervisor) stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked()
}

func (s *supervisor) startLocked(ctx context.Context) error {
	if err := os.MkdirAll(s.config.WorkDir, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", s.config.StartCommand)
	cmd.Dir = s.config.WorkDir
	cmd.Env = sanitizedChildEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	done := make(chan struct{})
	s.done = done
	go s.scanOutput(stdout)
	go s.scanOutput(stderr)
	go func() {
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			s.logs.append("process exited: " + err.Error())
		}
		close(done)
	}()
	return nil
}

func sanitizedChildEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "SANDBOX_CONTROL_TOKEN=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (s *supervisor) stopLocked() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	pid := s.cmd.Process.Pid
	done := s.done
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	s.cmd = nil
	s.done = nil
	return nil
}

func (s *supervisor) scanOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		s.logs.append(scanner.Text())
	}
}

type ringLog struct {
	mu       sync.Mutex
	limit    int
	linesBuf []string
}

func newRingLog(limit int) *ringLog {
	return &ringLog{limit: limit}
}

func (r *ringLog) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.linesBuf = append(r.linesBuf, line)
	if len(r.linesBuf) > r.limit {
		copy(r.linesBuf, r.linesBuf[len(r.linesBuf)-r.limit:])
		r.linesBuf = r.linesBuf[:r.limit]
	}
}

func (r *ringLog) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.linesBuf))
	copy(out, r.linesBuf)
	return out
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
