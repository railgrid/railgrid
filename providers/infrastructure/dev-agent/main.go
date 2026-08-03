/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command kedge-dev-agent is the in-pod control process for development-mode
// components of any infrastructure Template (docs/app-studio-template-sandboxes.md §2).
// It generalizes the sandbox-runner control process: where the runner was
// welded into one template's node-shaped image, the agent is a static binary
// an init container installs into ANY toolchain image (node, python, go, …) —
// the dev overlay synthesized by the kro backend runs it as the component's
// command, wrapping the Template-declared start command.
//
// It does two things:
//
//  1. Supervises the component's dev process (KEDGE_DEV_START_COMMAND) in the
//     workspace, captures stdout/stderr into a ring buffer, and executes the
//     Template-declared reload procedure on file sync: match changed paths
//     against KEDGE_DEV_RELOAD_RULES (run "npm install" when package.json
//     changed), then restart per KEDGE_DEV_RELOAD_STRATEGY.
//
//  2. Serves the HTTP control API on :7070 the infrastructure provider's
//     data-plane handler proxies component verbs to
//     (…/components/<name>/{sync,restart,env,log}):
//
//     GET  /healthz  liveness; no auth.
//     POST /sync     write/delete workspace files; restart: ""|"auto"|"always".
//     POST /restart  stop + start the dev process.
//     POST /env      set non-secret env for the dev process; optional restart.
//     GET  /logs     current dev-process attempt output (text/plain).
//     GET  /status   current child-process and declared-port readiness (JSON).
//
// Every endpoint except /healthz requires X-Sandbox-Control-Token (constant-
// time compared against KEDGE_DEV_CONTROL_TOKEN, read once then cleared).
// File writes are confined to the workdir via os.Root. SANDBOX_* names are
// accepted as env fallbacks so the binary can also replace the sandbox-runner
// entrypoint during its retirement window.
//
// Invoked as `kedge-dev-agent --install <dir>` it copies its own executable
// into <dir> and exits — the init-container injection mode, which is what
// lets the dev image stay a plain toolchain image with nothing kedge-specific
// baked in.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	defaultControlAddr = ":7070"
	defaultExecAddr    = ":7071"
	controlTokenHeader = "X-Sandbox-Control-Token"
	agentBinaryName    = "kedge-dev-agent"

	previewConsolePluginName = "preview-console-plugin.mjs"
	previewConsoleJWKSName   = "preview-console-jwks.json"
	previewConsoleJWKSEnv    = "KEDGE_PREVIEW_CONSOLE_VERIFICATION_JWKS"
	workspaceManifestName    = ".kedge-workspace-manifest.json"
)

//go:embed preview-console-plugin.mjs
var previewConsolePlugin []byte

// reloadRule mirrors TemplateDevelopmentReloadRule: changed-path globs that
// require a command before the process restarts.
type reloadRule struct {
	Paths   []string `json:"paths"`
	Command string   `json:"command"`
}

