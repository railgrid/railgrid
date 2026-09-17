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

// Package supervisor runs one long-lived child process for an edge add-on and
// keeps it running.
//
// The child is always the agent's OWN executable invoked with a different
// subcommand (`railgrid runner run ...`), so an add-on upgrades with the agent
// and there is no second artifact to distribute or verify.
//
// Three properties are load-bearing and are enforced here rather than left to
// callers:
//
//   - The child never runs as uid 0. A root agent MUST hand over an explicit
//     non-root uid/gid; a non-root agent runs the child as itself. There is no
//     third case.
//   - The child's environment is built from scratch. Nothing is inherited, so
//     the hub bearer token, the agent kubeconfig path and any API keys in the
//     agent's environment cannot reach a code-execution child. This is the same
//     reasoning as the Codex adapter's safeEnv, taken one step further: safeEnv
//     subtracts from os.Environ(), this adds to an empty slice.
//   - Stop kills the whole process GROUP. The runner spawns Codex, which spawns
//     git; signalling only the direct child would leave those behind.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// DefaultInitialBackoff is the pause after the first unexpected exit.
	DefaultInitialBackoff = time.Second
	// MaxBackoff caps the restart pause. A wedged add-on must keep retrying —
	// the host may recover — but not faster than once a minute.
	MaxBackoff = 60 * time.Second
	// DefaultStopGrace is how long a child gets to exit after SIGTERM before
	// the group is killed.
	DefaultStopGrace = 10 * time.Second
	// DefaultMaxLogBytes bounds the add-on log file. An add-on that crash-loops
	// while printing must not fill the host's disk.
	DefaultMaxLogBytes int64 = 8 << 20
	// stableRun is how long a child must stay up before its exit is treated as
	// a fresh failure rather than a continuing crash loop.
	stableRun = 60 * time.Second
)

// InheritUID tells the supervisor to run the child as the agent's own account.
// It is only legal when the agent itself is not root.
const InheritUID = -1

// Config describes one supervised child.
type Config struct {
	// Name identifies the child in log lines and errors.
	Name string
	// Executable is an absolute path. Callers pass os.Executable().
	Executable string
	// Args are the arguments after the executable name.
	Args []string
	// Dir is the child's working directory.
	Dir string
	// Env is the COMPLETE environment for the child. Nothing is added and
	// nothing is inherited; build it with ChildEnv.
	Env []string
	// UID/GID are the account the child runs as, or InheritUID to run as the
	// agent's own (non-root) account.
	UID int
	GID int
	// LogPath receives the child's stdout and stderr, owner-only and bounded.
	LogPath string
	// MaxLogBytes bounds LogPath; zero means DefaultMaxLogBytes.
	MaxLogBytes int64
	// StopGrace is the SIGTERM-to-SIGKILL window; zero means DefaultStopGrace.
	StopGrace time.Duration
	// InitialBackoff is the first restart pause; zero means
	// DefaultInitialBackoff. Tests shorten it.
	InitialBackoff time.Duration
}

// ChildEnv builds a child environment from scratch. It deliberately takes only
// what a process needs to find its home and its tools — never os.Environ().
func ChildEnv(home, path string) []string {
	if path == "" {
		path = DefaultPath
	}
	return []string{"HOME=" + home, "PATH=" + path}
}

// DefaultPath is the search path handed to add-on children. It matches the
// LaunchDaemon plist so a Homebrew-installed Codex is found on macOS.
const DefaultPath = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"

// Supervisor owns at most one running child at a time.
type Supervisor struct {
	cfg Config

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	alive   bool
	starts  int
	lastErr error
}

// New validates cfg and returns a stopped supervisor. It refuses a uid-0
// target outright: a code-execution child of a root agent must drop privileges,
// and "the caller forgot" must fail here rather than at exec time.
func New(cfg Config) (*Supervisor, error) {
	if cfg.Executable == "" {
		return nil, errors.New("supervisor: executable is required")
	}
	if cfg.LogPath == "" {
		return nil, errors.New("supervisor: log path is required")
	}
	switch {
	case cfg.UID == 0 || cfg.GID == 0:
		return nil, fmt.Errorf("supervisor %q: refusing to run a child as uid 0", cfg.Name)
	case cfg.UID < 0 && cfg.GID >= 0, cfg.UID >= 0 && cfg.GID < 0:
		return nil, fmt.Errorf("supervisor %q: uid and gid must both be set or both be inherited", cfg.Name)
	case cfg.UID < 0 && os.Geteuid() == 0:
		return nil, fmt.Errorf("supervisor %q: a root agent must name a non-root account to run the child as", cfg.Name)
	}
	if cfg.MaxLogBytes <= 0 {
		cfg.MaxLogBytes = DefaultMaxLogBytes
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = DefaultStopGrace
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = DefaultInitialBackoff
	}
	return &Supervisor{cfg: cfg}, nil
}

