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

package addons

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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/railgrid/railgrid/pkg/agent/addons/supervisor"
	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/harness/claude"
	"github.com/railgrid/railgrid/pkg/util/safeio"
)

const (
	// TokenSecretSuffix is appended to the Addon name to form the Secret that
	// carries the runner's bearer token. The provider's add-on controller waits
	// for this Secret before it publishes a Service, and points the Service's
	// authSecretRef at it.
	TokenSecretSuffix = "-runner-token"
	// TokenSecretKey is the Secret key holding the bearer token. It matches the
	// key an edges Service's authSecretRef is read from.
	TokenSecretKey = "token"
	// CodexAuthKey is the Secret key holding the Codex login session file.
	CodexAuthKey = "auth.json"
	// TokenSecretNamespace is where the token Secret is published. The agent's
	// ClusterRole grants core secrets get/create/update, and `default` is the
	// namespace every tenant workspace has.
	TokenSecretNamespace = "default"
	// LabelAddon ties a published object back to its Addon.
	LabelAddon = edgesGroup + "/addon"
	// LabelEdge is the shared edge correlation label.
	LabelEdge = edgesGroup + "/edge"
	// AddonKind is the owner kind stamped on published objects.
	AddonKind = "Addon"
)

// tokenBytes is the size of the generated runner bearer token. 32 bytes of
// crypto/rand, hex-encoded, is 64 characters — well inside the runner's 4096
// character limit and far beyond guessing.
const tokenBytes = 32

// defaultRunnerPort matches the API default and the runner's own default.
const defaultRunnerPort = 8787

// identifierPattern mirrors pkg/runner's unexported identifierPattern. The
// runner rejects a bad ID at load time; rejecting it here turns a crash loop
// into a Configured=False condition with a readable message.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// InheritUID marks an account as "the agent's own", for a non-root agent that
// launches the child as itself. Re-exported so callers do not have to import
// the supervisor package just to say so.
const InheritUID = supervisor.InheritUID

// RunAsAccount is the local account the supervised child runs as.
type RunAsAccount struct {
	// Home is the account's home directory; the add-on's state lives under it.
	Home string
	// UID/GID are the account's ids, or supervisor.InheritUID when the agent is
	// already non-root and the child runs as the agent's own account.
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

// RunnerOptions configures the runner add-on factory.
type RunnerOptions struct {
	// EdgeName is this edge's name; it is half of the runner's identity.
	EdgeName string
	// Executable is the agent's own binary, supervised as `<exe> runner run`.
	Executable string
	// Account is the non-root account the runner runs as.
	Account RunAsAccount
	// Kube is a clientset scoped to the tenant workspace, used to read the
	// Codex auth Secret and to publish the runner's token Secret.
	Kube kubernetes.Interface
	// ProbeDeadline bounds how long a reconcile waits for a freshly started
	// runner to answer its capabilities probe before reporting Installing.
	ProbeDeadline time.Duration
	// ProbeTimeout bounds a single probe request.
	ProbeTimeout time.Duration
	// InitialBackoff is handed to the supervisor; tests shorten it.
	InitialBackoff time.Duration
	// HTTPClient probes the loopback runner; nil builds a loopback-only client.
	HTTPClient *http.Client
}

const (
	defaultProbeDeadline = 3 * time.Second
	defaultProbeTimeout  = 2 * time.Second
)

// NewRunnerFactory returns a Factory the manager registers for TypeRunner.
func NewRunnerFactory(opts RunnerOptions) (Factory, error) {
	if opts.EdgeName == "" {
		return nil, fmt.Errorf("runner add-on: edge name is required")
	}
	if opts.Executable == "" {
		return nil, fmt.Errorf("runner add-on: executable path is required")
	}
	if opts.Account.Home == "" || !filepath.IsAbs(opts.Account.Home) || opts.Account.Home == "/" {
		return nil, fmt.Errorf("runner add-on: an absolute non-root home is required, got %q", opts.Account.Home)
	}
	if !opts.Account.Inherited() && (opts.Account.UID == 0 || opts.Account.GID == 0) {
		return nil, fmt.Errorf("runner add-on: refusing to run the runner as uid 0")
	}
	if opts.Kube == nil {
		return nil, fmt.Errorf("runner add-on: a tenant-workspace clientset is required")
	}
	if opts.ProbeDeadline <= 0 {
		opts.ProbeDeadline = defaultProbeDeadline
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = defaultProbeTimeout
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: opts.ProbeTimeout}
	}
	return func(name string) (Addon, error) {
		if err := validateName(name); err != nil {
			return nil, err
		}
		return &runnerAddon{name: name, opts: opts}, nil
	}, nil
}

