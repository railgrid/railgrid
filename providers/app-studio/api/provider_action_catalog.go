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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/hubapi"
	appskills "github.com/railgrid/provider-app-studio/skills"
	"github.com/railgrid/provider-sdk/dataplane"
)

const (
	providerCatalogPath             = hubapi.ProviderCatalogPath
	providerCatalogMaxResponseBytes = 4 << 20
	providerCatalogCallTimeout      = 15 * time.Second
)

var projectActionSchemaDigestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// providerActionCatalogResolver receives the identity whose workspace
// selection scopes the hub catalog request (made as the provider). Tests may
// inject a deterministic resolver without opening a second HTTP server, while
// production always uses fetchProviderActionCatalog.
type providerActionCatalogResolver func(context.Context, identity) ([]providerCatalogEntry, error)
type providerResourceDiscoveryResolver func(context.Context, identity, string, string, string, string) (providerResourceDiscoveryResponse, error)

// These structs mirror the hub's /api/providers contract, whose four sections
// follow CatalogEntry.spec one for one. The App Studio gateway only needs a
// subset for grant verification, but retaining the published fields makes
// decoding forward-compatible and keeps fixtures representative of the catalog
// wire shape.
type providerCatalogEntry struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Export is what a tenant may call once it enables the provider: the
	// APIExport and the resources it serves, with the verbs and actions on
	// each. An action's apiVersion, kind and resource are the PARENT
	// resource's, declared once there — there is no per-action bound resource
	// any more.
	Export *providerCatalogExport `json:"export,omitempty"`
	// Hub is what the provider asks of the hub itself, and where its inline
	// assistant skill packages are published.
	Hub *providerCatalogHub `json:"hub,omitempty"`
}

type providerCatalogExport struct {
	Name      string                          `json:"name"`
	Resources []providerCatalogExportResource `json:"resources"`
}

// providerCatalogExportResource is one exported kind with the coordinates on
// it. It is the only place an action's resource coordinate is published, so a
// grant is verified against this triple rather than against anything the
// action itself carries.
type providerCatalogExportResource struct {
	Name       string                  `json:"name"`
	APIVersion string                  `json:"apiVersion"`
	Kind       string                  `json:"kind"`
	Verbs      []providerCatalogVerb   `json:"verbs"`
	Actions    []providerCatalogAction `json:"actions"`
}

type providerCatalogVerb struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Stream      bool   `json:"stream"`
	ReadOnly    bool   `json:"readOnly"`
}

type providerCatalogHub struct {
	AssistantSkills []providerCatalogAssistantSkill `json:"assistantSkills"`
}

type providerCatalogAssistantSkill struct {
	PackageName string                             `json:"packageName"`
	Version     string                             `json:"version"`
	Digest      string                             `json:"digest"`
	Skill       string                             `json:"skill"`
	Resources   []providerCatalogAssistantResource `json:"resources"`
}

type providerCatalogAssistantResource struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// providerCatalogAction is one versioned action. Name and Version are two
// fields, not one id: the coordinate kcp routes on is the name alone, and the
// "<name>/<version>" string a grant and a consent record key on is derived
// from the pair. The hub also publishes the derived
// id, which is deliberately not decoded here — one source beats two.
type providerCatalogAction struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Version       string                       `json:"version"`
	DisplayName   string                       `json:"displayName"`
	Description   string                       `json:"description"`
	InputSchema   json.RawMessage              `json:"inputSchema"`
	OutputSchema  json.RawMessage              `json:"outputSchema"`
	SchemaDigest  string                       `json:"schemaDigest"`
	ExecutionMode string                       `json:"executionMode"`
	ReadOnly      bool                         `json:"readOnly"`
	Risk          string                       `json:"risk"`
	Idempotency   string                       `json:"idempotency"`
	Limits        providerCatalogActionLimits  `json:"limits"`
	Consent       providerCatalogActionConsent `json:"consent"`
	Deprecation   *providerCatalogDeprecation  `json:"deprecation,omitempty"`
}

// providerCatalogBoundAction is one action paired with the exported resource it
// hangs off. Everything that used to read an action's own boundResource walks
// these instead: the coordinate is a property of the parent resource, and
// flattening it once here keeps the nested walk out of every caller.
type providerCatalogBoundAction struct {
	APIVersion string
	Kind       string
	Resource   string
	Action     providerCatalogAction
}

