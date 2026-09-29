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

package harnessplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/agent/runner/supervisor"
	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/util/safeio"
)

// tokenBytes is the size of the generated runner bearer token. 32 bytes of
// crypto/rand, hex-encoded, is 64 characters — well inside the runner's 4096
// character limit and far beyond guessing.
const tokenBytes = 32

// InheritUID marks an account as "the agent's own", for a non-root agent that
// launches the child as itself. Re-exported so callers do not have to import
// the supervisor package just to say so.
const InheritUID = supervisor.InheritUID

// identifierPattern mirrors pkg/runner's unexported identifierPattern. The
// runner rejects a bad ID at load time; rejecting it here turns a crash loop
// into a readable reason on the harness status.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// RunAsAccount is the local account the supervised runner runs as.
type RunAsAccount struct {
	// Home is the account's home directory; runner state lives under it.
	Home string
	// UID/GID are the account's ids, or InheritUID when the agent is already
	// non-root and the child runs as the agent's own account.
	UID int
	GID int
}

// Inherited reports whether the child runs as the agent's own account.
func (a RunAsAccount) Inherited() bool { return a.UID < 0 }

// chown returns the uid/gid to stamp on files, or (0, 0) meaning "leave
// ownership alone" when the agent already IS the account.
func (a RunAsAccount) chown() (int, int) {
	if a.Inherited() {
		return 0, 0
	}
	return a.UID, a.GID
}

// childConfig is what every supervised runner shares. One value per Manager.
type childConfig struct {
	EdgeName       string
	Executable     string
	Account        RunAsAccount
	ProbeDeadline  time.Duration
	ProbeTimeout   time.Duration
	InitialBackoff time.Duration
	HTTPClient     *http.Client
}

// child supervises one `railgrid runner run --harness <name>` process.
type child struct {
	harness string
	port    int
	cfg     childConfig

	mu sync.Mutex
	// bearer is generated once, on the first reconcile that starts this
	// harness, and never rotated afterwards: rotating it on a re-reconcile
	// would break an in-flight attempt for no reason.
	bearer string
	sup    *supervisor.Supervisor
	// hash covers everything whose change requires a restart: the arguments and
	// the rendered enrollment. No credential goes into it because no credential
	// is on this host.
	hash string
	// observed is the last capabilities probe. It is what the heartbeat and the
	// advertised Service report, so a probe that fails clears readiness rather
	// than leaving a stale "ready".
	observed observation
	// permissionMode and allowedTools are the machine's ceiling as of the last
	// reconcile. They are on the child rather than in childConfig because
	// spec.harness can change them while the machine runs, and the restart hash
	// covers them so a change actually takes effect.
	permissionMode string
	allowedTools   []string
}

// permissionLimits is the machine's ceiling, passed to each reconcile.
type permissionLimits struct {
	mode         string
	allowedTools []string
}

// observation is the readable part of a capabilities response.
type observation struct {
	Version string
	Ready   bool
	Reasons []string
}

func newChild(harnessName string, port int, cfg childConfig) *child {
	return &child{harness: harnessName, port: port, cfg: cfg}
}

// stateDir is <runner home>/.railgrid/runner/<harness>. Everything this harness
// owns lives under it, and nothing outside it is ever written.
func (c *child) stateDir() string {
	return filepath.Join(c.cfg.Account.Home, ".railgrid", "runner", c.harness)
}

func (c *child) tokenPath() string   { return filepath.Join(c.stateDir(), "token") }
func (c *child) configPath() string  { return filepath.Join(c.stateDir(), "runner.json") }
func (c *child) harnessHome() string { return filepath.Join(c.stateDir(), c.harness+"-home") }
func (c *child) runnerState() string { return filepath.Join(c.stateDir(), "state") }
func (c *child) logPath() string     { return filepath.Join(c.stateDir(), "runner.log") }

// runnerID is the identity the runner advertises: "<edge>-<harness>" is unique
// per tenant workspace and readable in a capabilities response.
func (c *child) runnerID() string { return c.cfg.EdgeName + "-" + c.harness }

// ensure converges the host and (re)starts the child. Ordering matters: state
// directories, then the token, then the enrollment, and only then the process —
// a runner must never come up before the files it loads at startup exist.
//
// binary is the detected executable, or empty when the harness is enabled but
// not installed; the caller refuses that case before calling ensure.
func (c *child) ensure(ctx context.Context, binary string, limits permissionLimits) error {
	c.permissionMode, c.allowedTools = limits.mode, limits.allowedTools
	uid, gid := c.cfg.Account.chown()
	for _, dir := range []string{c.stateDir(), c.harnessHome(), c.runnerState()} {
		if err := safeio.EnsureDir(dir, 0700, uid, gid); err != nil {
			return fmt.Errorf("preparing %s: %w", dir, err)
		}
	}
	if err := c.ensureToken(uid, gid); err != nil {
		return fmt.Errorf("preparing the runner token: %w", err)
	}
	config, err := c.renderConfig()
	if err != nil {
		return err
	}
	if err := safeio.WriteFile(c.configPath(), config, 0600, uid, gid); err != nil {
		return fmt.Errorf("writing runner.json: %w", err)
	}
	return c.ensureSupervisor(ctx, c.childArgs(binary), config)
}