// runnerAddon supervises one `railgrid runner run` process for one Addon.
type runnerAddon struct {
	name string
	opts RunnerOptions

	mu  sync.Mutex
	sup *supervisor.Supervisor
	// hash is the content hash of everything that would require a child
	// restart: the arguments, the rendered runner.json and the credential.
	hash string
	// claudeKind is which credential the last Claude materialization wrote, so
	// childArgs can tell the runner how to inject it.
	claudeKind claude.CredentialKind
}

func (r *runnerAddon) Type() string { return TypeRunner }

// stateDir is <home>/.railgrid/addons/runner/<addon>. Everything the add-on
// owns lives under it, and nothing outside it is ever written.
func (r *runnerAddon) stateDir() string {
	return filepath.Join(r.opts.Account.Home, ".railgrid", "addons", TypeRunner, r.name)
}

func (r *runnerAddon) tokenPath() string     { return filepath.Join(r.stateDir(), "token") }
func (r *runnerAddon) configPath() string    { return filepath.Join(r.stateDir(), "runner.json") }
func (r *runnerAddon) codexHome() string     { return filepath.Join(r.stateDir(), "codex-home") }
func (r *runnerAddon) codexAuthPath() string { return filepath.Join(r.codexHome(), CodexAuthKey) }
func (r *runnerAddon) claudeHome() string    { return filepath.Join(r.stateDir(), "claude-home") }
func (r *runnerAddon) runnerState() string   { return filepath.Join(r.stateDir(), "state") }
func (r *runnerAddon) logPath() string       { return filepath.Join(r.stateDir(), "runner.log") }

// claudeCredentialPath holds the raw Claude Code credential. It sits beside the
// homes rather than inside claude-home, so the adapter's "no unexpected entries
// in the worker home" rule stays simple.
func (r *runnerAddon) claudeCredentialPath() string {
	return filepath.Join(r.stateDir(), "claude-credential")
}

// runnerID is the identity the runner advertises. "<edge>-<addon>" is unique
// per tenant workspace and readable in a capabilities response.
func (r *runnerAddon) runnerID() string { return r.opts.EdgeName + "-" + r.name }