// providerCatalogBoundActions flattens one catalog entry's export into every
// action it publishes, in declaration order. A resource missing any of its
// coordinate triple is skipped: the catalog is external data, and an action
// nothing can be addressed on is not a grantable action.
func providerCatalogBoundActions(entry providerCatalogEntry) []providerCatalogBoundAction {
	if entry.Export == nil {
		return nil
	}
	out := make([]providerCatalogBoundAction, 0, len(entry.Export.Resources))
	for _, resource := range entry.Export.Resources {
		apiVersion := strings.TrimSpace(resource.APIVersion)
		kind := strings.TrimSpace(resource.Kind)
		name := strings.TrimSpace(resource.Name)
		if apiVersion == "" || kind == "" || name == "" {
			continue
		}
		for _, action := range resource.Actions {
			out = append(out, providerCatalogBoundAction{
				APIVersion: apiVersion, Kind: kind, Resource: name, Action: action,
			})
		}
	}
	return out
}

type providerCatalogActionLimits struct {
	TimeoutSeconds int64 `json:"timeoutSeconds"`
	MaxInputBytes  int64 `json:"maxInputBytes"`
	MaxOutputBytes int64 `json:"maxOutputBytes"`
	MaxResultItems int64 `json:"maxResultItems"`
}

type providerCatalogActionConsent struct {
	Required bool   `json:"required"`
	Prompt   string `json:"prompt"`
	Scope    string `json:"scope"`
}

type providerCatalogDeprecation struct {
	Deprecated    bool         `json:"deprecated"`
	Message       string       `json:"message"`
	ReplacementID string       `json:"replacementID"`
	Sunset        *metav1.Time `json:"sunset"`
}

func (s *Server) verifyProjectActionGrants(ctx context.Context, id identity, provider string, ref *aiv1alpha1.ProjectProviderResourceReference, actions []aiv1alpha1.ProjectProviderActionSpec, consentAccepted bool, projects ...*aiv1alpha1.Project) ([]aiv1alpha1.ProjectProviderActionSpec, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, newValidationError("provider is required")
	}
	if err := validateProviderReferenceRef(ref); err != nil {
		return nil, err
	}
	caller := strings.TrimSpace(id.user)
	if caller == "" {
		return nil, newValidationError("authenticated caller is required to grant provider actions")
	}
	var catalog []providerCatalogEntry
	var err error
	if len(projects) > 0 && projects[0] != nil {
		catalog, err = s.providerActionCatalogForProject(ctx, id, projects[0])
	} else {
		catalog, err = s.providerActionCatalog(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	verified := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(actions))
	for _, grant := range actions {
		meta, err := findProviderCatalogAction(catalog, provider, ref, grant)
		if err != nil {
			return nil, err
		}
		if meta.Consent.Required && !consentAccepted {
			return nil, newValidationError(fmt.Sprintf("provider action %s requires explicit consentAccepted", grant.Name+"/"+grant.Version))
		}
		grant.GrantedBy = caller
		grantedAt := metav1.Now()
		grant.GrantedAt = &grantedAt
		grant.Revoked = false
		grant.RevokedBy = ""
		grant.RevokedAt = nil
		verified = append(verified, grant)
	}
	return verified, nil
}

func findProviderCatalogAction(catalog []providerCatalogEntry, provider string, ref *aiv1alpha1.ProjectProviderResourceReference, grant aiv1alpha1.ProjectProviderActionSpec) (providerCatalogAction, error) {
	var entry *providerCatalogEntry
	for i := range catalog {
		if catalog[i].Name == provider {
			entry = &catalog[i]
			break
		}
	}
	if entry == nil {
		return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider %q is not present in the action catalog", provider))
	}
	if !entry.Ready {
		return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider %q is not ready for action grants", provider))
	}
	name, version, err := normalizeIntegrationAction(grant.Name, grant.Version)
	if err != nil {
		return providerCatalogAction{}, err
	}
	for _, bound := range providerCatalogBoundActions(*entry) {
		action := bound.Action
		if strings.TrimSpace(action.Name) != name || strings.TrimSpace(action.Version) != version {
			continue
		}
		// The coordinate comes off the PARENT resource the action is declared
		// on, which is the only place the catalog publishes it.
		if bound.APIVersion != strings.TrimSpace(ref.APIVersion) ||
			bound.Kind != strings.TrimSpace(ref.Kind) ||
			bound.Resource != strings.TrimSpace(ref.Resource) {
			return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider action %s/%s is not bound to resource %s/%s/%s", name, version, ref.APIVersion, ref.Kind, ref.Resource))
		}
		if action.SchemaDigest == "" || !projectActionSchemaDigestRE.MatchString(action.SchemaDigest) {
			return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider action %s/%s has an invalid schemaDigest", name, version))
		}
		if action.SchemaDigest != grant.SchemaDigest {
			return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider action %s/%s schemaDigest does not match the catalog", name, version))
		}
		if action.Deprecation != nil && action.Deprecation.Deprecated {
			return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider action %s/%s is deprecated", name, version))
		}
		return action, nil
	}
	return providerCatalogAction{}, newValidationError(fmt.Sprintf("provider action %s/%s is not available for the bound resource", name, version))
}

