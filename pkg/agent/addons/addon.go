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

// Package addons materializes edges.railgrid.ai Addon objects on the host next
// to the agent.
//
// It is the same loop as the workload plane in pkg/agent/reconciler: the hub
// DECLARES an Addon, the agent MATERIALIZES it locally, and the agent REPORTS
// what happened on the object's status. There is deliberately no imperative
// channel from the hub to the host — an Addon is a desired state, and an agent
// that disagrees with it (because the machine owner never allowed the type)
// simply says so in status and does nothing.
//
// Two keys are required before anything runs. The tenant creates the Addon,
// AND the machine owner started the agent with --allow-addon for its type.
// Neither alone materializes anything. See docs/edge-addons.md.
//
// The Addon API lives in the standalone edges provider module, so — exactly
// like pkg/agent/reconciler — this package reads the objects as unstructured
// and decodes only the fields it needs into local mirror structs.
package addons

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Known add-on types. Keep in step with the AddonType enum in the edges
// provider's API (providers/edges/apis/v1alpha1/types_addon.go).
const (
	// TypeRunner supervises `railgrid runner run` on this host.
	TypeRunner = "runner"
)

// KnownTypes are the add-on types this agent build can materialize. The
// --allow-addon flag rejects anything outside this set so a typo fails at
// startup rather than silently allowing nothing.
var KnownTypes = []string{TypeRunner}

// Phases mirror AddonPhase in the edges API.
const (
	PhasePending    = "Pending"
	PhaseInstalling = "Installing"
	PhaseRunning    = "Running"
	PhaseDegraded   = "Degraded"
	PhaseBlocked    = "Blocked"
	PhasePaused     = "Paused"
)

// Condition types the AGENT owns. Published is the provider's and is never
// written from here.
const (
	ConditionAllowed    = "Allowed"
	ConditionConfigured = "Configured"
	ConditionRunning    = "Running"
)

// Condition reasons. Keep in step with the AddonReason* constants in the edges
// API: the portal and docs key on these strings.
const (
	ReasonNotAllowedOnEdge = "NotAllowedOnEdge"
	ReasonAllowedOnEdge    = "AllowedOnEdge"
	ReasonCodexAuthMissing = "CodexAuthMissing"
	// ReasonClaudeAuthMissing: no Claude Code credential Secret, or the Secret
	// carries neither credential key. Nothing is started.
	ReasonClaudeAuthMissing = "ClaudeAuthMissing"
	// ReasonClaudeAuthInvalid: the Secret carries BOTH credential keys, or the
	// value is unusable. Which identity the tenant meant is not a guess the
	// agent will make, so nothing is started.
	ReasonClaudeAuthInvalid = "ClaudeAuthInvalid"
	ReasonPaused            = "Paused"
	ReasonConfigured        = "Configured"
	ReasonConfigError       = "ConfigError"
	ReasonProbeOK           = "ProbeSucceeded"
	ReasonProbeFailed       = "ProbeFailed"
	ReasonStarting          = "Starting"
	ReasonNotRunning        = "NotRunning"
)

// EdgeRef is the connectable an Addon is bound to.
type EdgeRef struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name"`
}

// Repository mirrors AddonRepository. Both fields are optional individually —
// an entry may name a local checkout, a remote the runner clones from, or both
// — but an entry naming neither is a configuration mistake and is refused.
type Repository struct {
	Source         string `json:"source,omitempty"`
	FetchRemoteURL string `json:"fetchRemoteURL,omitempty"`
}

// Harness names, mirroring the AddonHarness enum.
const (
	HarnessCodex  = "codex"
	HarnessClaude = "claude"
)

// Secret keys a Claude Code credential may be carried in. Exactly one of them,
// never both: which identity the tenant meant is not a guess.
const (
	ClaudeOAuthTokenKey = "oauthToken"
	ClaudeAPIKeyKey     = "apiKey"
)