// Reconcile converges the host to spec. Ordering matters: state, then token,
// then credentials, then configuration, then the published Secret, and only
// then the process. Nothing starts before the Codex session is on disk, so a
// runner never comes up in a state where it would accept an attempt it cannot
// execute.
func (r *runnerAddon) Reconcile(ctx context.Context, spec Spec) (Status, error) {
	logger := klog.FromContext(ctx).WithValues("addon", r.name)
	if spec.Runner == nil {
		return Status{Phase: PhaseBlocked}, fmt.Errorf("spec.runner is required when spec.type is %q", TypeRunner)
	}

	uid, gid := r.opts.Account.chown()
	// Both harness homes are created regardless of the selected harness: they
	// are empty 0700 directories, and keeping them means switching harness
	// back and forth never loses session state.
	for _, dir := range []string{r.stateDir(), r.codexHome(), r.claudeHome(), r.runnerState()} {
		if err := safeio.EnsureDir(dir, 0700, uid, gid); err != nil {
			return Status{Phase: PhaseDegraded}, fmt.Errorf("preparing %s: %w", dir, err)
		}
	}

	token, err := r.ensureToken(uid, gid)
	if err != nil {
		return Status{Phase: PhaseDegraded}, fmt.Errorf("preparing the runner token: %w", err)
	}

	// The harness credential. Absent, this add-on is configured but unusable,
	// so it is reported rather than started: a runner without a working harness
	// would accept attempts and fail every one of them.
	material, refusal, err := r.materializeHarness(ctx, spec.Runner, uid, gid)
	if err != nil {
		return Status{Phase: PhaseDegraded}, err
	}
	if refusal != nil {
		return r.stoppedStatus(ctx, refusal.reason, refusal.message), nil
	}

	config, err := r.renderConfig(spec.Runner)
	if err != nil {
		msg := err.Error()
		return Status{
			Phase:   PhaseBlocked,
			Message: msg,
			Conditions: []Condition{
				{Type: ConditionConfigured, Status: metav1.ConditionFalse, Reason: ReasonConfigError, Message: msg},
				{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonConfigError, Message: "not started"},
			},
		}, nil
	}
	if err := safeio.WriteFile(r.configPath(), config, 0600, uid, gid); err != nil {
		return Status{Phase: PhaseDegraded}, fmt.Errorf("writing runner.json: %w", err)
	}

	// Publishing the token is what lets the provider build a Service that can
	// actually talk to this runner. It is owned by the Addon, so deleting the
	// Addon takes the credential with it.
	if err := r.publishToken(ctx, spec, token); err != nil {
		return Status{Phase: PhaseDegraded}, fmt.Errorf("publishing the runner token Secret: %w", err)
	}

	args := r.childArgs(spec.Runner)
	// The credential digest, not the credential, feeds the restart hash: the
	// child restarts when and only when the Secret's CONTENT changed, so a
	// rotated credential takes effect on the next resync without a bounce on
	// every reconcile.
	if err := r.ensureSupervisor(ctx, args, config, material.digest); err != nil {
		return Status{Phase: PhaseDegraded}, err
	}

	configured := Condition{
		Type: ConditionConfigured, Status: metav1.ConditionTrue, Reason: ReasonConfigured,
		Message: material.configuredMessage,
	}

	port := runnerPort(spec.Runner)
	probed, probeErr := r.probe(ctx, port, token)
	if probeErr == nil {
		return Status{
			Phase:   PhaseRunning,
			Version: probed.Version,
			Harness: probed.Harness,
			Message: fmt.Sprintf("runner %s answering on 127.0.0.1:%d", r.runnerID(), port),
			Conditions: []Condition{
				configured,
				{Type: ConditionRunning, Status: metav1.ConditionTrue, Reason: ReasonProbeOK, Message: "capabilities probe returned " + runner.ProtocolVersion},
			},
		}, nil
	}

	r.mu.Lock()
	sup := r.sup
	r.mu.Unlock()
	if sup != nil && sup.Alive() {
		logger.V(2).Info("runner not answering yet", "err", probeErr.Error())
		return Status{
			Phase:   PhaseInstalling,
			Message: "waiting for the runner to answer its capabilities probe: " + probeErr.Error(),
			Conditions: []Condition{
				configured,
				{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonStarting, Message: probeErr.Error()},
			},
		}, nil
	}
	msg := probeErr.Error()
	if sup != nil {
		if last := sup.LastError(); last != nil {
			msg = last.Error()
		}
	}
	return Status{
		Phase:   PhaseDegraded,
		Message: msg,
		Conditions: []Condition{
			configured,
			{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonProbeFailed, Message: msg},
		},
	}, nil
}

// Stop tears down the child. The state directory, the token, the Codex session
// and — most importantly — the enrolled repositories are left untouched, so a
// pause or a delete never destroys work on the host.
func (r *runnerAddon) Stop(ctx context.Context) error {
	r.mu.Lock()
	sup := r.sup
	r.sup = nil
	r.hash = ""
	r.mu.Unlock()
	if sup == nil {
		return nil
	}
	return sup.Stop(ctx)
}