func (s *Server) providerActionCatalog(ctx context.Context, id identity) ([]providerCatalogEntry, error) {
	if s.providerActionCatalogResolver != nil {
		return s.providerActionCatalogResolver(ctx, id)
	}
	return s.fetchProviderActionCatalog(ctx, id)
}

// providerActionCatalogForProject fetches the read-only catalog with the
// current Project's hub-minted scoped identity. Request proofs are bound to a
// particular incoming action and are absent for workload or background
// requests; they are not reused here.
func (s *Server) providerActionCatalogForProject(ctx context.Context, id identity, project *aiv1alpha1.Project) ([]providerCatalogEntry, error) {
	if s != nil && s.providerActionCatalogResolver != nil {
		return s.providerActionCatalogResolver(ctx, id)
	}
	catalog, err := s.fetchProviderCatalogForProject(ctx, id, project)
	if err != nil {
		return nil, err
	}
	return catalog.Items, nil
}

// errProjectActionDigestDrift marks a persisted grant whose schema digest no
// longer matches the live catalog. Invocation maps it to 409 Conflict: the
// grant must be re-verified (and re-consented where required) before the
// action can run again.
type errProjectActionDigestDrift struct{ message string }

func (e errProjectActionDigestDrift) Error() string { return e.message }

// verifyProjectActionDigestForInvoke re-checks the persisted grant against
// the workspace's live catalog at invocation time. With invocations riding
// the provider data plane directly, this is where schema drift is caught —
// the grant-time digest pin alone would let a provider schema bump go
// unnoticed until the generated app breaks on changed output.
func (s *Server) verifyProjectActionDigestForInvoke(ctx context.Context, id identity, project *aiv1alpha1.Project, provider string, ref *aiv1alpha1.ProjectProviderResourceReference, name, version, grantDigest string) error {
	catalog, err := s.providerActionCatalogForProject(ctx, id, project)
	if err != nil {
		return fmt.Errorf("provider action catalog is unavailable: %w", err)
	}
	meta, err := findProviderCatalogAction(catalog, provider, ref, aiv1alpha1.ProjectProviderActionSpec{Name: name, Version: version, SchemaDigest: grantDigest})
	if err != nil {
		return errProjectActionDigestDrift{message: err.Error()}
	}
	if meta.SchemaDigest != grantDigest {
		return errProjectActionDigestDrift{message: fmt.Sprintf("provider action %s/%s schema digest has drifted from the catalog", name, version)}
	}
	return nil
}