// Claude mirrors AddonClaude. Unlike Codex there is no portable on-disk login
// state, so the credential is a VALUE the adapter injects as one environment
// variable; the agent only ever puts it in a runner-owned 0600 file.
type Claude struct {
	Binary        string     `json:"binary,omitempty"`
	VersionPin    string     `json:"versionPin,omitempty"`
	Model         string     `json:"model,omitempty"`
	AuthSecretRef *SecretRef `json:"authSecretRef,omitempty"`
	// PermissionMode and AllowedTools are passed through to the runner; the
	// harness validates the mode and the API restricts it to two values.
	PermissionMode string   `json:"permissionMode,omitempty"`
	AllowedTools   []string `json:"allowedTools,omitempty"`
}

// Codex mirrors AddonCodex. There is no API-key field on purpose: the Codex
// adapter strips API-key environment variables from the harness, so one could
// never be used — see pkg/runner/harness/codex.blockedEnvKey.
type Codex struct {
	Binary        string     `json:"binary,omitempty"`
	VersionPin    string     `json:"versionPin,omitempty"`
	AuthSecretRef *SecretRef `json:"authSecretRef,omitempty"`
}

// SecretRef names a Secret in the tenant workspace.
type SecretRef struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// RunnerSpec mirrors AddonRunnerSpec.
type RunnerSpec struct {
	Port                     int32                 `json:"port,omitempty"`
	MaximumCapacity          int32                 `json:"maximumCapacity,omitempty"`
	Toolchains               []string              `json:"toolchains,omitempty"`
	VerificationCapabilities []string              `json:"verificationCapabilities,omitempty"`
	Repositories             map[string]Repository `json:"repositories,omitempty"`
	Harness                  string                `json:"harness,omitempty"`
	Codex                    *Codex                `json:"codex,omitempty"`
	Claude                   *Claude               `json:"claude,omitempty"`
}

// HarnessName returns the selected harness, defaulting to Codex exactly as the
// API's own default does — so an Addon written before the field existed keeps
// behaving the way it always did.
func (s *RunnerSpec) HarnessName() string {
	if s == nil || strings.TrimSpace(s.Harness) == "" {
		return HarnessCodex
	}
	return s.Harness
}

// Spec is one Addon object as the agent sees it: the identity it needs to own
// the object's children plus the decoded spec.
type Spec struct {
	// Name is the Addon's metadata.name. It names the add-on's state directory
	// and the Secret it publishes, so it is validated before use.
	Name string
	// UID and Generation come from metadata; UID goes into the ownerReference
	// of anything the add-on publishes, Generation into status.observedGeneration.
	UID        types.UID
	Generation int64
	// EdgeRef, Type, Paused and Runner are the decoded spec.
	EdgeRef EdgeRef
	Type    string
	Paused  bool
	Runner  *RunnerSpec
}

// Condition is one agent-owned status condition.
type Condition struct {
	Type    string
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

// Status is what an Addon implementation reports back after a reconcile. The
// manager turns it into the object's status subresource, adding
// observedGeneration and leaving the provider's Published condition alone.
type Status struct {
	Phase      string
	Version    string
	Message    string
	Harness    *HarnessStatus
	Conditions []Condition
}

// HarnessStatus is the harness half of a runner's capabilities response. It is
// reported onto the Addon so a portal can show "claude-code 2.1.273, ready"
// without holding a runner bearer token and calling the runner itself.
type HarnessStatus struct {
	Name    string
	Version string
	Ready   bool
	Reasons []string
}

// Addon materializes one Addon object on this host. One instance per Addon
// object; the manager creates it on first sight and keeps it until the object
// is deleted or stops targeting this edge.
//
// A new add-on type is: an implementation of this interface, a Factory
// registered with the manager, a value in the API's AddonType enum, and — if
// it should be reachable through the hub — a hook in the provider's add-on
// controller. See docs/edge-addons.md.
type Addon interface {
	// Type is the AddonType this implementation serves.
	Type() string
	// Reconcile converges the host to spec and reports what it observed. It
	// must be idempotent: the manager calls it on every change and on a
	// periodic resync.
	Reconcile(ctx context.Context, spec Spec) (Status, error)
	// Stop tears down the supervised process. It keeps the add-on's state
	// directory and NEVER touches enrolled repositories: pausing or deleting an
	// Addon must not destroy a developer's checkouts.
	Stop(ctx context.Context) error
}

// Factory builds an Addon instance for one object name.
type Factory func(name string) (Addon, error)