// stoppedStatus stops the child and reports a Configured=False condition. Used
// for the "declared but not usable" cases where starting would be worse than
// not starting.
func (r *runnerAddon) stoppedStatus(ctx context.Context, reason, message string) Status {
	if err := r.Stop(ctx); err != nil {
		klog.FromContext(ctx).Error(err, "stopping runner add-on", "addon", r.name)
	}
	return Status{
		Phase:   PhaseDegraded,
		Message: message,
		Conditions: []Condition{
			{Type: ConditionConfigured, Status: metav1.ConditionFalse, Reason: reason, Message: message},
			{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonNotRunning, Message: "not started"},
		},
	}
}

func codexAuthRef(spec *RunnerSpec) *SecretRef {
	if spec.Codex == nil || spec.Codex.AuthSecretRef == nil || strings.TrimSpace(spec.Codex.AuthSecretRef.Name) == "" {
		return nil
	}
	return spec.Codex.AuthSecretRef
}

func claudeAuthRef(spec *RunnerSpec) *SecretRef {
	if spec.Claude == nil || spec.Claude.AuthSecretRef == nil || strings.TrimSpace(spec.Claude.AuthSecretRef.Name) == "" {
		return nil
	}
	return spec.Claude.AuthSecretRef
}

// harnessMaterial is what the selected harness needs on disk before the child
// may start.
type harnessMaterial struct {
	// digest is a content hash of the credential. It feeds the supervisor's
	// restart hash so the child restarts exactly when the credential changed —
	// never the credential itself, which must not reach a hash that is logged.
	digest []byte
	// configuredMessage describes what is in place, for Configured=True.
	configuredMessage string
}

// harnessRefusal is a "declared but not usable" outcome: reported on status,
// nothing started, nothing published.
type harnessRefusal struct {
	reason  string
	message string
}

// materializeHarness writes the selected harness's credential and removes the
// other harness's, so switching spec.runner.harness never leaves a credential
// for a harness this runner is no longer configured to use. The homes are kept:
// they hold session state, and losing it would break resume.
func (r *runnerAddon) materializeHarness(ctx context.Context, spec *RunnerSpec, uid, gid int) (harnessMaterial, *harnessRefusal, error) {
	switch spec.HarnessName() {
	case HarnessClaude:
		material, refusal, err := r.materializeClaude(ctx, spec, uid, gid)
		if err != nil || refusal != nil {
			return material, refusal, err
		}
		if err := removeIfPresent(r.codexAuthPath()); err != nil {
			return material, nil, fmt.Errorf("removing the stale Codex session: %w", err)
		}
		return material, nil, nil
	default:
		material, refusal, err := r.materializeCodex(ctx, spec, uid, gid)
		if err != nil || refusal != nil {
			return material, refusal, err
		}
		if err := removeIfPresent(r.claudeCredentialPath()); err != nil {
			return material, nil, fmt.Errorf("removing the stale Claude Code credential: %w", err)
		}
		return material, nil, nil
	}
}

func (r *runnerAddon) materializeCodex(ctx context.Context, spec *RunnerSpec, uid, gid int) (harnessMaterial, *harnessRefusal, error) {
	authRef := codexAuthRef(spec)
	if authRef == nil {
		return harnessMaterial{}, &harnessRefusal{
			reason: ReasonCodexAuthMissing,
			message: "spec.runner.codex.authSecretRef is required: the runner needs a Codex login session (Secret key " +
				CodexAuthKey + "). There is no API-key alternative — the Codex harness strips API-key environment variables.",
		}, nil
	}
	secret, err := r.readSecret(ctx, authRef)
	if err != nil {
		return harnessMaterial{}, &harnessRefusal{reason: ReasonCodexAuthMissing, message: err.Error()}, nil
	}
	data := secret.Data[CodexAuthKey]
	if len(data) == 0 {
		return harnessMaterial{}, &harnessRefusal{
			reason:  ReasonCodexAuthMissing,
			message: fmt.Sprintf("Codex auth Secret %s/%s has no %q key", secret.Namespace, secret.Name, CodexAuthKey),
		}, nil
	}
	if err := safeio.WriteFile(r.codexAuthPath(), data, 0600, uid, gid); err != nil {
		return harnessMaterial{}, nil, fmt.Errorf("writing the Codex session: %w", err)
	}
	return harnessMaterial{
		digest:            credentialDigest(HarnessCodex, CodexAuthKey, data),
		configuredMessage: "runner.json, bearer token and Codex session are in place",
	}, nil, nil
}