// providerAssistantSkillSource loads provider-declared packages through the
// same authenticated /api/providers catalog as Provider Actions. It never
// contacts provider backends. Skill presence follows the registered
// CatalogEntry; transient heartbeat/readiness state does not revoke an
// already-published package.
func (s *Server) providerAssistantSkillSource(ctx context.Context, id identity) (appskills.Source, error) {
	if s == nil {
		return nil, errors.New("provider assistant skill catalog is not configured")
	}
	if s.providerActionCatalogResolver == nil && (strings.TrimSpace(s.hubBase) == "" || strings.TrimSpace(s.hubToken) == "") {
		// Provider skills are optional guidance. A process with no hub, or no
		// hub credential to fetch the catalog with, must not block bundled or
		// project skills (or an otherwise actionless assistant turn).
		return appskills.NewProviderSkillSource(nil)
	}
	catalog, err := s.providerActionCatalog(ctx, id)
	if err != nil {
		// Provider skills are optional guidance. Return a source whose List
		// failure is isolated by appskills.Catalog.Load, preserving bundled and
		// project entries while exposing only its sanitized bounded warning. The
		// same catalog failure remains an error for Provider Action/grant paths.
		return appskills.NewProviderSkillUnavailableSource(), nil
	}
	packages := make([]appskills.ProviderSkillPackage, 0)
	for _, provider := range catalog {
		if provider.Hub == nil {
			continue
		}
		for _, skill := range provider.Hub.AssistantSkills {
			resources := make([]appskills.ProviderSkillResource, 0, len(skill.Resources))
			for _, resource := range skill.Resources {
				resources = append(resources, appskills.ProviderSkillResource{Path: resource.Path, Content: resource.Content})
			}
			packages = append(packages, appskills.ProviderSkillPackage{
				ProviderName: provider.Name,
				PackageName:  skill.PackageName,
				Version:      skill.Version,
				Digest:       skill.Digest,
				Skill:        skill.Skill,
				Resources:    resources,
			})
		}
	}
	return appskills.NewProviderSkillSource(packages)
}

func (s *Server) fetchProviderActionCatalog(ctx context.Context, id identity) ([]providerCatalogEntry, error) {
	catalog, err := s.fetchProviderCatalog(ctx, id)
	if err != nil {
		return nil, err
	}
	return catalog.Items, nil
}

type providerCatalogFetchResponse struct {
	Items []providerCatalogEntry `json:"items"`
}

type providerResourceMetadata struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

type providerResourceDiscoveryItem struct {
	Metadata providerResourceMetadata `json:"metadata"`
}

type providerResourceDiscoveryResponse struct {
	APIVersion string                          `json:"apiVersion"`
	Kind       string                          `json:"kind"`
	Resource   string                          `json:"resource"`
	Items      []providerResourceDiscoveryItem `json:"items"`
	Truncated  bool                            `json:"truncated"`
}

type providerResourceDiscoveryHTTPError struct {
	Status int
}

func (e providerResourceDiscoveryHTTPError) Error() string {
	return fmt.Sprintf("provider resource discovery returned status %d", e.Status)
}

