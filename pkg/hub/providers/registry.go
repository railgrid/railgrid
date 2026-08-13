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

// Package providers backs the hub's pluggable-provider extension surface:
// an in-memory routing table, reverse proxies for /ui/providers/{name}/*
// and /services/providers/{name}/*, and the controller that keeps the
// table in sync with ProviderCatalogEntry resources.
package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"sync"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"k8s.io/apimachinery/pkg/runtime"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
)

// HeartbeatTTL is how long a provider's last heartbeat is considered fresh.
// After this duration, the sweeper flips the registry record's
// HeartbeatStale flag, which the Ready computation observes.
const HeartbeatTTL = 90 * time.Second

// SweepInterval is how often the sweeper goroutine walks the registry to
// evict stale heartbeats. Should comfortably divide HeartbeatTTL.
const SweepInterval = 30 * time.Second

// Provider is the in-memory record the proxies consult to route a request.
// Fields are nil-able to reflect that UI/backend/VW are independently optional
// in the source ProviderCatalogEntry.
type Provider struct {
	Name         string
	DisplayName  string // human-readable label, surfaced to the portal
	IconURL      string // optional, defaults to /ui/providers/{name}/icon.svg
	Category     string // optional grouping in the portal nav; empty = top-level
	Dependencies []Dependency
	UIURL        *url.URL // proxy target for /ui/providers/{name}/*; nil → 404
	BackendURL   *url.URL // proxy target for /services/providers/{name}/*; nil → 404
	// VirtualWorkspaceURL is the provider-declared action transport target.
	// Provider Actions append /actions/{name}/{version} to this URL; they never
	// use BackendURL or a provider MCP endpoint.
	VirtualWorkspaceURL *url.URL
	Actions             []ProviderAction
	// AssistantSkills contains only validated, provider-supplied inline App
	// Studio packages. It intentionally carries no provider URL, credential, or
	// runtime handle; the authenticated catalog API is the sole distribution
	// boundary.
	AssistantSkills  []ProviderAssistantSkill
	BuiltinRoute     string     // when set, portal renders this Vue route instead of loading /main.js
	Children         []NavChild // sub-nav entries surfaced indented under this provider
	Version          string     // CatalogEntry.spec.version (chart-declared)
	APIExportPath    string     // kcp workspace path hosting the APIExport (e.g. root:faros:providers:cost)
	APIExportName    string     // APIExport name (e.g. cost.providers.faros.sh)
	PermissionClaims []PermissionClaim

	// EdgeProxyAccess mirrors CatalogEntry.spec.edgeProxyAccess: on tenant
	// Enable, the hub grants the provider SA the "proxy" verb on edges in
	// the tenant workspace (see pkg/hub/restapi/providers_enable.go).
	EdgeProxyAccess bool
	// WorkspaceCluster is the logical cluster ID of the provider's
	// sub-workspace (Workspace.spec.cluster of root:faros:providers:{name}).
	// It anchors the qualified RBAC subject the edge-proxy grant binds —
	// the same cluster ID kcp puts in the provider SA's token claims. Set
	// via SetWorkspaceCluster after provisioning; empty until then.
	WorkspaceCluster string

	// LocalUIAssets, when non-nil, is an embedded fs.FS that the UI proxy
	// serves under /ui/providers/{Name}/* instead of forwarding to UIURL.
	// Populated for first-party providers whose Vite-built portal/dist is
	// baked into the hub binary via //go:embed. Third-party providers leave
	// it nil and rely on UIURL.
	LocalUIAssets fs.FS

	// EndpointsValid is true when spec.ui.url/spec.backend.url parsed cleanly
	// and at least one endpoint was declared (or LocalUIAssets is set).
	// The catalog controller sets this; the sweeper does not touch it.
	EndpointsValid bool

	// LastHeartbeat is the provider's most recent beat. It is set both by the
	// POST /api/providers/{name}/heartbeat handler on the replica that served
	// it and by the catalog reconciler from CatalogEntry.status.lastHeartbeat,
	// which is how the signal reaches replicas that did not serve the beat.
	// Zero until the first heartbeat (or for providers that don't heartbeat).
	LastHeartbeat time.Time
	// ReportedVersion is the version string the provider pod sent in its
	// most recent heartbeat — may diverge from Version during a chart
	// upgrade.
	ReportedVersion string
	// HeartbeatRequired distinguishes "heartbeats-not-configured" from
	// "stale heartbeat". It flips to true on the first heartbeat received,
	// and stays true. From then on, missing heartbeats mark the provider
	// not Ready.
	HeartbeatRequired bool
	// HeartbeatStale is maintained by the sweeper. When HeartbeatRequired
	// is true and now - LastHeartbeat exceeds HeartbeatTTL, the sweeper
	// flips this to true.
	HeartbeatStale bool
}