// Start launches the child and keeps restarting it until Stop. Calling Start on
// an already-running supervisor is a no-op, so a reconcile loop can call it on
// every pass.
func (s *Supervisor) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	// The supervision loop outlives the reconcile that started it, so it gets
	// its own context rather than the caller's request-scoped one. Only Stop
	// (or the agent shutting down, via the manager) ends it.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.supervise(runCtx, s.done)
}

// Stop terminates the child's process group and waits for the supervision loop
// to finish. It is safe to call on a stopped supervisor.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("supervisor %q: child did not stop: %w", s.cfg.Name, ctx.Err())
	}
}

// Alive reports whether a child process is currently running.
func (s *Supervisor) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

// Starts is the number of child launches so far. Tests use it to observe
// restarts; the runner add-on uses it only for log lines.
func (s *Supervisor) Starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

// LastError is the most recent unexpected child exit or launch failure, or nil.
func (s *Supervisor) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// nextBackoff doubles the current pause, capped at MaxBackoff, unless the child
// managed a stable run — in which case the next failure starts over at initial.
func nextBackoff(current, initial, ran time.Duration) time.Duration {
	if ran >= stableRun {
		return initial
	}
	next := current * 2
	if next > MaxBackoff {
		next = MaxBackoff
	}
	return next
}

func (s *Supervisor) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)
	backoff := s.cfg.InitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		ran := time.Since(started)
		s.record(err)
		if ran >= stableRun {
			backoff = s.cfg.InitialBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff, s.cfg.InitialBackoff, ran)
	}
}

func (s *Supervisor) record(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
}

func (s *Supervisor) setAlive(alive bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = alive
	if alive {
		s.starts++
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	logw, err := openBoundedLog(s.cfg.LogPath, s.cfg.MaxLogBytes, s.cfg.UID, s.cfg.GID)
	if err != nil {
		return fmt.Errorf("supervisor %q: opening log: %w", s.cfg.Name, err)
	}
	defer logw.Close() //nolint:errcheck

	//nolint:gosec // the executable is the agent's own binary and the args are rendered from a validated spec
	cmd := exec.Command(s.cfg.Executable, s.cfg.Args...)
	cmd.Dir = s.cfg.Dir
	cmd.Env = append([]string(nil), s.cfg.Env...)
	cmd.Stdout = logw
	cmd.Stderr = logw
	// The child must never share the agent's stdin: a harness that reads it
	// would consume the agent's own console.
	cmd.Stdin = nil
	if err := configureChild(cmd, s.cfg.UID, s.cfg.GID); err != nil {
		return fmt.Errorf("supervisor %q: %w", s.cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervisor %q: starting child: %w", s.cfg.Name, err)
	}
	s.setAlive(true)
	defer s.setAlive(false)

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		if err != nil {
			return fmt.Errorf("supervisor %q: child exited: %w", s.cfg.Name, err)
		}
		return fmt.Errorf("supervisor %q: child exited unexpectedly with status 0", s.cfg.Name)
	case <-ctx.Done():
		stopChild(cmd.Process, waited, s.cfg.StopGrace)
		return ctx.Err()
	}
}

// stopChild signals the child's whole process group, then escalates. The group
// matters: the runner starts Codex, which starts git; a SIGTERM to the direct
// child alone leaves those running and holding the add-on's worktrees.
func stopChild(proc *os.Process, waited <-chan error, grace time.Duration) {
	if proc == nil {
		return
	}
	if err := signalProcessGroup(proc.Pid, syscall.SIGTERM); err != nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	select {
	case <-waited:
		return
	case <-time.After(grace):
	}
	if err := signalProcessGroup(proc.Pid, syscall.SIGKILL); err != nil {
		_ = proc.Kill()
	}
	<-waited
}

// boundedLog is an owner-only append log that truncates itself instead of
// growing without limit.
type boundedLog struct {
	mu   sync.Mutex
	f    *os.File
	size int64
	max  int64
}

func openBoundedLog(path string, max int64, uid, gid int) (*boundedLog, error) {
	//nolint:gosec // the path is derived from the add-on's own state directory
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if uid >= 0 && gid >= 0 {
		if err := f.Chown(uid, gid); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &boundedLog{f: f, size: info.Size(), max: max}, nil
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A single write larger than the whole budget keeps its tail: the end of a
	// panic is more useful than its beginning.
	if int64(len(p)) > b.max {
		p = p[int64(len(p))-b.max:]
	}
	if b.size+int64(len(p)) > b.max {
		if err := b.f.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := b.f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		b.size = 0
	}
	n, err := b.f.Write(p)
	b.size += int64(n)
	return n, err
}

func (b *boundedLog) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.f.Close()
}