func (r *runnerAddon) materializeClaude(ctx context.Context, spec *RunnerSpec, uid, gid int) (harnessMaterial, *harnessRefusal, error) {
	authRef := claudeAuthRef(spec)
	if authRef == nil {
		return harnessMaterial{}, &harnessRefusal{
			reason: ReasonClaudeAuthMissing,
			message: "spec.runner.claude.authSecretRef is required: the runner needs exactly one of the Secret keys " +
				ClaudeOAuthTokenKey + " (from `claude setup-token`) or " + ClaudeAPIKeyKey + ".",
		}, nil
	}
	secret, err := r.readSecret(ctx, authRef)
	if err != nil {
		return harnessMaterial{}, &harnessRefusal{reason: ReasonClaudeAuthMissing, message: err.Error()}, nil
	}
	oauth := strings.TrimSpace(string(secret.Data[ClaudeOAuthTokenKey]))
	apiKey := strings.TrimSpace(string(secret.Data[ClaudeAPIKeyKey]))
	switch {
	case oauth == "" && apiKey == "":
		return harnessMaterial{}, &harnessRefusal{
			reason: ReasonClaudeAuthMissing,
			message: fmt.Sprintf("Claude Code auth Secret %s/%s has neither a %q nor an %q key",
				secret.Namespace, secret.Name, ClaudeOAuthTokenKey, ClaudeAPIKeyKey),
		}, nil
	case oauth != "" && apiKey != "":
		// Not a guess the agent will make on the tenant's behalf: the two keys
		// are different identities with different billing and different scope.
		return harnessMaterial{}, &harnessRefusal{
			reason: ReasonClaudeAuthInvalid,
			message: fmt.Sprintf("Claude Code auth Secret %s/%s carries both %q and %q; set exactly one",
				secret.Namespace, secret.Name, ClaudeOAuthTokenKey, ClaudeAPIKeyKey),
		}, nil
	}

	value, kind, key := oauth, claude.CredentialOAuthToken, ClaudeOAuthTokenKey
	if oauth == "" {
		value, kind, key = apiKey, claude.CredentialAPIKey, ClaudeAPIKeyKey
	}
	// A credential with embedded whitespace cannot become an environment value
	// and would silently authenticate as nobody.
	if strings.ContainsAny(value, "\r\n\x00") {
		return harnessMaterial{}, &harnessRefusal{
			reason:  ReasonClaudeAuthInvalid,
			message: fmt.Sprintf("the %q value in Secret %s/%s contains invalid whitespace", key, secret.Namespace, secret.Name),
		}, nil
	}
	if err := safeio.WriteFile(r.claudeCredentialPath(), []byte(value), 0600, uid, gid); err != nil {
		return harnessMaterial{}, nil, fmt.Errorf("writing the Claude Code credential: %w", err)
	}
	r.mu.Lock()
	r.claudeKind = kind
	r.mu.Unlock()
	return harnessMaterial{
		digest: credentialDigest(HarnessClaude, key, []byte(value)),
		configuredMessage: fmt.Sprintf("runner.json, bearer token and a Claude Code %s credential are in place",
			kind),
	}, nil, nil
}