// Dependency mirrors CatalogEntry.spec.dependencies so the portal and hub can
// gate provider enablement without coupling callers to CRD types.
type Dependency struct {
	Name string
}

// Ready returns true when the proxy should forward to the provider. The
// catalog controller's URL parse must have succeeded AND, if the provider
// has heartbeated at least once, its most recent heartbeat must be fresh.
func (p Provider) Ready() bool {
	if !p.EndpointsValid {
		return false
	}
	if p.HeartbeatRequired && p.HeartbeatStale {
		return false
	}
	return true
}

// PermissionClaim mirrors CatalogEntry.spec.apiExport.permissionClaims so the
// portal can render the Enable confirmation dialog without coupling to the
// CRD types.
type PermissionClaim struct {
	Group        string
	Resource     string
	Verbs        []string
	TenantScoped bool
}

// NavChild mirrors CatalogEntry.spec.ui.children — a single sub-nav
// entry the portal renders indented under its parent provider.
type NavChild struct {
	DisplayName  string
	BuiltinRoute string
}

// ProviderAction is the hub's normalized view of one CatalogEntry action.
// The catalog API owns the wire shape; the registry keeps the complete
// discovery and policy metadata needed by both the forward-only action
// transport and the portal's consent UI. It deliberately does not retain
// provider transport URLs on each action.
type ProviderAction struct {
	ID              string
	Name            string
	Version         string
	DisplayName     string
	Description     string
	Resource        ProviderActionResource
	InputSchema     json.RawMessage
	OutputSchema    json.RawMessage
	InputValidator  *jsonschema.Schema
	OutputValidator *jsonschema.Schema
	SchemaDigest    string
	ExecutionMode   string
	ReadOnly        bool
	Risk            providersv1alpha1.ProviderActionRisk
	Idempotency     string
	Limits          ProviderActionLimits
	Consent         providersv1alpha1.ProviderActionConsent
	Deprecation     *providersv1alpha1.ProviderActionDeprecation
}

// ProviderActionResource identifies the provider-owned resource an action is
// allowed to receive. The request's resourceRef must match all three fields.
type ProviderActionResource struct {
	APIVersion string
	Kind       string
	Resource   string
}

// ProviderActionLimits carries catalog-enforced byte, timeout, and result
// bounds into the transport without coupling it to API object pointers.
type ProviderActionLimits struct {
	TimeoutSeconds int64
	MaxInputBytes  int64
	MaxOutputBytes int64
	MaxResultItems int64
}

// ProviderAssistantSkill is the registry's normalized view of one inline App
// Studio skill package. Resource paths remain package-relative and content is
// copied into the catalog response only after CatalogEntry validation.
type ProviderAssistantSkill struct {
	PackageName string
	Version     string
	Digest      string
	Skill       string
	Resources   []ProviderAssistantSkillResource
}

// ProviderAssistantSkillResource is one package-relative supporting resource.
type ProviderAssistantSkillResource struct {
	Path    string
	Content string
}

// ParseProviderActions normalizes the typed CatalogEntry action list and
// compiles both schemas with the full JSON Schema implementation. Callers must
// handle the error: admitting an action without compiled validators would make
// the router fail open on malformed catalog metadata.
func ParseProviderActions(actions []providersv1alpha1.ProviderActionSpec) ([]ProviderAction, error) {
	out := make([]ProviderAction, 0, len(actions))
	for _, action := range actions {
		parts := strings.SplitN(strings.TrimSpace(action.ID), "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("action ID %q is invalid", action.ID)
		}
		inputSchema := schemaBytes(action.InputSchema)
		outputSchema := schemaBytes(action.OutputSchema)
		inputValidator, err := CompileProviderActionSchema(inputSchema, action.ID+"/input")
		if err != nil {
			return nil, fmt.Errorf("action %q inputSchema: %w", action.ID, err)
		}
		outputValidator, err := CompileProviderActionSchema(outputSchema, action.ID+"/output")
		if err != nil {
			return nil, fmt.Errorf("action %q outputSchema: %w", action.ID, err)
		}
		out = append(out, ProviderAction{
			ID: parts[0] + "/" + parts[1], Name: parts[0], Version: parts[1],
			DisplayName: action.DisplayName, Description: action.Description,
			Resource: ProviderActionResource{
				APIVersion: action.BoundResource.APIVersion,
				Kind:       action.BoundResource.Kind,
				Resource:   action.BoundResource.Resource,
			},
			InputSchema: inputSchema, OutputSchema: outputSchema,
			InputValidator: inputValidator, OutputValidator: outputValidator,
			SchemaDigest: action.SchemaDigest, ExecutionMode: string(action.ExecutionMode),
			ReadOnly: action.ReadOnly, Risk: action.Risk,
			Idempotency: string(action.Idempotency),
			Limits: ProviderActionLimits{
				TimeoutSeconds: action.Limits.TimeoutSeconds,
				MaxInputBytes:  action.Limits.MaxInputBytes,
				MaxOutputBytes: action.Limits.MaxOutputBytes,
				MaxResultItems: action.Limits.MaxResultItems,
			},
			Consent:     action.Consent,
			Deprecation: action.Deprecation.DeepCopy(),
		})
	}
	return out, nil
}