// fetchProviderResourceMetadata asks the hub to list one exact action-bearing
// resource coordinate after it validates the signed context of the human
// caller who reached App Studio. The provider bearer authenticates App Studio
// to the hub; the proof is an opaque, short-lived delegation and no user bearer
// is forwarded.
func (s *Server) fetchProviderResourceMetadata(ctx context.Context, id identity, provider, apiVersion, kind, resource string) (providerResourceDiscoveryResponse, error) {
	if s != nil && s.providerResourceDiscoveryResolver != nil {
		response, err := s.providerResourceDiscoveryResolver(ctx, id, provider, apiVersion, kind, resource)
		if err != nil {
			return providerResourceDiscoveryResponse{}, err
		}
		if err := validateProviderResourceDiscoveryResponse(response, apiVersion, kind, resource); err != nil {
			return providerResourceDiscoveryResponse{}, err
		}
		return response, nil
	}
	base := strings.TrimRight(strings.TrimSpace(s.hubBase), "/")
	if base == "" {
		return providerResourceDiscoveryResponse{}, errors.New("provider resource discovery hub endpoint is not configured")
	}
	if strings.TrimSpace(id.actionProof) == "" {
		return providerResourceDiscoveryResponse{}, errors.New("provider resource discovery caller proof is missing")
	}
	provider = strings.TrimSpace(provider)
	apiVersion = strings.TrimSpace(apiVersion)
	resource = strings.TrimSpace(resource)
	if provider == "" || apiVersion == "" || resource == "" || strings.ContainsAny(provider+resource, "/\\\r\n\x00 ") {
		return providerResourceDiscoveryResponse{}, errors.New("provider resource discovery coordinate is invalid")
	}
	query := url.Values{}
	query.Set("apiVersion", apiVersion)
	endpoint := base + providerCatalogPath + "/" + url.PathEscape(provider) + "/resources/" + url.PathEscape(resource) + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerResourceDiscoveryResponse{}, fmt.Errorf("new provider resource discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	s.setHubCallerHeaders(req.Header, id)
	s.setHubActionProofHeader(req.Header, id)
	client := s.hubHTTPClient(providerCatalogCallTimeout)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("provider resource discovery redirect rejected")
	}
	resp, err := client.Do(req)
	if err != nil {
		return providerResourceDiscoveryResponse{}, fmt.Errorf("GET provider resource discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerCatalogMaxResponseBytes))
	if err != nil {
		return providerResourceDiscoveryResponse{}, fmt.Errorf("read provider resource discovery response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerResourceDiscoveryResponse{}, providerResourceDiscoveryHTTPError{Status: resp.StatusCode}
	}
	var response providerResourceDiscoveryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return providerResourceDiscoveryResponse{}, fmt.Errorf("decode provider resource discovery response: %w", err)
	}
	if err := validateProviderResourceDiscoveryResponse(response, apiVersion, kind, resource); err != nil {
		return providerResourceDiscoveryResponse{}, err
	}
	return response, nil
}

func validateProviderResourceDiscoveryResponse(response providerResourceDiscoveryResponse, apiVersion, kind, resource string) error {
	if response.APIVersion != apiVersion || response.Resource != resource || strings.TrimSpace(response.Kind) == "" || response.Kind != kind {
		return errors.New("provider resource discovery response did not match the requested coordinate")
	}
	return nil
}

func (s *Server) fetchProviderCatalog(ctx context.Context, id identity) (providerCatalogFetchResponse, error) {
	base := strings.TrimRight(strings.TrimSpace(s.hubBase), "/")
	if base == "" {
		return providerCatalogFetchResponse{}, errors.New("provider catalog hub endpoint is not configured")
	}
	endpoint := base + providerCatalogPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("new provider catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// A hub REST call, made as the provider; the caller's workspace selection
	// and name travel as headers the hub resolves the scope from.
	s.setHubCallerHeaders(req.Header, id)
	s.setHubActionProofHeader(req.Header, id)
	client := &http.Client{
		Timeout: providerCatalogCallTimeout,
		// Catalog lookup honors the same explicit development hub TLS setting
		// as the MCP and Project clients. Verification is enabled by default.
		Transport: projectMCPTransport(s.mcpInsecureSkipTLSVerify),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("provider action catalog redirect rejected")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerCatalogMaxResponseBytes))
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("read provider catalog response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerCatalogFetchResponse{}, fmt.Errorf("GET %s returned status %d", endpoint, resp.StatusCode)
	}
	var catalog providerCatalogFetchResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&catalog); err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("decode provider catalog response: %w", err)
	}
	return catalog, nil
}

func (s *Server) fetchProviderCatalogForProject(ctx context.Context, id identity, project *aiv1alpha1.Project) (providerCatalogFetchResponse, error) {
	base := strings.TrimRight(strings.TrimSpace(s.hubBase), "/")
	if base == "" {
		return providerCatalogFetchResponse{}, errors.New("provider catalog hub endpoint is not configured")
	}
	if !dataplane.IsClusterID(id.clusterID) || id.tenant != id.clusterID || strings.TrimSpace(id.orgUUID) == "" || strings.TrimSpace(id.workspaceUUID) == "" {
		return providerCatalogFetchResponse{}, errors.New("project catalog lookup requires authoritative tenant, org, workspace, and cluster scope")
	}
	token, err := s.projectIdentityToken(ctx, id, project)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("resolve Project identity for provider catalog: %w", err)
	}
	endpoint := base + providerCatalogPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("new Project provider catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Railgrid-Org", id.orgUUID)
	req.Header.Set("X-Railgrid-Workspace", id.workspaceUUID)
	req.Header.Set(dataplane.HeaderTenant, id.tenant)
	req.Header.Set(dataplane.HeaderCluster, id.clusterID)
	providerConfig, err := s.anonymousProjectRESTConfig(endpoint)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("configure Project provider catalog endpoint: %w", err)
	}
	transport, err := projectProviderActionTransport(providerConfig, s.mcpInsecureSkipTLSVerify)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("configure Project provider catalog TLS: %w", err)
	}
	client := &http.Client{
		Timeout:   providerCatalogCallTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("project provider catalog redirect rejected")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("GET Project provider catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerCatalogMaxResponseBytes))
	if err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("read Project provider catalog response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerCatalogFetchResponse{}, fmt.Errorf("GET Project provider catalog returned status %d", resp.StatusCode)
	}
	var catalog providerCatalogFetchResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&catalog); err != nil {
		return providerCatalogFetchResponse{}, fmt.Errorf("decode Project provider catalog response: %w", err)
	}
	return catalog, nil
}