// credentialDigest hashes a credential together with the harness and key it
// belongs to, so swapping "the same bytes under a different key" still counts
// as a change and restarts the child.
func credentialDigest(harnessName, key string, value []byte) []byte {
	sum := sha256.New()
	sum.Write([]byte(harnessName))
	sum.Write([]byte{0})
	sum.Write([]byte(key))
	sum.Write([]byte{0})
	sum.Write(value)
	return sum.Sum(nil)
}

// removeIfPresent deletes path, tolerating its absence. Used to clear the
// credential of a harness this add-on no longer runs.
func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ensureToken returns the add-on's bearer token, generating it exactly once.
// Re-reconciles reuse the existing value: rotating it on every pass would
// invalidate the published Secret the provider's Service points at, and would
// break an in-flight attempt for no reason.
func (r *runnerAddon) ensureToken(uid, gid int) (string, error) {
	//nolint:gosec // the path is inside the add-on's own 0700 state directory
	existing, err := os.ReadFile(r.tokenPath())
	if err == nil {
		if token := strings.TrimSpace(string(existing)); token != "" {
			return token, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := safeio.WriteFile(r.tokenPath(), []byte(token+"\n"), 0600, uid, gid); err != nil {
		return "", err
	}
	return token, nil
}

// readSecret fetches a referenced credential Secret from the tenant workspace
// with the agent's own credential. It is called on every reconcile — never
// cached — so a rotated credential is picked up by the next resync. It never
// logs the contents.
func (r *runnerAddon) readSecret(ctx context.Context, ref *SecretRef) (*corev1.Secret, error) {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = TokenSecretNamespace
	}
	secret, err := r.opts.Kube.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("auth Secret %s/%s does not exist", namespace, ref.Name)
		}
		return nil, fmt.Errorf("reading auth Secret %s/%s: %w", namespace, ref.Name, err)
	}
	// Namespace is defaulted above, so stamp it back for the messages that
	// quote it.
	secret.Namespace = namespace
	return secret, nil
}