// CompileProviderActionSchema compiles one catalog-declared JSON Schema with
// format assertions enabled. It is exported for the action transport's
// bounded fallback cache used by tests and embedders that construct a
// normalized Provider directly instead of going through the catalog
// reconciler.
func CompileProviderActionSchema(raw json.RawMessage, name string) (*jsonschema.Schema, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("schema is required")
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := validateLocalSchemaReferences(document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	// Action contracts must enforce format assertions even when the schema
	// omits an explicit draft vocabulary declaration.
	compiler.AssertFormat()
	compiler.UseLoader(noExternalSchemaLoader{})
	location := "urn:faros:provider-action:" + url.PathEscape(name)
	if err := compiler.AddResource(location, document); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return compiled, nil
}

type noExternalSchemaLoader struct{}

func (noExternalSchemaLoader) Load(ref string) (any, error) {
	return nil, fmt.Errorf("external schema resource %q is not allowed", ref)
}

func validateLocalSchemaReferences(value any) error {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			if err := validateLocalSchemaReferences(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range current {
			switch key {
			case "$ref", "$dynamicRef", "$recursiveRef":
				ref, ok := item.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return fmt.Errorf("%s must be a local fragment reference", key)
				}
			case "$id":
				id, ok := item.(string)
				if !ok || (id != "" && !strings.HasPrefix(id, "#")) {
					return fmt.Errorf("$id must be local to the declared schema")
				}
			case "$schema":
				schemaURL, ok := item.(string)
				if !ok || !localOrBuiltinSchemaURL(schemaURL) {
					return fmt.Errorf("$schema must be a local or built-in JSON Schema URL")
				}
			case "$vocabulary":
				vocabularies, ok := item.(map[string]any)
				if !ok {
					return fmt.Errorf("$vocabulary must be an object")
				}
				for vocabulary := range vocabularies {
					if !localOrBuiltinSchemaURL(vocabulary) {
						return fmt.Errorf("$vocabulary %q is external and not allowed", vocabulary)
					}
				}
			}
			if err := validateLocalSchemaReferences(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func localOrBuiltinSchemaURL(value string) bool {
	return strings.HasPrefix(value, "#") ||
		strings.HasPrefix(value, "http://json-schema.org/") ||
		strings.HasPrefix(value, "https://json-schema.org/")
}

func schemaBytes(extension *runtime.RawExtension) json.RawMessage {
	if extension == nil {
		return nil
	}
	if len(extension.Raw) > 0 {
		return append(json.RawMessage(nil), extension.Raw...)
	}
	if extension.Object == nil {
		return nil
	}
	encoded, err := json.Marshal(extension.Object)
	if err != nil {
		return nil
	}
	return encoded
}

// Registry is the hub's source of truth for provider routing. It is updated
// by the catalog controller on Watch events and read by the proxies on each
// request. Reads vastly outnumber writes; an RWMutex is sufficient.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]*Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]*Provider{}}
}

// Get returns a copy of the Provider record (or false if unknown). A copy is
// returned so callers can safely inspect fields without holding the lock.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[name]
	if !ok {
		return Provider{}, false
	}
	return cloneProvider(*p), true
}

// List returns a snapshot of all registered providers.
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.byName))
	for _, p := range r.byName {
		out = append(out, cloneProvider(*p))
	}
	return out
}