type agentConfig struct {
	WorkDir              string
	StartCommand         string
	Port                 string
	ControlToken         string
	ReloadStrategy       string // "process" (default) | "container"
	ReloadRules          []reloadRule
	AllowInsecureControl bool
	DropChildGroups      bool
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "--install" {
		if err := installSelf(os.Args[2]); err != nil {
			log.Fatalf("install: %v", err)
		}
		if len(os.Args) >= 5 && os.Args[3] == "--prepare-workspace" {
			if err := preparePlatformState(os.Args[4]); err != nil {
				log.Fatalf("prepare workspace: %v", err)
			}
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "--exec-worker" {
		if err := runExecWorker(); err != nil {
			log.Fatalf("exec worker: %v", err)
		}
		return
	}
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := newAgentServer(ctx, cfg)
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
		log.Printf("kedge-dev-agent control listening on %s (workdir=%s)", defaultControlAddr, cfg.WorkDir)
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

// installSelf atomically installs the agent executable, the platform-owned
// preview-console Vite plugin, and (when configured) its trusted public JWKS
// into dir, the shared emptyDir the dev container mounts at /kedge/bin. Plain
// copies are used because the injector image may be scratch.
//
// Missing or invalid verification configuration disables the optional browser
// bridge without blocking the application. Any stale JWKS is removed so an old
// platform key set cannot be trusted accidentally after a bad rollout.
func installSelf(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomicInstall(dir, agentBinaryName, 0o755, src); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	if err := atomicInstall(dir, previewConsolePluginName, 0o644, bytes.NewReader(previewConsolePlugin)); err != nil {
		return fmt.Errorf("install preview console plugin: %w", err)
	}

	jwksPath := filepath.Join(dir, previewConsoleJWKSName)
	rawJWKS := strings.TrimSpace(os.Getenv(previewConsoleJWKSEnv))
	if rawJWKS == "" {
		if err := os.Remove(jwksPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale preview console JWKS: %w", err)
		}
		log.Printf("preview console bridge disabled: %s is unset", previewConsoleJWKSEnv)
	} else if jwks, err := normalizePreviewConsoleJWKS([]byte(rawJWKS)); err != nil {
		if removeErr := os.Remove(jwksPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove stale preview console JWKS after invalid configuration: %w", removeErr)
		}
		log.Printf("preview console bridge disabled: invalid %s: %v", previewConsoleJWKSEnv, err)
	} else if err := atomicInstall(dir, previewConsoleJWKSName, 0o644, bytes.NewReader(jwks)); err != nil {
		return fmt.Errorf("install preview console JWKS: %w", err)
	}

	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	log.Printf("installed %s and %s", filepath.Join(dir, agentBinaryName), filepath.Join(dir, previewConsolePluginName))
	return nil
}

func atomicInstall(dir, name string, mode os.FileMode, src io.Reader) (retErr error) {
	target := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

type publicVerificationJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

func normalizePreviewConsoleJWKS(raw []byte) ([]byte, error) {
	var document struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}
	if len(document.Keys) < 1 || len(document.Keys) > 2 {
		return nil, fmt.Errorf("want current key and optional previous key, got %d", len(document.Keys))
	}

	seen := map[string]bool{}
	keys := make([]publicVerificationJWK, 0, len(document.Keys))
	for i, fields := range document.Keys {
		if _, private := fields["d"]; private {
			return nil, fmt.Errorf("keys[%d] contains private key material", i)
		}
		var key publicVerificationJWK
		for name, dst := range map[string]*string{
			"kty": &key.Kty,
			"crv": &key.Crv,
			"kid": &key.Kid,
			"x":   &key.X,
			"y":   &key.Y,
			"alg": &key.Alg,
			"use": &key.Use,
		} {
			value, ok := fields[name]
			if !ok {
				continue
			}
			if err := json.Unmarshal(value, dst); err != nil {
				return nil, fmt.Errorf("keys[%d].%s must be a string", i, name)
			}
		}
		if key.Kty != "EC" || key.Crv != "P-256" || strings.TrimSpace(key.Kid) == "" {
			return nil, fmt.Errorf("keys[%d] must be a named EC P-256 key", i)
		}
		if key.Alg != "" && key.Alg != "ES256" {
			return nil, fmt.Errorf("keys[%d].alg must be ES256", i)
		}
		if key.Use != "" && key.Use != "sig" {
			return nil, fmt.Errorf("keys[%d].use must be sig", i)
		}
		if seen[key.Kid] {
			return nil, fmt.Errorf("duplicate key id %q", key.Kid)
		}
		seen[key.Kid] = true
		for name, coordinate := range map[string]string{"x": key.X, "y": key.Y} {
			decoded, err := base64.RawURLEncoding.DecodeString(coordinate)
			if err != nil || len(decoded) != 32 {
				return nil, fmt.Errorf("keys[%d].%s must be a 32-byte base64url coordinate", i, name)
			}
		}
		key.Alg = "ES256"
		key.Use = "sig"
		keys = append(keys, key)
	}
	return json.Marshal(struct {
		Keys []publicVerificationJWK `json:"keys"`
	}{Keys: keys})
}

// envOr reads the first non-empty of the KEDGE_DEV_* name and its SANDBOX_*
// fallback (compatibility with the sandbox-runner contract during retirement).
func envOr(primary, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(primary)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(fallback))
}

func configFromEnv() (*agentConfig, error) {
	workdir := envOr("KEDGE_DEV_WORKDIR", "SANDBOX_WORKDIR")
	if workdir == "" {
		workdir = "/workspace"
	}
	token := envOr("KEDGE_DEV_CONTROL_TOKEN", "SANDBOX_CONTROL_TOKEN")
	_ = os.Unsetenv("KEDGE_DEV_CONTROL_TOKEN")
	_ = os.Unsetenv("SANDBOX_CONTROL_TOKEN")

	strategy := strings.ToLower(envOr("KEDGE_DEV_RELOAD_STRATEGY", ""))
	switch strategy {
	case "", "process":
		strategy = "process"
	case "container":
	default:
		return nil, fmt.Errorf("unknown KEDGE_DEV_RELOAD_STRATEGY %q", strategy)
	}

	rules, err := reloadRulesFromEnv(os.Getenv("KEDGE_DEV_RELOAD_RULES"))
	if err != nil {
		return nil, err
	}

	insecure := envOr("KEDGE_DEV_ALLOW_INSECURE_CONTROL", "SANDBOX_ALLOW_INSECURE_CONTROL")
	return &agentConfig{
		WorkDir:              workdir,
		StartCommand:         envOr("KEDGE_DEV_START_COMMAND", "SANDBOX_START_COMMAND"),
		Port:                 envOr("KEDGE_DEV_PORT", "SANDBOX_PORT"),
		ControlToken:         token,
		ReloadStrategy:       strategy,
		ReloadRules:          rules,
		AllowInsecureControl: strings.EqualFold(insecure, "true"),
		DropChildGroups:      strings.EqualFold(os.Getenv("KEDGE_DEV_DROP_CHILD_GROUPS"), "true"),
	}, nil
}

func reloadRulesFromEnv(raw string) ([]reloadRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []reloadRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("KEDGE_DEV_RELOAD_RULES is not a JSON rule list: %w", err)
	}
	for i, r := range rules {
		if len(r.Paths) == 0 || strings.TrimSpace(r.Command) == "" {
			return nil, fmt.Errorf("KEDGE_DEV_RELOAD_RULES[%d] needs paths and a command", i)
		}
	}
	return rules, nil
}

// matchReloadRules returns the commands whose path globs match any of the
// changed workspace paths, in declaration order, deduplicated. Globs are
// path.Match patterns against the workdir-relative path; a pattern without a
// slash also matches by basename ("package.json" matches "web/package.json").
func matchReloadRules(rules []reloadRule, changed []string) []string {
	var commands []string
	seen := map[string]bool{}
	for _, rule := range rules {
		if seen[rule.Command] {
			continue
		}
		for _, pattern := range rule.Paths {
			if matchAny(pattern, changed) {
				commands = append(commands, rule.Command)
				seen[rule.Command] = true
				break
			}
		}
	}
	return commands
}

func matchAny(pattern string, changed []string) bool {
	for _, p := range changed {
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if ok, _ := path.Match(pattern, path.Base(p)); ok {
				return true
			}
		}
	}
	return false
}

type syncFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type syncRequest struct {
	Files          []syncFile `json:"files"`
	DeletePaths    []string   `json:"deletePaths"`
	Restart        string     `json:"restart"`
	SourceRevision uint64     `json:"sourceRevision,omitempty"`
	SourceDigest   string     `json:"sourceDigest,omitempty"`
}

type syncResponse struct {
	Phase          string   `json:"phase"`
	Changed        []string `json:"changed"`
	Deleted        []string `json:"deleted,omitempty"`
	ReloadRuns     []string `json:"reloadRuns,omitempty"`
	Restarted      bool     `json:"restarted"`
	ReloadError    string   `json:"reloadError,omitempty"`
	SourceRevision uint64   `json:"sourceRevision,omitempty"`
	SourceDigest   string   `json:"sourceDigest,omitempty"`
}

// workspaceManifest is synchronization metadata, not a security boundary.
// The agent rehashes every listed file before execution, so a stale or
// tampered manifest cannot make an old workspace look current. Runtime-created
// files (node_modules, build output, logs) are intentionally absent and are
// never deleted by authoritative sync.
type workspaceManifest struct {
	SourceRevision uint64   `json:"sourceRevision"`
	SourceDigest   string   `json:"sourceDigest"`
	Files          []string `json:"files"`
}

type envRequest struct {
	Env     map[string]string `json:"env"`
	Restart bool              `json:"restart"`
}

type envResponse struct {
	Phase     string   `json:"phase"`
	Applied   []string `json:"applied"`
	Restarted bool     `json:"restarted"`
}