// publishToken creates or updates the Secret the provider's add-on controller
// waits for. The ownerReference makes deletion of the Addon delete the
// credential; BlockOwnerDeletion is deliberately NOT set, because the agent has
// no update access to the Addon's finalizers.
func (r *runnerAddon) publishToken(ctx context.Context, spec Spec, token string) error {
	if err := r.ensureNamespace(ctx, TokenSecretNamespace); err != nil {
		return err
	}
	name := r.name + TokenSecretSuffix
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TokenSecretNamespace,
			Labels: map[string]string{
				LabelEdge:  r.opts.EdgeName,
				LabelAddon: r.name,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgesGroup + "/" + edgesVersion,
				Kind:       AddonKind,
				Name:       spec.Name,
				UID:        spec.UID,
				Controller: ptr.To(true),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{TokenSecretKey: []byte(token)},
	}
	existing, err := r.opts.Kube.CoreV1().Secrets(TokenSecretNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = r.opts.Kube.CoreV1().Secrets(TokenSecretNamespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	} else if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.OwnerReferences = desired.OwnerReferences
	existing.Type = corev1.SecretTypeOpaque
	existing.Data = desired.Data
	_, err = r.opts.Kube.CoreV1().Secrets(TokenSecretNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (r *runnerAddon) ensureNamespace(ctx context.Context, name string) error {
	_, err := r.opts.Kube.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %s: %w", name, err)
	}
	_, err = r.opts.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", name, err)
	}
	return nil
}

func runnerPort(spec *RunnerSpec) int32 {
	if spec.Port > 0 {
		return spec.Port
	}
	return defaultRunnerPort
}

// renderConfig turns the Addon spec into the runner's JSON enrollment. The
// listener is pinned to loopback here: the API has no host field, and this is
// the only place the address is constructed.
func (r *runnerAddon) renderConfig(spec *RunnerSpec) ([]byte, error) {
	id := r.runnerID()
	if !identifierPattern.MatchString(id) {
		return nil, fmt.Errorf("runner ID %q (edge name + add-on name) is not a valid runner identifier", id)
	}
	capacity := spec.MaximumCapacity
	if capacity == 0 {
		capacity = 1
	}
	if capacity != 1 {
		return nil, fmt.Errorf("spec.runner.maximumCapacity must be 1 for the single-execution runner, got %d", capacity)
	}
	repositories := make(map[string]runner.RepositoryConfig, len(spec.Repositories))
	for id, repo := range spec.Repositories {
		if !identifierPattern.MatchString(id) {
			return nil, fmt.Errorf("repository ID %q is invalid", id)
		}
		source := strings.TrimSpace(repo.Source)
		if source == "" {
			return nil, fmt.Errorf("repository %q has an empty source", id)
		}
		if !filepath.IsAbs(source) {
			return nil, fmt.Errorf("repository %q source %q must be an absolute path on the edge host", id, source)
		}
		repositories[id] = runner.RepositoryConfig{
			Source:         filepath.Clean(source),
			FetchRemoteURL: strings.TrimSpace(repo.FetchRemoteURL),
		}
	}
	cfg := runner.Config{
		ProtocolVersion: runner.ProtocolVersion,
		RunnerID:        id,
		Listen:          net.JoinHostPort("127.0.0.1", strconv.Itoa(int(runnerPort(spec)))),
		StateDir:        r.runnerState(),
		TokenFile:       r.tokenPath(),
		Toolchains:      spec.Toolchains,
		Verification:    spec.VerificationCapabilities,
		MaximumCapacity: 1,
		Repositories:    repositories,
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// childArgs is the `railgrid runner run` command line. Paths are passed
// explicitly rather than relying on the runner's defaults so the add-on owns
// every location it touches.
func (r *runnerAddon) childArgs(spec *RunnerSpec) []string {
	args := []string{
		"runner", "run",
		"--config", r.configPath(),
		"--state-dir", r.runnerState(),
		"--token-file", r.tokenPath(),
		"--harness", spec.HarnessName(),
	}
	switch spec.HarnessName() {
	case HarnessClaude:
		r.mu.Lock()
		kind := r.claudeKind
		r.mu.Unlock()
		args = append(args,
			"--claude-home", r.claudeHome(),
			"--claude-credential-file", r.claudeCredentialPath(),
			"--claude-credential-kind", string(kind),
		)
		if spec.Claude != nil {
			if binary := strings.TrimSpace(spec.Claude.Binary); binary != "" {
				args = append(args, "--claude-binary", binary)
			}
			if model := strings.TrimSpace(spec.Claude.Model); model != "" {
				args = append(args, "--claude-model", model)
			}
			if pin := strings.TrimSpace(spec.Claude.VersionPin); pin != "" {
				args = append(args, "--version-pin", pin)
			}
		}
	default:
		args = append(args, "--codex-home", r.codexHome())
		if spec.Codex != nil {
			if binary := strings.TrimSpace(spec.Codex.Binary); binary != "" {
				args = append(args, "--codex-binary", binary)
			}
			if pin := strings.TrimSpace(spec.Codex.VersionPin); pin != "" {
				args = append(args, "--version-pin", pin)
			}
		}
	}
	return args
}

// ensureSupervisor (re)starts the child when the rendered configuration, the
// arguments or the Codex session changed, and is otherwise a no-op.
func (r *runnerAddon) ensureSupervisor(ctx context.Context, args []string, config, auth []byte) error {
	sum := sha256.New()
	for _, a := range args {
		sum.Write([]byte(a))
		sum.Write([]byte{0})
	}
	sum.Write(config)
	sum.Write(auth)
	hash := hex.EncodeToString(sum.Sum(nil))

	r.mu.Lock()
	unchanged := r.sup != nil && r.hash == hash
	current := r.sup
	r.mu.Unlock()
	if unchanged {
		current.Start(ctx) // no-op when already running; restarts nothing
		return nil
	}
	if current != nil {
		if err := current.Stop(ctx); err != nil {
			klog.FromContext(ctx).Error(err, "stopping runner before restart", "addon", r.name)
		}
	}

	sup, err := supervisor.New(supervisor.Config{
		Name:       "addon/" + TypeRunner + "/" + r.name,
		Executable: r.opts.Executable,
		Args:       args,
		Dir:        r.stateDir(),
		// Built from scratch: the agent's hub token, its kubeconfig path and any
		// API keys in its environment must never reach a code-execution child.
		Env:            supervisor.ChildEnv(r.opts.Account.Home, ""),
		UID:            r.opts.Account.UID,
		GID:            r.opts.Account.GID,
		LogPath:        r.logPath(),
		InitialBackoff: r.opts.InitialBackoff,
	})
	if err != nil {
		return err
	}
	sup.Start(ctx)

	r.mu.Lock()
	r.sup = sup
	r.hash = hash
	r.mu.Unlock()
	return nil
}

// capabilitiesResponse is the subset of the runner's capabilities document the
// health probe reads.
type capabilitiesResponse struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Version         string                `json:"version"`
	RunnerID        string                `json:"runnerID"`
	Harnesses       []harnessCapabilities `json:"harnesses"`
}

// harnessCapabilities mirrors runner.HarnessCapability. One runner process
// serves one harness, so only the first entry is reported.
type harnessCapabilities struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

// probeResult is what a successful capabilities probe tells the add-on.
type probeResult struct {
	// Version is the RUNNER build, which is the agent build.
	Version string
	// Harness is the coding harness the runner drives, copied onto status so a
	// portal can show it without holding a runner bearer token.
	Harness *HarnessStatus
}

// probe asks the loopback runner for its capabilities with the same bearer
// token the provider will use. Running=True requires a runner/v1 answer, not
// merely a live process: a process that is up but not serving the protocol is
// not a working add-on.
func (r *runnerAddon) probe(ctx context.Context, port int32, token string) (probeResult, error) {
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))) + "/runner/v1/capabilities"
	deadline := time.Now().Add(r.opts.ProbeDeadline)
	var lastErr error
	for {
		probed, err := r.probeOnce(ctx, url, token)
		if err == nil {
			return probed, nil
		}
		lastErr = err
		if time.Now().After(deadline) || ctx.Err() != nil {
			return probeResult{}, lastErr
		}
		select {
		case <-ctx.Done():
			return probeResult{}, lastErr
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (r *runnerAddon) probeOnce(ctx context.Context, url, token string) (probeResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, r.opts.ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return probeResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := r.opts.HTTPClient.Do(req)
	if err != nil {
		return probeResult{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return probeResult{}, fmt.Errorf("capabilities probe returned HTTP %d", resp.StatusCode)
	}
	var caps capabilitiesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCapabilitiesBytes)).Decode(&caps); err != nil {
		return probeResult{}, fmt.Errorf("decoding capabilities: %w", err)
	}
	if caps.ProtocolVersion != runner.ProtocolVersion {
		return probeResult{}, fmt.Errorf("capabilities reported protocol %q, want %q", caps.ProtocolVersion, runner.ProtocolVersion)
	}
	probed := probeResult{Version: caps.Version}
	if len(caps.Harnesses) > 0 {
		h := caps.Harnesses[0]
		probed.Harness = &HarnessStatus{
			Name:    truncateField(h.Name),
			Version: truncateField(h.Version),
			Ready:   h.Ready,
			Reasons: truncateReasons(h.Reasons),
		}
	}
	return probed, nil
}

// maxCapabilitiesBytes bounds the probe response. The runner is our own
// process, but it is still a network peer.
const maxCapabilitiesBytes = 1 << 20

// maxHarnessReasons / maxHarnessField match the API's caps on status.harness,
// so a long reason is trimmed here rather than rejected by admission.
const (
	maxHarnessReasons = 16
	maxHarnessField   = 64
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
		out = append(out, truncate(reason, maxStatusMessage))
	}
	return out
}