// stop terminates the child and keeps everything on disk. The token, the
// enrollment, the harness home, the runner's state and its clones all survive,
// so switching a harness off and on again resumes the same sessions instead of
// destroying a developer's work.
func (c *child) stop(ctx context.Context) error {
	c.mu.Lock()
	sup := c.sup
	c.sup = nil
	c.hash = ""
	c.observed = observation{}
	c.mu.Unlock()
	if sup == nil {
		return nil
	}
	// The supervisor signals the child's whole process GROUP with SIGTERM and
	// waits out its grace period before escalating, which is the same graceful
	// drain an agent upgrade gives it: the runner persists its receipt and
	// parks the attempt rather than losing it.
	return sup.Stop(ctx)
}

// ensureToken generates the bearer exactly once and remembers it for the
// /svc proxy, which injects it so the token never leaves this host.
func (c *child) ensureToken(uid, gid int) error {
	existing, err := os.ReadFile(c.tokenPath()) //nolint:gosec // inside the runner's own 0700 state directory
	if err == nil {
		if token := strings.TrimSpace(string(existing)); token != "" {
			c.mu.Lock()
			c.bearer = token
			c.mu.Unlock()
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	if err := safeio.WriteFile(c.tokenPath(), []byte(token+"\n"), 0600, uid, gid); err != nil {
		return err
	}
	c.mu.Lock()
	c.bearer = token
	c.mu.Unlock()
	return nil
}

// token returns the bearer, or "" before the first successful ensure.
func (c *child) token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bearer
}

// renderConfig turns the harness selection into the runner's JSON enrollment.
// The listener is pinned to loopback here, and this is the only place the
// address is constructed: a runner that bound anything else would be reachable
// without the tunnel's authorization.
func (c *child) renderConfig() ([]byte, error) {
	id := c.runnerID()
	if !identifierPattern.MatchString(id) {
		return nil, fmt.Errorf("runner ID %q (edge name + harness) is not a valid runner identifier", id)
	}
	cfg := runner.Config{
		ProtocolVersion: runner.ProtocolVersion,
		RunnerID:        id,
		Listen:          net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port)),
		StateDir:        c.runnerState(),
		TokenFile:       c.tokenPath(),
		MaximumCapacity: 1,
		// No repositories: the caller names a clone URL with each attempt and
		// the runner keeps its own clone under its state directory, so nothing
		// has to be staged on this host.
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// childArgs is the `railgrid runner run` command line. Paths are passed
// explicitly rather than relying on the runner's defaults so this package owns
// every location it touches, and the detected executable is passed so the child
// launches the same binary detection found — it must not depend on the service
// account's PATH agreeing with the agent's.
//
// There is deliberately no credential flag: the caller sends its own harness
// credential with each attempt.
func (c *child) childArgs(binary string) []string {
	args := []string{
		"runner", "run",
		"--config", c.configPath(),
		"--state-dir", c.runnerState(),
		"--token-file", c.tokenPath(),
		"--harness", c.harness,
	}
	switch c.harness {
	case HarnessClaude:
		args = append(args, "--claude-home", c.harnessHome())
		if binary != "" {
			args = append(args, "--claude-binary", binary)
		}
		// The machine's ceiling. Without these the runner took the adapter's
		// default and denied anything that would prompt, with no way to ask —
		// which read, to whoever was using the agent, as "web access is blocked
		// in this session".
		args = append(args, "--claude-permission-mode", c.permissionMode)
		for _, tool := range c.allowedTools {
			if tool = strings.TrimSpace(tool); tool != "" {
				args = append(args, "--claude-allowed-tool", tool)
			}
		}
	case HarnessCodex:
		args = append(args, "--codex-home", c.harnessHome())
		if binary != "" {
			args = append(args, "--codex-binary", binary)
		}
	}
	return args
}

// ensureSupervisor (re)starts the child when the arguments or the enrollment
// changed, and is otherwise a no-op, so a reconcile every 60 seconds does not
// bounce a working runner.
func (c *child) ensureSupervisor(ctx context.Context, args []string, config []byte) error {
	sum := sha256.New()
	for _, a := range args {
		sum.Write([]byte(a))
		sum.Write([]byte{0})
	}
	sum.Write(config)
	hash := hex.EncodeToString(sum.Sum(nil))

	c.mu.Lock()
	unchanged := c.sup != nil && c.hash == hash
	current := c.sup
	c.mu.Unlock()
	if unchanged {
		current.Start(ctx) // no-op when already running; restarts nothing
		return nil
	}
	if current != nil {
		if err := current.Stop(ctx); err != nil {
			klog.FromContext(ctx).Error(err, "stopping a runner before restart", "harness", c.harness)
		}
	} else {
		// First child of this agent process: whatever holds the runner's state
		// lock now is a runner a previous agent left behind, and it would keep
		// the port with a stale build and configuration while this agent's own
		// child crash-loops on the lock.
		lockPath := filepath.Join(c.runnerState(), "runner.lock")
		if pid, err := supervisor.ReclaimStaleHolder(lockPath, supervisor.DefaultStopGrace); err != nil {
			klog.FromContext(ctx).Error(err, "reclaiming the runner state lock", "harness", c.harness)
		} else if pid != 0 {
			klog.FromContext(ctx).Info("terminated a runner left behind by a previous agent", "harness", c.harness, "pid", pid)
		}
	}

	sup, err := supervisor.New(supervisor.Config{
		Name:       "runner/" + c.harness,
		Executable: c.cfg.Executable,
		Args:       args,
		Dir:        c.stateDir(),
		// Built from scratch: the agent's hub token, its kubeconfig path and any
		// API keys in its environment must never reach a code-execution child.
		Env:            supervisor.ChildEnv(c.cfg.Account.Home, ""),
		UID:            c.cfg.Account.UID,
		GID:            c.cfg.Account.GID,
		LogPath:        c.logPath(),
		InitialBackoff: c.cfg.InitialBackoff,
	})
	if err != nil {
		return err
	}
	sup.Start(ctx)

	c.mu.Lock()
	c.sup = sup
	c.hash = hash
	c.mu.Unlock()
	return nil
}

// supervised reports whether this agent has a child for the harness that is
// running, or has not yet had its first chance to start. A child that started
// and exited (a crash loop on the state lock, say) is neither, and must not be
// advertised as a service.
func (c *child) supervised() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sup == nil {
		return false
	}
	return c.sup.Alive() || (c.sup.Starts() == 0 && c.sup.LastError() == nil)
}