// Upsert replaces the spec-derived fields of the registry record for p.Name
// (or inserts a new record).
//
// Liveness is merged, not overwritten. p.LastHeartbeat carries what the
// CatalogEntry status says (the cross-replica signal); the record may already
// hold a newer beat this replica served directly, or one whose status write was
// throttled. Taking the later of the two keeps the merge monotone, so neither
// source can move a provider backwards in time and flip it stale. HeartbeatStale
// stays a purely local verdict, recomputed by the sweeper from these timestamps.
func (r *Registry) Upsert(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byName[p.Name]; ok {
		if existing.LastHeartbeat.After(p.LastHeartbeat) {
			// The reported version belongs to the beat it arrived with, so it
			// travels with the timestamp that wins.
			p.LastHeartbeat = existing.LastHeartbeat
			if existing.ReportedVersion != "" {
				p.ReportedVersion = existing.ReportedVersion
			}
		}
		// A provider that has ever heartbeated is expected to keep doing so.
		p.HeartbeatRequired = p.HeartbeatRequired || existing.HeartbeatRequired
		p.HeartbeatStale = existing.HeartbeatStale
		if p.WorkspaceCluster == "" {
			// Provisioning sets this after the Upsert in the same reconcile;
			// don't lose it on the next reconcile's fresh Provider value.
			p.WorkspaceCluster = existing.WorkspaceCluster
		}
	}
	cp := p
	cp.AssistantSkills = cloneProviderAssistantSkills(p.AssistantSkills)
	r.byName[p.Name] = &cp
}

func cloneProviderAssistantSkills(in []ProviderAssistantSkill) []ProviderAssistantSkill {
	if in == nil {
		return nil
	}
	out := make([]ProviderAssistantSkill, len(in))
	for i, skill := range in {
		out[i] = skill
		if skill.Resources != nil {
			out[i].Resources = append([]ProviderAssistantSkillResource(nil), skill.Resources...)
		}
	}
	return out
}

func cloneProvider(p Provider) Provider {
	p.Dependencies = append([]Dependency(nil), p.Dependencies...)
	p.PermissionClaims = append([]PermissionClaim(nil), p.PermissionClaims...)
	for i := range p.PermissionClaims {
		p.PermissionClaims[i].Verbs = append([]string(nil), p.PermissionClaims[i].Verbs...)
	}
	p.Children = append([]NavChild(nil), p.Children...)
	p.Actions = append([]ProviderAction(nil), p.Actions...)
	for i := range p.Actions {
		p.Actions[i].InputSchema = append(json.RawMessage(nil), p.Actions[i].InputSchema...)
		p.Actions[i].OutputSchema = append(json.RawMessage(nil), p.Actions[i].OutputSchema...)
	}
	p.AssistantSkills = cloneProviderAssistantSkills(p.AssistantSkills)
	return p
}

// SetWorkspaceCluster records the logical cluster ID of the provider's
// sub-workspace. Called by the catalog controller once provisioning has
// resolved it (the workspace must be Ready before spec.cluster is set).
// Returns false if the name is not in the registry.
func (r *Registry) SetWorkspaceCluster(name, cluster string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byName[name]
	if !ok {
		return false
	}
	p.WorkspaceCluster = cluster
	return true
}

// Heartbeat records a heartbeat for a known provider. Returns false if the
// name is not in the registry (caller should reject with 404). reportedVersion
// is optional; pass empty to leave the field untouched.
func (r *Registry) Heartbeat(name, reportedVersion string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byName[name]
	if !ok {
		return false
	}
	p.LastHeartbeat = now
	p.HeartbeatRequired = true
	p.HeartbeatStale = false
	if reportedVersion != "" {
		p.ReportedVersion = reportedVersion
	}
	return true
}

// SweepStale walks the registry and marks providers whose last heartbeat is
// older than ttl as stale. Providers that have never heartbeated
// (HeartbeatRequired=false) are left alone — they're treated as "doesn't
// heartbeat" not "missed a beat". Returns the number of records flipped.
func (r *Registry) SweepStale(now time.Time, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	flipped := 0
	for _, p := range r.byName {
		if !p.HeartbeatRequired {
			continue
		}
		stale := now.Sub(p.LastHeartbeat) > ttl
		if stale != p.HeartbeatStale {
			p.HeartbeatStale = stale
			flipped++
		}
	}
	return flipped
}

// Delete removes the record for name. Returns true if anything was removed.
func (r *Registry) Delete(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.byName[name]
	delete(r.byName, name)
	return ok
}

// ParseURL is a small helper for the controller that converts a spec string
// into a *url.URL with the requirements the proxies rely on (non-empty host,
// absolute).
func ParseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url %q must be absolute (scheme + host)", raw)
	}
	return u, nil
}
