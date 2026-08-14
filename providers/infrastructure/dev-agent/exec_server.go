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

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	execRequestMaxBytes    = 512 << 10
	execDefaultTimeout     = 30 * time.Second
	execMaxTimeout         = 2 * time.Minute
	execDefaultOutputBytes = 256 << 10
	execMaxOutputBytes     = 256 << 10
	execMaxWorkDirBytes    = 256
)

var (
	errExecPathEscape = errors.New("path escapes execution workspace")
	errExecSymlink    = errors.New("symbolic links are not allowed in execution workspace paths")
)

type execResponse struct {
	Phase           string   `json:"phase"`
	Argv            []string `json:"argv,omitempty"`
	WorkDir         string   `json:"workDir"`
	ExitCode        int      `json:"exitCode"`
	TimedOut        bool     `json:"timedOut,omitempty"`
	Cancelled       bool     `json:"cancelled,omitempty"`
	Stdout          string   `json:"stdout,omitempty"`
	Stderr          string   `json:"stderr,omitempty"`
	StdoutTruncated bool     `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool     `json:"stderrTruncated,omitempty"`
	SourceRevision  uint64   `json:"sourceRevision,omitempty"`
	SourceDigest    string   `json:"sourceDigest,omitempty"`
	DurationMS      int64    `json:"durationMs"`
	Error           string   `json:"error,omitempty"`
}

// persistentExecRequest is the normal dev-agent protocol. Source files are
// deliberately absent: /sync owns the persistent workspace and this endpoint
// verifies the platform-applied revision/digest before launching argv.
type persistentExecRequest struct {
	Argv           []string `json:"argv"`
	WorkDir        string   `json:"workDir,omitempty"`
	TimeoutMS      int      `json:"timeoutMs,omitempty"`
	MaxOutputBytes int      `json:"maxOutputBytes,omitempty"`
	SourceRevision uint64   `json:"sourceRevision"`
	SourceDigest   string   `json:"sourceDigest"`
}

type statelessExecutor struct {
	workspace string
	execute   func(context.Context, string, persistentExecRequest) (execResponse, error)
	exit      func(int)
	failed    atomic.Bool
}

func (s *statelessExecutor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/exec" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.failed.Load() {
		http.Error(w, "executor is unavailable after an unproven process cleanup", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, execRequestMaxBytes)
	var req persistentExecRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "request body must contain one JSON object", http.StatusBadRequest)
		return
	}
	execute := s.execute
	if execute == nil {
		execute = runPersistentExec
	}
	result, err := execute(r.Context(), s.workspace, req)
	if err != nil {
		// Revision/digest mismatch is a conflict: the caller must wait for the
		// authoritative sync evidence rather than retrying the same command.
		status := http.StatusBadRequest
		if errors.Is(err, errExecCleanupUnproven) {
			status = http.StatusInternalServerError
		} else if strings.Contains(err.Error(), "source revision") || strings.Contains(err.Error(), "source digest") || strings.Contains(err.Error(), "workspace manifest") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		if errors.Is(err, errExecCleanupUnproven) {
			s.failStop()
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// failStop makes cleanup uncertainty terminal for this executor. Continuing to
// accept commands after an execution may have escaped would violate the one-at-
// a-time process boundary; the pod restart restores a clean PID namespace.
func (s *statelessExecutor) failStop() {
	if !s.failed.CompareAndSwap(false, true) {
		return
	}
	exit := s.exit
	if exit == nil {
		exit = os.Exit
	}
	go func() {
		// Give the HTTP server a brief opportunity to flush the cleanup failure
		// to the coordinator before terminating the executor process.
		time.Sleep(10 * time.Millisecond)
		exit(1)
	}()
}

func runPersistentExec(parent context.Context, workspace string, req persistentExecRequest) (execResponse, error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := validatePersistentExecRequest(req); err != nil {
		return execResponse{}, err
	}
	rootPath, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil || strings.TrimSpace(workspace) == "" {
		return execResponse{}, errors.New("execution workspace is required")
	}
	if err := rejectRootSymlink(rootPath); err != nil {
		return execResponse{}, err
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return execResponse{}, fmt.Errorf("create execution workspace: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return execResponse{}, fmt.Errorf("open execution workspace: %w", err)
	}
	defer func() { _ = root.Close() }()
	workDir, err := cleanExecWorkDir(req.WorkDir)
	if err != nil {
		return execResponse{}, err
	}
	if err := ensureExecDirectory(root, workDir); err != nil {
		return execResponse{}, err
	}
	manifest, found, err := readWorkspaceManifest(root)
	if err != nil {
		return execResponse{}, fmt.Errorf("read workspace manifest: %w", err)
	}
	if !found {
		return execResponse{}, errors.New("source revision is not synchronized: workspace manifest is missing")
	}
	if manifest.SourceRevision != req.SourceRevision {
		return execResponse{}, fmt.Errorf("source revision %d is not the applied source revision %d", req.SourceRevision, manifest.SourceRevision)
	}
	if normalizeSourceDigest(manifest.SourceDigest) != normalizeSourceDigest(req.SourceDigest) {
		return execResponse{}, errors.New("source digest does not match the applied workspace manifest")
	}
	if len(manifest.PendingReloadCommands) > 0 {
		return execResponse{}, errors.New("source revision dependency reload is still pending")
	}
	if err := verifyWorkspaceManifest(root, manifest); err != nil {
		return execResponse{}, fmt.Errorf("workspace manifest verification failed: %w", err)
	}

	workPath := filepath.Join(rootPath, filepath.FromSlash(workDir))
	env := sanitizedExecEnvironment(workPath)
	executable, err := resolveExecExecutable(req.Argv[0], env, workPath)
	if err != nil {
		return execResponse{}, err
	}
	outputLimit := boundedExecOutput(req.MaxOutputBytes)
	started := time.Now()
	stdout := newExecOutputBuffer(outputLimit)
	stderr := newExecOutputBuffer(outputLimit)
	cmd := exec.Command(executable, req.Argv[1:]...)
	cmd.Dir = workPath
	cmd.Env = env
	execMarker := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	cmd.Env = append(cmd.Env, "FAROS_EXEC_SESSION="+execMarker)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := enableChildSubreaper(); err != nil {
		return execResponse{}, fmt.Errorf("enable child subreaper: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return execResponse{}, fmt.Errorf("start %q: %w", req.Argv[0], err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	timer := time.NewTimer(boundedExecTimeout(req.TimeoutMS))
	defer timer.Stop()
	response := execResponse{
		Phase: "completed", Argv: append([]string(nil), req.Argv...), WorkDir: workDir,
		ExitCode: -1, SourceRevision: req.SourceRevision, SourceDigest: req.SourceDigest,
	}
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-timer.C:
		response.Phase = "timed_out"
		response.TimedOut = true
		_ = cmd.Process.Kill()
		if err := cleanupExecProcesses(execMarker, 2*time.Second); err != nil {
			return execResponse{}, err
		}
		waitErr = <-waitCh
	case <-parent.Done():
		response.Phase = "cancelled"
		response.Cancelled = true
		_ = cmd.Process.Kill()
		if err := cleanupExecProcesses(execMarker, 2*time.Second); err != nil {
			return execResponse{}, err
		}
		waitErr = <-waitCh
	}
	if err := cleanupExecProcesses(execMarker, 2*time.Second); err != nil {
		return execResponse{}, err
	}
	reapExitedChildren()
	response.DurationMS = time.Since(started).Milliseconds()
	response.Stdout = stdout.String()
	response.Stderr = stderr.String()
	response.StdoutTruncated = stdout.Truncated()
	response.StderrTruncated = stderr.Truncated()
	if exitCode, ok := execExitCode(waitErr); ok {
		response.ExitCode = exitCode
	}
	if waitErr != nil && !response.TimedOut && !response.Cancelled {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			response.Error = waitErr.Error()
		}
	}
	return response, nil
}

func validatePersistentExecRequest(req persistentExecRequest) error {
	if len(req.Argv) == 0 || strings.TrimSpace(req.Argv[0]) == "" {
		return errors.New("argv must contain an executable")
	}
	if len(req.Argv) > 128 {
		return errors.New("argv contains too many arguments")
	}
	for i, arg := range req.Argv {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("argv[%d] contains NUL", i)
		}
		if len([]byte(arg)) > 16<<10 {
			return fmt.Errorf("argv[%d] is too large", i)
		}
	}
	if req.SourceRevision == 0 {
		return errors.New("source revision is required")
	}
	digest := normalizeSourceDigest(req.SourceDigest)
	if len(digest) != sha256.Size*2 {
		return errors.New("source digest is required")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("source digest must be a SHA-256 hex digest")
	}
	if req.TimeoutMS < 0 || (req.TimeoutMS > 0 && time.Duration(req.TimeoutMS)*time.Millisecond > execMaxTimeout) {
		return fmt.Errorf("timeout must be between 1ms and %s", execMaxTimeout)
	}
	if req.MaxOutputBytes < 0 || req.MaxOutputBytes > execMaxOutputBytes {
		return fmt.Errorf("maxOutputBytes must be between 1 and %d", execMaxOutputBytes)
	}
	return nil
}

func cleanExecPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') {
		return "", errors.New("workspace path must be non-empty and use slash-separated components")
	}
	if path.IsAbs(raw) {
		return "", errExecPathEscape
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", errExecPathEscape
		}
		switch strings.ToLower(part) {
		case ".git", "node_modules", ".assistant-snapshots":
			return "", fmt.Errorf("workspace path contains reserved component %q", part)
		}
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errExecPathEscape
	}
	if clean == workspaceManifestName {
		return "", fmt.Errorf("workspace path %q is reserved for the platform sync manifest", raw)
	}
	return clean, nil
}

func cleanExecWorkDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ".", nil
	}
	if len([]byte(raw)) > execMaxWorkDirBytes {
		return "", fmt.Errorf("workdir exceeds %d bytes", execMaxWorkDirBytes)
	}
	if path.Clean(raw) == "." {
		for _, part := range strings.Split(raw, "/") {
			if part == ".." {
				return "", errExecPathEscape
			}
		}
		return ".", nil
	}
	return cleanExecPath(raw)
}

func ensureExecDirectory(root *os.Root, clean string) error {
	if clean == "." {
		return nil
	}
	if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
		return err
	}
	info, err := root.Stat(clean)
	if err != nil {
		return fmt.Errorf("workdir %q: %w", clean, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workdir %q is not a directory", clean)
	}
	return nil
}

func ensureExecPathNoSymlink(root *os.Root, clean string, includeTarget bool) error {
	parts := strings.Split(clean, "/")
	for index, part := range parts {
		current := strings.Join(parts[:index+1], "/")
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errExecSymlink
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("workspace path component %q is not a directory", part)
		}
		if index == len(parts)-1 && !includeTarget {
			return nil
		}
	}
	return nil
}

func rejectRootSymlink(rootPath string) error {
	rootPath = filepath.Clean(rootPath)
	current := filepath.VolumeName(rootPath)
	if strings.HasPrefix(rootPath, string(filepath.Separator)) {
		current += string(filepath.Separator)
	}
	for _, part := range strings.Split(strings.TrimPrefix(rootPath, current), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errExecSymlink
		}
	}
	return nil
}

func sanitizedExecEnvironment(workDir string) []string {
	// Never inherit the agent's environment: it may contain the control token
	// or provider/runtime credentials. Keep this server-owned and deterministic.
	// This is not a container boundary: the child shares the component's mounts,
	// network and PID namespace, so deployment-level secret/credential exposure
	// must be handled by the dev workload itself.
	values := map[string]string{
		"HOME":   "/tmp",
		"LANG":   "C.UTF-8",
		"PATH":   "/usr/local/go/bin:/go/bin:/usr/local/bin:/usr/bin:/bin",
		"PWD":    workDir,
		"TMPDIR": "/tmp",
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func resolveExecExecutable(name string, env []string, workDir string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.ContainsRune(name, '\x00') {
		return "", errors.New("argv executable is invalid")
	}
	if strings.Contains(name, "/") {
		if filepath.IsAbs(name) {
			return name, nil
		}
		clean, err := cleanExecPath(name)
		if err != nil {
			return "", err
		}
		return filepath.Join(workDir, filepath.FromSlash(clean)), nil
	}
	pathValue := ""
	for _, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			pathValue = strings.TrimPrefix(value, "PATH=")
			break
		}
	}
	if pathValue == "" {
		return "", errors.New("argv executable must be absolute or PATH must be supplied explicitly")
	}
	for _, directory := range strings.Split(pathValue, string(os.PathListSeparator)) {
		if directory == "" || !filepath.IsAbs(directory) {
			return "", errors.New("execution PATH entries must be absolute")
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q was not found in explicit PATH", name)
}

func boundedExecTimeout(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		return execDefaultTimeout
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func boundedExecOutput(max int) int {
	if max <= 0 {
		return execDefaultOutputBytes
	}
	return max
}

type execOutputBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func newExecOutputBuffer(limit int) *execOutputBuffer {
	return &execOutputBuffer{limit: limit}
}

func (b *execOutputBuffer) Write(data []byte) (int, error) {
	if b.limit <= b.Len() {
		b.truncated = b.truncated || len(data) > 0
		return len(data), nil
	}
	remaining := b.limit - b.Len()
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.truncated = true
		return len(data), nil
	}
	return b.Buffer.Write(data)
}

// ReadFrom shadows bytes.Buffer.ReadFrom. os/exec copies pipe output through
// io.ReaderFrom when the destination advertises it; delegating to the embedded
// bytes.Buffer would bypass Write's bound entirely.
func (b *execOutputBuffer) ReadFrom(reader io.Reader) (int64, error) {
	var total int64
	buf := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			written, _ := b.Write(buf[:n])
			total += int64(written)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}

func (b *execOutputBuffer) Truncated() bool { return b.truncated }

func execExitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1, false
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Exited() {
			return status.ExitStatus(), true
		}
		if status.Signaled() {
			return 128 + int(status.Signal()), true
		}
	}
	return -1, true
}