type agentServer struct {
	mux        *http.ServeMux
	config     *agentConfig
	supervisor *supervisor
	logs       *ringLog
	execMu     sync.Mutex
}

func newAgentServer(ctx context.Context, cfg *agentConfig) *agentServer {
	logs := newRingLog(500)
	s := &agentServer{config: cfg, logs: logs}
	s.supervisor = newSupervisor(ctx, cfg, logs)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/sync", s.handleSync)
	mux.HandleFunc("/restart", s.handleRestart)
	mux.HandleFunc("/env", s.handleEnv)
	mux.HandleFunc("/logs", s.handleLogs)
	mux.HandleFunc("/status", s.handleStatus)
	s.mux = mux
	return s
}

func (s *agentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *agentServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *agentServer) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()
	workspaceLock, err := acquireWorkspaceMutationLock(r.Context(), s.config.WorkDir, s.config.DropChildGroups)
	if err != nil {
		http.Error(w, "lock workspace: "+err.Error(), http.StatusRequestTimeout)
		return
	}
	defer func() { _ = workspaceLock.Close() }()
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
	authoritative := req.SourceRevision != 0 || strings.TrimSpace(req.SourceDigest) != ""
	if authoritative && (req.SourceRevision == 0 || strings.TrimSpace(req.SourceDigest) == "") {
		http.Error(w, "sourceRevision and sourceDigest must be supplied together", http.StatusBadRequest)
		return
	}
	previous, found, err := readWorkspaceManifest(root)
	if err != nil {
		if req.SourceRevision == 0 && strings.TrimSpace(req.SourceDigest) == "" {
			http.Error(w, "read workspace manifest: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// A protected manifest is synchronization metadata, not source of truth.
		// If it is corrupted, an authoritative full-file sync can safely rebuild
		// it, but must not delete paths that are no longer known to be managed.
		log.Printf("workspace manifest is invalid; rebuilding from authoritative sync: %v", err)
		previous, found = workspaceManifest{}, false
	}
	var incomingPaths map[string]struct{}
	cleanDeletePaths := make([]string, 0, len(req.DeletePaths))
	for _, raw := range req.DeletePaths {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if authoritative {
			if err := validateManagedWorkspacePath(clean); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		cleanDeletePaths = append(cleanDeletePaths, clean)
	}
	if authoritative {
		incomingPaths, err = validateSyncFiles(req.Files)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotDigest, err := digestSyncFiles(req.Files)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if normalizeSourceDigest(req.SourceDigest) != gotDigest {
			http.Error(w, "sourceDigest does not match the supplied workspace files", http.StatusConflict)
			return
		}
		if found {
			switch {
			case req.SourceRevision < previous.SourceRevision:
				http.Error(w, "workspace sync revision is older than the applied revision", http.StatusConflict)
				return
			case req.SourceRevision == previous.SourceRevision && normalizeSourceDigest(previous.SourceDigest) != gotDigest:
				http.Error(w, "workspace sync revision was already applied with a different digest", http.StatusConflict)
				return
			case req.SourceRevision == previous.SourceRevision && verifyWorkspaceManifest(root, previous) == nil:
				writeJSON(w, http.StatusOK, syncResponse{Phase: "Synced", SourceRevision: previous.SourceRevision, SourceDigest: previous.SourceDigest})
				return
			}
		}
	}

	changed := make([]string, 0, len(req.Files))
	for _, f := range req.Files {
		clean, err := cleanWorkspacePath(f.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if authoritative {
			if err := validateManagedWorkspacePath(clean); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := ensureExecPathNoSymlink(root, clean, false); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusConflict)
			return
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusConflict)
			return
		}
		content := []byte(f.Content)
		if !workspaceFileContentChanged(root, clean, content) {
			continue
		}
		if err := writeWorkspaceFile(root, clean, content); err != nil {
			http.Error(w, fmt.Sprintf("write %q: %v", clean, err), http.StatusInternalServerError)
			return
		}
		changed = append(changed, clean)
	}
	deleted := make([]string, 0, len(req.DeletePaths))
	if authoritative {
		candidates := make(map[string]struct{}, len(previous.Files)+len(cleanDeletePaths))
		for _, raw := range previous.Files {
			candidates[raw] = struct{}{}
		}
		for _, raw := range cleanDeletePaths {
			candidates[raw] = struct{}{}
		}
		paths := make([]string, 0, len(candidates))
		for raw := range candidates {
			paths = append(paths, raw)
		}
		slices.Sort(paths)
		managed := make(map[string]struct{}, len(previous.Files))
		for _, raw := range previous.Files {
			managed[raw] = struct{}{}
		}
		for _, raw := range paths {
			if _, keep := incomingPaths[raw]; keep {
				continue
			}
			// When a valid prior manifest exists, explicit deletion hints are
			// advisory and may remove only paths that manifest managed. If the
			// manifest was missing/corrupt, the full sync is still authoritative
			// for writes, and FileStore's explicit hints allow safe convergence.
			if found {
				if _, ok := managed[raw]; !ok {
					continue
				}
			}
			removed, err := removeManagedWorkspaceFile(root, raw)
			if err != nil {
				http.Error(w, fmt.Sprintf("delete %q: %v", raw, err), http.StatusInternalServerError)
				return
			}
			if removed {
				deleted = append(deleted, raw)
			}
		}
	} else {
		for _, clean := range cleanDeletePaths {
			if err := root.RemoveAll(clean); err != nil {
				http.Error(w, fmt.Sprintf("delete %q: %v", clean, err), http.StatusInternalServerError)
				return
			}
			deleted = append(deleted, clean)
		}
	}

	resp := syncResponse{Phase: "Synced", Changed: changed, Deleted: deleted, SourceRevision: req.SourceRevision, SourceDigest: req.SourceDigest}
	if authoritative {
		manifest := workspaceManifest{SourceRevision: req.SourceRevision, SourceDigest: req.SourceDigest, Files: make([]string, 0, len(incomingPaths))}
		for clean := range incomingPaths {
			manifest.Files = append(manifest.Files, clean)
		}
		slices.Sort(manifest.Files)
		if err := writeWorkspaceManifest(root, manifest); err != nil {
			http.Error(w, "write workspace manifest: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// The Template-declared reload procedure: run matching rule commands
	// first (dependency installs), then restart per policy/strategy.
	touched := append(append([]string{}, changed...), deleted...)
	ruleCommands := matchReloadRules(s.config.ReloadRules, touched)
	restartNeeded := s.shouldRestartAfterSync(req.Restart, len(ruleCommands) > 0, len(s.config.ReloadRules) > 0, touched)
	if restartNeeded && len(ruleCommands) > 0 {
		resp.ReloadRuns = ruleCommands
		if err := s.supervisor.runReloadCommands(r.Context(), ruleCommands); err != nil {
			// Keep the sync result; surface the reload failure for the caller
			// (the dev process keeps running against the old dependencies).
			resp.ReloadError = err.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	if restartNeeded {
		if s.config.ReloadStrategy == "container" {
			// The declared escape hatch: let the pod restart the container.
			resp.Restarted = true
			writeJSON(w, http.StatusOK, resp)
			go func() {
				time.Sleep(200 * time.Millisecond)
				log.Print("reload strategy container: exiting for container restart")
				os.Exit(0)
			}()
			return
		}
		if err := s.supervisor.restart(r.Context()); err != nil {
			http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Restarted = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func validateSyncFiles(files []syncFile) (map[string]struct{}, error) {
	paths := make(map[string]struct{}, len(files))
	for _, file := range files {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return nil, err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return nil, err
		}
		if !utf8.ValidString(file.Content) || strings.ContainsRune(file.Content, '\x00') {
			return nil, fmt.Errorf("source file %q must be UTF-8 text without NUL bytes", clean)
		}
		if _, exists := paths[clean]; exists {
			return nil, fmt.Errorf("duplicate source path %q", clean)
		}
		paths[clean] = struct{}{}
	}
	return paths, nil
}

func digestSyncFiles(files []syncFile) (string, error) {
	type digestEntry struct {
		path    string
		content string
	}
	entries := make([]digestEntry, 0, len(files))
	for _, file := range files {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return "", err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return "", err
		}
		entries = append(entries, digestEntry{path: clean, content: file.Content})
	}
	slices.SortFunc(entries, func(a, b digestEntry) int { return strings.Compare(a.path, b.path) })
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(entry.content))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readWorkspaceManifest(root *os.Root) (workspaceManifest, bool, error) {
	raw, err := root.ReadFile(workspaceManifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return workspaceManifest{}, false, nil
	}
	if err != nil {
		return workspaceManifest{}, false, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return workspaceManifest{}, false, err
	}
	if manifest.SourceRevision == 0 || normalizeSourceDigest(manifest.SourceDigest) == "" {
		return workspaceManifest{}, false, errors.New("manifest has no source revision or digest")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for i, rawPath := range manifest.Files {
		clean, err := cleanWorkspacePath(rawPath)
		if err != nil {
			return workspaceManifest{}, false, fmt.Errorf("manifest files[%d]: %w", i, err)
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return workspaceManifest{}, false, fmt.Errorf("manifest files[%d]: %w", i, err)
		}
		if _, exists := seen[clean]; exists {
			return workspaceManifest{}, false, fmt.Errorf("manifest duplicates path %q", clean)
		}
		seen[clean] = struct{}{}
		manifest.Files[i] = clean
	}
	slices.Sort(manifest.Files)
	return manifest, true, nil
}

func writeWorkspaceManifest(root *os.Root, manifest workspaceManifest) error {
	slices.Sort(manifest.Files)
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	tmp := workspaceManifestName + ".tmp"
	if err := root.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := root.Rename(tmp, workspaceManifestName); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

func removeManagedWorkspaceFile(root *os.Root, raw string) (bool, error) {
	clean, err := cleanWorkspacePath(raw)
	if err != nil {
		return false, err
	}
	if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	info, err := root.Lstat(clean)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("managed path is not a regular file")
	}
	if err := root.Remove(clean); err != nil {
		return false, err
	}
	return true, nil
}

func normalizeSourceDigest(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "sha256:")
}

func validateManagedWorkspacePath(clean string) error {
	for _, part := range strings.Split(clean, "/") {
		switch strings.ToLower(part) {
		case ".git", "node_modules", ".assistant-snapshots":
			return fmt.Errorf("workspace path contains reserved component %q", part)
		}
	}
	return nil
}

func verifyWorkspaceManifest(root *os.Root, manifest workspaceManifest) error {
	if manifest.SourceRevision == 0 || normalizeSourceDigest(manifest.SourceDigest) == "" {
		return errors.New("workspace manifest has no source revision or digest")
	}
	entries := make([]syncFile, 0, len(manifest.Files))
	for _, raw := range manifest.Files {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			return err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return err
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			return err
		}
		info, err := root.Lstat(clean)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed path %q is not a regular file", clean)
		}
		content, err := root.ReadFile(clean)
		if err != nil {
			return err
		}
		entries = append(entries, syncFile{Path: clean, Content: string(content)})
	}
	got, err := digestSyncFiles(entries)
	if err != nil {
		return err
	}
	if normalizeSourceDigest(manifest.SourceDigest) != got {
		return fmt.Errorf("workspace manifest digest does not match managed files")
	}
	return nil
}

// shouldRestartAfterSync decides the post-sync restart. "always" restarts
// unconditionally; "auto" restarts when a reload rule fired, when the process
// isn't running, or — for templates that declare no rules — when the legacy
// startup-affecting heuristic matches (the sandbox-runner behavior).
func (s *agentServer) shouldRestartAfterSync(policy string, ruleFired, rulesDeclared bool, touched []string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always":
		return true
	case "auto":
		if !s.supervisor.hasCommand() {
			return false
		}
		if !s.supervisor.isRunning() || ruleFired {
			return true
		}
		if !rulesDeclared {
			for _, p := range touched {
				if isStartupAffectingPath(p) {
					return true
				}
			}
		}
		return false
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

// isStartupAffectingPath is the legacy node-shaped heuristic, used only when
// the template declares no reload rules.
func isStartupAffectingPath(clean string) bool {
	switch path.Base(clean) {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "server.js":
		return true
	}
	return strings.HasPrefix(path.Base(clean), "vite.config.")
}

func (s *agentServer) handleRestart(w http.ResponseWriter, r *http.Request) {
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

func (s *agentServer) handleEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	var req envRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	applied, err := s.supervisor.setEnv(req.Env)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	restarted := false
	if req.Restart {
		if err := s.supervisor.restart(r.Context()); err != nil {
			http.Error(w, "restart: "+err.Error(), http.StatusInternalServerError)
			return
		}
		restarted = true
	}
	writeJSON(w, http.StatusOK, envResponse{Phase: "EnvUpdated", Applied: applied, Restarted: restarted})
}

func (s *agentServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, strings.Join(s.logs.lines(), "\n"))
}

type processStatusResponse struct {
	AttemptID               uint64 `json:"attemptID"`
	AttemptStartedUnixMilli int64  `json:"attemptStartedUnixMilli,omitempty"`
	Configured              bool   `json:"configured"`
	Running                 bool   `json:"running"`
	Port                    string `json:"port,omitempty"`
	PortReachable           bool   `json:"portReachable,omitempty"`
}

func (s *agentServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeControl(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.supervisor.status())
}

func (s *agentServer) authorizeControl(w http.ResponseWriter, r *http.Request) bool {
	token := strings.TrimSpace(s.config.ControlToken)
	if token == "" {
		if s.config.AllowInsecureControl {
			return true
		}
		http.Error(w, "dev agent control token is not configured", http.StatusUnauthorized)
		return false
	}
	if subtleConstantTimeCompare(r.Header.Get(controlTokenHeader), token) {
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
	if clean == workspaceManifestName {
		return "", fmt.Errorf("path %q is reserved for the platform sync manifest", raw)
	}
	if clean == platformMetadataDir || strings.HasPrefix(clean, platformMetadataDir+"/") {
		return "", fmt.Errorf("path %q is reserved for platform metadata", raw)
	}
	return clean, nil
}

type supervisor struct {
	config         *agentConfig
	logs           *ringLog
	ctx            context.Context
	mu             sync.Mutex
	cmd            *exec.Cmd
	done           chan struct{}
	attempt        uint64
	attemptStarted time.Time
	// customEnv holds non-secret environment variables set at runtime via
	// /env, merged over the process environment on the next (re)start.
	customEnv map[string]string
}

const maxRuntimeEnvKeys = 32

// reservedEnvPrefixes protect the agent's own control plane (and the legacy
// sandbox names) from being overridden through /env or child env merging.
var reservedEnvPrefixes = []string{"KEDGE_DEV_", "SANDBOX_"}

func hasReservedEnvPrefix(name string) bool {
	return slices.ContainsFunc(reservedEnvPrefixes, func(p string) bool {
		return strings.HasPrefix(name, p)
	})
}

func (s *supervisor) setEnv(env map[string]string) ([]string, error) {
	if len(env) == 0 {
		return nil, fmt.Errorf("at least one environment variable is required")
	}
	if len(env) > maxRuntimeEnvKeys {
		return nil, fmt.Errorf("at most %d environment variables may be set in one call", maxRuntimeEnvKeys)
	}
	for key := range env {
		name := strings.TrimSpace(key)
		if !isValidRuntimeEnvName(name) {
			return nil, fmt.Errorf("invalid environment variable name %q; use letters, digits, and underscores", key)
		}
		if hasReservedEnvPrefix(name) {
			return nil, fmt.Errorf("environment variable %q is reserved for the dev agent", name)
		}
		if isSecretLikeRuntimeEnvName(name) {
			return nil, fmt.Errorf("secret-looking environment variable %q cannot be set through /env", name)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.customEnv == nil {
		s.customEnv = map[string]string{}
	}
	applied := make([]string, 0, len(env))
	for key, value := range env {
		name := strings.TrimSpace(key)
		s.customEnv[name] = value
		applied = append(applied, name)
	}
	sort.Strings(applied)
	return applied, nil
}

func isValidRuntimeEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isSecretLikeRuntimeEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "ACCESS_KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return upper == "KEY" || strings.HasSuffix(upper, "_KEY")
}

func newSupervisor(ctx context.Context, cfg *agentConfig, logs *ringLog) *supervisor {
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

// runReloadCommands executes rule commands sequentially in the workdir,
// logging their output into the same ring buffer as the dev process so the
// caller sees "npm install" progress in /logs. Fails on the first error.
func (s *supervisor) runReloadCommands(ctx context.Context, commands []string) error {
	// Serialize reload work with start/restart. A successful restart begins a
	// new log epoch; allowing an older reload command to keep writing after
	// that boundary would reintroduce stale failures into the current attempt.
	s.mu.Lock()
	defer s.mu.Unlock()
	childEnv := make(map[string]string, len(s.customEnv))
	maps.Copy(childEnv, s.customEnv)
	for _, command := range commands {
		s.logs.append("[kedge reload] " + command)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", command)
		cmd.Dir = s.config.WorkDir
		cmd.Env = mergeChildEnv(os.Environ(), childEnv, s.config.Port)
		if s.config.DropChildGroups {
			cmd.SysProcAttr = platformChildProcAttr(false)
		}
		out, err := cmd.CombinedOutput()
		for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
			if line != "" {
				s.logs.append(line)
			}
		}
		if err != nil {
			s.logs.append("[kedge reload] failed: " + err.Error())
			return fmt.Errorf("reload command %q: %w", command, err)
		}
	}
	return nil
}

func (s *supervisor) hasCommand() bool {
	return strings.TrimSpace(s.config.StartCommand) != ""
}

func (s *supervisor) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return processRunning(s.done, s.cmd)
}

func (s *supervisor) status() processStatusResponse {
	s.mu.Lock()
	status := processStatusResponse{
		AttemptID:  s.attempt,
		Configured: strings.TrimSpace(s.config.StartCommand) != "",
		Running:    processRunning(s.done, s.cmd),
		Port:       strings.TrimSpace(s.config.Port),
	}
	if !s.attemptStarted.IsZero() {
		status.AttemptStartedUnixMilli = s.attemptStarted.UnixMilli()
	}
	s.mu.Unlock()
	if status.Running && status.Port != "" {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", status.Port), 250*time.Millisecond)
		if err == nil {
			status.PortReachable = true
			_ = conn.Close()
		}
	}
	return status
}

func processRunning(done chan struct{}, cmd *exec.Cmd) bool {
	if cmd == nil || done == nil {
		return false
	}
	select {
	case <-done:
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
	cmd.Env = mergeChildEnv(os.Environ(), s.customEnv, s.config.Port)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if s.config.DropChildGroups {
		cmd.SysProcAttr = platformChildProcAttr(true)
	}
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
	attempt := s.logs.beginAttempt()
	s.cmd = cmd
	s.attempt = attempt
	s.attemptStarted = time.Now()
	done := make(chan struct{})
	s.done = done
	go s.scanOutput(attempt, stdout)
	go s.scanOutput(attempt, stderr)
	go func() {
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			s.logs.appendAttempt(attempt, "process exited: "+err.Error())
		}
		close(done)
	}()
	return nil
}

func sanitizedChildEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "KEDGE_DEV_CONTROL_TOKEN=") || strings.HasPrefix(entry, "SANDBOX_CONTROL_TOKEN=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// mergeChildEnv layers custom runtime env over the sanitized process
// environment. Reserved-prefix names are skipped so custom env cannot touch
// the control plane. When the component declares a dev port, PORT and
// SANDBOX_PORT are exported for the child (unless already set) — the common
// conventions dev servers and the legacy vite shim respect.
func mergeChildEnv(base []string, custom map[string]string, devPort string) []string {
	out := sanitizedChildEnv(base)
	index := make(map[string]int, len(out))
	for i, entry := range out {
		if name, _, ok := strings.Cut(entry, "="); ok {
			index[name] = i
		}
	}
	set := func(name, value string, override bool) {
		if i, ok := index[name]; ok {
			if override {
				out[i] = name + "=" + value
			}
			return
		}
		index[name] = len(out)
		out = append(out, name+"="+value)
	}
	if devPort = strings.TrimSpace(devPort); devPort != "" {
		set("PORT", devPort, false)
		set("SANDBOX_PORT", devPort, false)
	}
	names := make([]string, 0, len(custom))
	for name := range custom {
		if name == "" || hasReservedEnvPrefix(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		set(name, custom[name], true)
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

func (s *supervisor) scanOutput(attempt uint64, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		s.logs.appendAttempt(attempt, scanner.Text())
	}
}

type ringLog struct {
	mu       sync.Mutex
	limit    int
	attempt  uint64
	linesBuf []string
}

func newRingLog(limit int) *ringLog {
	return &ringLog{limit: limit}
}

func (r *ringLog) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLocked(line)
}

func (r *ringLog) beginAttempt() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt++
	r.linesBuf = nil
	return r.attempt
}

func (r *ringLog) appendAttempt(attempt uint64, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if attempt != r.attempt {
		return
	}
	r.appendLocked(line)
}

func (r *ringLog) appendLocked(line string) {
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