// lastError is the most recent child failure, for a reason on the status.
func (c *child) lastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sup == nil {
		return nil
	}
	return c.sup.LastError()
}

// snapshot returns the last probe result.
func (c *child) snapshot() observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observed
}

// probe asks the loopback runner for its capabilities with the same bearer the
// /svc proxy injects, and records what it said. Readiness is a live protocol
// answer, not merely a live process: a runner that is up but not serving
// runner/v1 is not a harness anyone can use.
func (c *child) probe(ctx context.Context) error {
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port)) + "/runner/v1/capabilities"
	token := c.token()
	deadline := time.Now().Add(c.cfg.ProbeDeadline)
	var lastErr error
	for {
		observed, err := c.probeOnce(ctx, url, token)
		if err == nil {
			c.mu.Lock()
			c.observed = observed
			c.mu.Unlock()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) || ctx.Err() != nil {
			c.mu.Lock()
			c.observed = observation{}
			c.mu.Unlock()
			return lastErr
		}
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.observed = observation{}
			c.mu.Unlock()
			return lastErr
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (c *child) probeOnce(ctx context.Context, url, token string) (observation, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return observation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return observation{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return observation{}, fmt.Errorf("capabilities probe returned HTTP %d", resp.StatusCode)
	}
	var caps capabilitiesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCapabilitiesBytes)).Decode(&caps); err != nil {
		return observation{}, fmt.Errorf("decoding capabilities: %w", err)
	}
	if caps.ProtocolVersion != runner.ProtocolVersion {
		return observation{}, fmt.Errorf("capabilities reported protocol %q, want %q", caps.ProtocolVersion, runner.ProtocolVersion)
	}
	// One runner process serves one harness — this child was launched with
	// --harness <name> — so the single entry is ours. It is deliberately not
	// matched by name: the capability carries the HARNESS's own name
	// ("claude-code"), while spec.harness and this package select it by
	// ("claude"), and requiring the two to be equal would report every Claude
	// Code runner as never ready.
	if len(caps.Harnesses) == 0 {
		return observation{}, fmt.Errorf("runner %s advertises no harness", c.runnerID())
	}
	h := caps.Harnesses[0]
	return observation{
		Version: truncateField(h.Version),
		Ready:   h.Ready,
		Reasons: truncateReasons(h.Reasons),
	}, nil
}

// capabilitiesResponse is the subset of the runner's capabilities document the
// health probe reads.
type capabilitiesResponse struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Version         string                `json:"version"`
	RunnerID        string                `json:"runnerID"`
	Harnesses       []harnessCapabilities `json:"harnesses"`
}

// harnessCapabilities mirrors runner.HarnessCapability.
type harnessCapabilities struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

// maxCapabilitiesBytes bounds the probe response. The runner is our own
// process, but it is still a network peer.
const maxCapabilitiesBytes = 1 << 20

// maxHarnessReasons / maxHarnessField match the edge API's caps on
// status.harnesses, so a long reason is trimmed here rather than rejected by
// admission on every heartbeat.
const (
	maxHarnessReasons = 16
	maxHarnessField   = 64
	maxReasonBytes    = 2048
)

func truncateField(value string) string {
	if len(value) <= maxHarnessField {
		return value
	}
	return value[:maxHarnessField]
}

func truncateReasons(reasons []string) []string {
	if len(reasons) > maxHarnessReasons {
		reasons = reasons[:maxHarnessReasons]
	}
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if len(reason) > maxReasonBytes {
			reason = reason[:maxReasonBytes]
		}
		out = append(out, reason)
	}
	return out
}
