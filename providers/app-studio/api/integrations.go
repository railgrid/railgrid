/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	projectIntegrationDefaultEnvironment  = "development"
	projectProviderActionMaxResponseBytes = 4 << 20
)

// projectProviderActionCallTimeout is separate from the assistant/MCP
// timeout. Keeping the action gateway budget independent makes it possible to
// tune or test this synchronous forwarding path without changing assistant
// execution semantics.
var projectProviderActionCallTimeout = 2 * time.Minute

var projectIntegrationIdentifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)

// projectIntegrationAddRequest is intentionally generic: the binding
// contract can point at resources owned by any provider. Invocation is
// forwarded to the hub's provider-actions gateway; App Studio does not carry
// provider-specific adapters or credentials.
type projectIntegrationAddRequest struct {
	Environment     string                                       `json:"environment,omitempty"`
	Alias           string                                       `json:"alias"`
	Provider        string                                       `json:"provider"`
	Kind            aiv1alpha1.ProjectBindingKind                `json:"kind,omitempty"`
	ResourceRef     *aiv1alpha1.ProjectProviderResourceReference `json:"resourceRef"`
	ConsentAccepted bool                                         `json:"consentAccepted,omitempty"`
	AllowedActions  []aiv1alpha1.ProjectProviderActionSpec       `json:"allowedActions,omitempty"`
	// Actions is accepted as a concise alias for clients that use the action
	// terminology directly. It is normalized into AllowedActions before the
	// Project is persisted.
	Actions []aiv1alpha1.ProjectProviderActionSpec `json:"actions,omitempty"`
}

type projectIntegrationPatchRequest struct {
	ConsentAccepted bool                                   `json:"consentAccepted,omitempty"`
	AllowedActions  []aiv1alpha1.ProjectProviderActionSpec `json:"allowedActions,omitempty"`
	Actions         []aiv1alpha1.ProjectProviderActionSpec `json:"actions,omitempty"`
}

type projectIntegrationView struct {
	Environment    string                                       `json:"environment"`
	Alias          string                                       `json:"alias"`
	Provider       string                                       `json:"provider"`
	Kind           aiv1alpha1.ProjectBindingKind                `json:"kind"`
	ResourceRef    *aiv1alpha1.ProjectProviderResourceReference `json:"resourceRef,omitempty"`
	AllowedActions []aiv1alpha1.ProjectProviderActionSpec       `json:"allowedActions,omitempty"`
	Phase          string                                       `json:"phase,omitempty"`
}

type projectIntegrationInvokeRequest struct {
	Action        string          `json:"action"`
	ActionVersion string          `json:"actionVersion,omitempty"`
	Version       string          `json:"version,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
}

type projectProviderActionError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// projectProviderActionEnvelope is the stable contract shared by the hub and
// App Studio. The result is kept raw so provider actions can choose their own
// bounded JSON result without App Studio reinterpreting it.
type projectProviderActionEnvelope struct {
	RequestID     string                                       `json:"requestID"`
	Provider      string                                       `json:"provider"`
	Action        string                                       `json:"action"`
	ActionVersion string                                       `json:"actionVersion"`
	ResourceRef   *aiv1alpha1.ProjectProviderResourceReference `json:"resourceRef"`
	Result        json.RawMessage                              `json:"result,omitempty"`
	Error         *projectProviderActionError                  `json:"error,omitempty"`
}

func (s *Server) addProjectIntegration(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req projectIntegrationAddRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Environment) == "" {
		req.Environment = projectIntegrationDefaultEnvironment
	}
	if err := validateProjectIntegrationAlias(req.Alias); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(req.Provider) == "" {
		writeError(w, newValidationError("provider is required"))
		return
	}
	if req.Kind != "" && req.Kind != aiv1alpha1.ProjectBindingKindProviderReference {
		writeError(w, newValidationError("integrations must use kind providerReference"))
		return
	}
	if req.ResourceRef == nil {
		writeError(w, newValidationError("resourceRef is required"))
		return
	}
	ref := normalizeIntegrationResourceRef(req.ResourceRef)
	if err := validateProviderReferenceRef(ref); err != nil {
		writeError(w, err)
		return
	}
	actions, err := normalizeProjectIntegrationActions(append(req.AllowedActions, req.Actions...))
	if err != nil {
		writeError(w, err)
		return
	}
	if len(actions) == 0 {
		writeError(w, newValidationError("at least one allowed action is required"))
		return
	}
	actions, err = s.verifyProjectActionGrants(r.Context(), id, req.Provider, ref, actions, req.ConsentAccepted)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := observeProjectProviderReference(r.Context(), c, aiv1alpha1.ProjectProviderBindingSpec{
		Name: req.Alias, Provider: req.Provider,
		Kind: aiv1alpha1.ProjectBindingKindProviderReference, ResourceRef: ref,
	}); err != nil {
		writeError(w, err)
		return
	}

	next := project.DeepCopy()
	env := ensureProjectIntegrationEnvironment(next, req.Environment)
	for _, existing := range env.Bindings {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(req.Alias)) {
			writeStatus(w, http.StatusConflict, "Conflict", fmt.Sprintf("project integration %q already exists", req.Alias))
			return
		}
	}
	env.Bindings = append(env.Bindings, aiv1alpha1.ProjectProviderBindingSpec{
		Name: req.Alias, Provider: strings.TrimSpace(req.Provider),
		Kind:        aiv1alpha1.ProjectBindingKindProviderReference,
		ResourceRef: ref, AllowedActions: actions,
	})
	newBinding := env.Bindings[len(env.Bindings)-1]
	if _, err := s.projectTemplateBindingContext(next, id); err != nil {
		// Validate the trusted runtime context before persisting the grant. A
		// missing/invalid external origin must not leave an action grant in the
		// Project after this request reports failure.
		writeError(w, newValidationError(err.Error()))
		return
	}
	updated, err := c.Projects().Update(r.Context(), next, metav1.UpdateOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	// Provider-resource instances are converged exclusively by the Project
	// controller. Integration CRUD records the non-owning reference and reads
	// the target below for truthful status; it must not synchronously create or
	// update a provider-owned object.
	phase := projectProviderBindingStatus(r.Context(), c, updated, newBinding, id).Phase
	if phase == "" {
		phase = "Pending"
	}
	writeJSON(w, http.StatusCreated, projectIntegrationViewForBinding(req.Environment, newBinding, phase))
}

func (s *Server) listProjectIntegrations(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	project = projectWithLiveBindingStatus(r.Context(), c, project, id)
	items := make([]projectIntegrationView, 0)
	statusByKey := map[string]string{}
	for _, envStatus := range project.Status.Environments {
		for _, bindingStatus := range envStatus.Bindings {
			statusByKey[envStatus.Name+"\x00"+bindingStatus.Name] = bindingStatus.Phase
		}
	}
	for _, env := range project.Spec.Environments {
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference {
				continue
			}
			items = append(items, projectIntegrationViewForBinding(env.Name, binding, statusByKey[env.Name+"\x00"+binding.Name]))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Environment != items[j].Environment {
			return items[i].Environment < items[j].Environment
		}
		return items[i].Alias < items[j].Alias
	})
	writeJSON(w, http.StatusOK, ListResponse[projectIntegrationView]{Items: items})
}

func (s *Server) removeProjectIntegration(w http.ResponseWriter, r *http.Request) {
	c, _, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	alias := strings.TrimSpace(muxVars(r)["integration"])
	if alias == "" {
		writeError(w, newValidationError("integration alias is required"))
		return
	}
	next := project.DeepCopy()
	removed := false
	for i := range next.Spec.Environments {
		env := &next.Spec.Environments[i]
		kept := env.Bindings[:0]
		for _, binding := range env.Bindings {
			if binding.Kind == aiv1alpha1.ProjectBindingKindProviderReference && strings.EqualFold(strings.TrimSpace(binding.Name), alias) {
				removed = true
				continue
			}
			kept = append(kept, binding)
		}
		env.Bindings = kept
	}
	if !removed {
		writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("project integration %q was not found", alias))
		return
	}
	_, err := c.Projects().Update(r.Context(), next, metav1.UpdateOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	// Removal is a Project spec mutation only. The Project controller observes
	// the changed grant set and clears/converges the owning runtime binding.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) patchProjectIntegration(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	alias := strings.TrimSpace(muxVars(r)["integration"])
	var req projectIntegrationPatchRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	actions, err := normalizeProjectIntegrationActions(append(req.AllowedActions, req.Actions...))
	if err != nil {
		writeError(w, err)
		return
	}
	if len(actions) == 0 {
		writeError(w, newValidationError("at least one allowed action is required"))
		return
	}
	next := project.DeepCopy()
	for i := range next.Spec.Environments {
		for j := range next.Spec.Environments[i].Bindings {
			binding := &next.Spec.Environments[i].Bindings[j]
			if binding.Kind == aiv1alpha1.ProjectBindingKindProviderReference && strings.EqualFold(strings.TrimSpace(binding.Name), alias) {
				ref := normalizeIntegrationResourceRef(binding.ResourceRef)
				if err := validateProviderReferenceRef(ref); err != nil {
					writeError(w, err)
					return
				}
				merged, mergeErr := s.mergeProjectIntegrationActions(r.Context(), id, binding.Provider, ref, binding.AllowedActions, actions, req.ConsentAccepted)
				if mergeErr != nil {
					writeError(w, mergeErr)
					return
				}
				binding.AllowedActions = merged
				if _, contextErr := s.projectTemplateBindingContext(next, id); contextErr != nil {
					// The reactivation path is another grant mutation. Keep it
					// transactional with respect to the Project when the runtime
					// exchange origin is unavailable; revocations have no active grant
					// and therefore pass this check and still clear the runtime below.
					writeError(w, newValidationError(contextErr.Error()))
					return
				}
				updated, updateErr := c.Projects().Update(r.Context(), next, metav1.UpdateOptions{})
				if updateErr != nil {
					writeProjectError(w, updateErr)
					return
				}
				// The target status is read through after the Project write. Runtime
				// convergence is asynchronous and belongs to the Project controller.
				phase := projectProviderBindingStatus(r.Context(), c, updated, updated.Spec.Environments[i].Bindings[j], id).Phase
				if phase == "" {
					phase = "Pending"
				}
				writeJSON(w, http.StatusOK, projectIntegrationViewForBinding(next.Spec.Environments[i].Name, updated.Spec.Environments[i].Bindings[j], phase))
				return
			}
		}
	}
	writeStatus(w, http.StatusNotFound, "NotFound", fmt.Sprintf("project integration %q was not found", alias))
}

func (s *Server) invokeProjectIntegration(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	alias := strings.TrimSpace(muxVars(r)["integration"])
	var req projectIntegrationInvokeRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Action) == "" {
		// The action path form is useful for generated clients that keep the
		// action outside the JSON body. The body form remains canonical and can
		// carry an explicit actionVersion for version negotiation.
		req.Action = strings.TrimSpace(muxVars(r)["action"])
	}
	requestedVersion := req.ActionVersion
	if strings.TrimSpace(requestedVersion) == "" {
		requestedVersion = req.Version
	} else if strings.TrimSpace(req.Version) != "" && !strings.EqualFold(strings.TrimSpace(requestedVersion), strings.TrimSpace(req.Version)) {
		writeError(w, newValidationError("actionVersion and version do not match"))
		return
	}
	name, version, err := normalizeIntegrationAction(req.Action, requestedVersion)
	if err != nil {
		writeError(w, err)
		return
	}
	envName, binding, err := findProjectIntegration(project, alias)
	if err != nil {
		writeError(w, err)
		return
	}
	if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || binding.ResourceRef == nil {
		writeError(w, newValidationError(fmt.Sprintf("integration %q is not a provider reference", alias)))
		return
	}
	allowed := false
	revoked := false
	var schemaDigest string
	var grantedBy string
	var grantedAt *metav1.Time
	for _, declared := range binding.AllowedActions {
		if strings.EqualFold(strings.TrimSpace(declared.Name), name) && strings.EqualFold(strings.TrimSpace(declared.Version), version) {
			allowed = true
			revoked = declared.Revoked
			schemaDigest = strings.TrimSpace(declared.SchemaDigest)
			grantedBy = strings.TrimSpace(declared.GrantedBy)
			grantedAt = declared.GrantedAt
			break
		}
	}
	if !allowed {
		writeStatus(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("integration %q does not allow action %s/%s", alias, name, version))
		return
	}
	if revoked {
		writeStatus(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("integration %q action %s/%s has been revoked", alias, name, version))
		return
	}
	if !projectActionSchemaDigestRE.MatchString(schemaDigest) || grantedBy == "" || grantedAt == nil || grantedAt.IsZero() {
		writeStatus(w, http.StatusForbidden, "Forbidden", fmt.Sprintf("integration %q action %s/%s has an incomplete catalog grant", alias, name, version))
		return
	}
	ref := normalizeIntegrationResourceRef(binding.ResourceRef)
	if err := validateProviderReferenceRef(ref); err != nil {
		writeError(w, err)
		return
	}
	input, err := normalizeProjectProviderActionInput(req.Input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.verifyProjectActionDigestForInvoke(r.Context(), id, binding.Provider, ref, name, version, schemaDigest); err != nil {
		var drift errProjectActionDigestDrift
		if errors.As(err, &drift) {
			writeStatus(w, http.StatusConflict, "Conflict", drift.Error())
			return
		}
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", err.Error())
		return
	}

	statusCode, envelope, err := s.forwardProjectProviderAction(r, id, binding.Provider, name, version, schemaDigest, ref, input)
	if err != nil {
		writeProviderActionForwardError(w, statusCode, envelope, err)
		return
	}
	if envelope.RequestID != "" {
		w.Header().Set("X-Request-ID", envelope.RequestID)
	}
	writeJSON(w, statusCode, envelope)
	_ = envName // environment is included in binding lookup for ambiguity checks
}

func validateProjectIntegrationAlias(alias string) error {
	if !projectIntegrationIdentifierRE.MatchString(strings.TrimSpace(alias)) {
		return newValidationError("integration alias must be 1-63 characters and contain only letters, numbers, '_' or '-'")
	}
	return nil
}

func normalizeIntegrationResourceRef(ref *aiv1alpha1.ProjectProviderResourceReference) *aiv1alpha1.ProjectProviderResourceReference {
	if ref == nil {
		return nil
	}
	out := *ref
	return &out
}

func validateProviderReferenceRef(ref *aiv1alpha1.ProjectProviderResourceReference) error {
	if ref == nil {
		return newValidationError("resourceRef is required")
	}
	if strings.TrimSpace(ref.Name) == "" {
		return newValidationError("resourceRef.name is required")
	}
	if strings.TrimSpace(ref.APIVersion) == "" || strings.TrimSpace(ref.Kind) == "" || strings.TrimSpace(ref.Resource) == "" {
		return newValidationError("resourceRef must include apiVersion, kind, resource, and name")
	}
	if _, err := projectProviderResourceGVR(ref); err != nil {
		return newValidationError("invalid resourceRef: " + err.Error())
	}
	return nil
}

func normalizeProjectIntegrationActions(actions []aiv1alpha1.ProjectProviderActionSpec) ([]aiv1alpha1.ProjectProviderActionSpec, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	out := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(actions))
	seen := map[string]struct{}{}
	for _, action := range actions {
		name, version, err := normalizeIntegrationAction(action.Name, action.Version)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(name + "\x00" + version)
		if _, exists := seen[key]; exists {
			return nil, newValidationError(fmt.Sprintf("duplicate allowed action %s/%s", name, version))
		}
		digest := strings.TrimSpace(action.SchemaDigest)
		// A revoke is keyed by the stored name/version and can proceed with a
		// stale or omitted client digest. Active grants still require the
		// catalog-shaped digest before they can be created or reactivated.
		if !action.Revoked && !projectActionSchemaDigestRE.MatchString(digest) {
			return nil, newValidationError(fmt.Sprintf("allowed action %s/%s must include schemaDigest sha256:<64 lowercase hex digits>", name, version))
		}
		seen[key] = struct{}{}
		// Grant and revoke audit fields are deliberately not copied: those
		// server-owned values are written only by the transition merge.
		out = append(out, aiv1alpha1.ProjectProviderActionSpec{Name: name, Version: version, SchemaDigest: digest, Revoked: action.Revoked})
	}
	return out, nil
}

func normalizeIntegrationAction(action, explicitVersion string) (string, string, error) {
	action = strings.TrimSpace(action)
	explicitVersion = strings.TrimSpace(explicitVersion)
	if action == "" {
		return "", "", newValidationError("action is required")
	}
	name, version := action, explicitVersion
	if slash := strings.IndexByte(action, '/'); slash >= 0 {
		if strings.Count(action, "/") != 1 {
			return "", "", newValidationError("action must be name/version")
		}
		parsedVersion := action[slash+1:]
		name = action[:slash]
		if version != "" && !strings.EqualFold(version, parsedVersion) {
			return "", "", newValidationError("action version does not match action name/version")
		}
		version = parsedVersion
	}
	if !projectIntegrationIdentifierRE.MatchString(name) || !projectIntegrationIdentifierRE.MatchString(version) {
		return "", "", newValidationError("action name and version must be non-empty identifiers")
	}
	return name, version, nil
}

func findProjectIntegration(project *aiv1alpha1.Project, alias string) (string, aiv1alpha1.ProjectProviderBindingSpec, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", aiv1alpha1.ProjectProviderBindingSpec{}, newValidationError("integration alias is required")
	}
	var found *aiv1alpha1.ProjectProviderBindingSpec
	envName := ""
	for _, env := range project.Spec.Environments {
		for i := range env.Bindings {
			binding := &env.Bindings[i]
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || !strings.EqualFold(strings.TrimSpace(binding.Name), alias) {
				continue
			}
			if found != nil {
				return "", aiv1alpha1.ProjectProviderBindingSpec{}, newValidationError(fmt.Sprintf("integration alias %q is ambiguous across environments", alias))
			}
			copy := binding.DeepCopy()
			found = copy
			envName = env.Name
		}
	}
	if found == nil {
		return "", aiv1alpha1.ProjectProviderBindingSpec{}, apierrors.NewNotFound(schema.GroupResource{Group: aiv1alpha1.GroupName, Resource: "project integrations"}, alias)
	}
	return envName, *found, nil
}

func ensureProjectIntegrationEnvironment(project *aiv1alpha1.Project, name string) *aiv1alpha1.ProjectEnvironmentSpec {
	for i := range project.Spec.Environments {
		if strings.EqualFold(strings.TrimSpace(project.Spec.Environments[i].Name), strings.TrimSpace(name)) {
			return &project.Spec.Environments[i]
		}
	}
	project.Spec.Environments = append(project.Spec.Environments, aiv1alpha1.ProjectEnvironmentSpec{
		Name: name, Mode: aiv1alpha1.ProjectEnvironmentModeLive, Promotion: aiv1alpha1.ProjectPromotionManual,
	})
	return &project.Spec.Environments[len(project.Spec.Environments)-1]
}

func projectIntegrationViewForBinding(environment string, binding aiv1alpha1.ProjectProviderBindingSpec, phase string) projectIntegrationView {
	actions := append([]aiv1alpha1.ProjectProviderActionSpec(nil), binding.AllowedActions...)
	return projectIntegrationView{
		Environment: environment, Alias: binding.Name, Provider: binding.Provider,
		Kind: binding.Kind, ResourceRef: binding.ResourceRef, AllowedActions: actions, Phase: phase,
	}
}

// providerActionForbiddenInputKeys is deliberately provider-neutral. The
// binding owns the provider and resource coordinates; callers may only supply
// action-specific data. Reject these keys recursively so a nested options map
// cannot smuggle a second target, credential, or backend topology through an
// otherwise generic action.
var providerActionForbiddenInputKeys = map[string]struct{}{
	"action": {}, "actionversion": {}, "version": {},
	"provider": {}, "providername": {}, "providerref": {}, "providerreference": {},
	"providerurl": {}, "providerbaseurl": {}, "huburl": {},
	"resourceref": {}, "resource": {}, "resourceid": {}, "resourcename": {},
	"tableref": {}, "apiversion": {}, "kind": {}, "namespace": {},
	"tenant": {}, "tenantpath": {}, "cluster": {}, "clusterid": {},
	"credential": {}, "credentials": {}, "token": {}, "accesstoken": {},
	"bearertoken": {}, "authorization": {},
}

func normalizeProjectProviderActionInput(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, newValidationError("invalid provider action input: " + err.Error())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, newValidationError("invalid provider action input: multiple JSON values")
		}
		return nil, newValidationError("invalid provider action input: " + err.Error())
	}
	if err := validateProjectProviderActionInputValue(value, "input"); err != nil {
		return nil, err
	}
	return json.RawMessage(trimmed), nil
}

func validateProjectProviderActionInputValue(value any, path string) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			canonical := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(key)))
			if _, forbidden := providerActionForbiddenInputKeys[canonical]; forbidden {
				return newValidationError(fmt.Sprintf("provider action input field %q cannot override bound provider or resource context", path+"."+key))
			}
			if err := validateProjectProviderActionInputValue(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range value {
			if err := validateProjectProviderActionInputValue(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

type projectProviderActionInvokeRequest struct {
	Input json.RawMessage `json:"input"`
}

// providerActionInvokeURL composes the data-plane action route on the target
// provider's embedded virtual workspace, reached through the hub backend
// proxy. The URL is the resource reference — cluster ID, resource, name, and
// verb all live in the path, so the provider authorizes exactly what was
// addressed and no identity travels in the body.
func providerActionInvokeURL(hubBase, provider, clusterID string, ref *aiv1alpha1.ProjectProviderResourceReference, action, version string) (string, error) {
	for field, value := range map[string]string{
		"provider": provider, "cluster": clusterID, "resource": ref.Resource, "resource name": ref.Name,
		"action": action, "version": version,
	} {
		value = strings.TrimSpace(value)
		if value == "" || value == "." || value == ".." || url.PathEscape(value) != value {
			return "", fmt.Errorf("provider action %s is not path-safe", field)
		}
	}
	return strings.TrimRight(hubBase, "/") + "/services/providers/" + provider +
		"/actions/clusters/" + clusterID + "/" + ref.Resource + "/" + ref.Name + "/" + action + "/" + version, nil
}

func (s *Server) forwardProjectProviderAction(r *http.Request, id identity, provider, action, version, schemaDigest string, ref *aiv1alpha1.ProjectProviderResourceReference, input json.RawMessage) (int, projectProviderActionEnvelope, error) {
	if _, err := validateActionsExternalURL(s.actionsExternalURL); err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), err
	}
	payload, err := json.Marshal(projectProviderActionInvokeRequest{Input: input})
	if err != nil {
		return http.StatusBadGateway, projectProviderActionEnvelope{}, fmt.Errorf("encode provider action request: %w", err)
	}
	endpoint, err := providerActionInvokeURL(s.hubBase, provider, id.clusterID, ref, action, version)
	if err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return http.StatusBadGateway, projectProviderActionEnvelope{}, fmt.Errorf("new provider action request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if id.tenantPath != "" {
		req.Header.Set("X-Faros-Tenant", id.tenantPath)
	}
	if id.clusterID != "" {
		req.Header.Set("X-Faros-Cluster", id.clusterID)
	}
	if id.orgUUID != "" {
		req.Header.Set("X-Faros-Org", id.orgUUID)
	}
	if id.workspaceUUID != "" {
		req.Header.Set("X-Faros-Workspace", id.workspaceUUID)
	}
	for _, header := range []string{"Idempotency-Key", "X-Request-ID", "X-Faros-Action-Deadline-Ms"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			req.Header.Set(header, value)
		}
	}

	transport, err := projectProviderActionTransport(s.actionsCABundle)
	if err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), fmt.Errorf("configure provider action transport: %w", err)
	}
	client := &http.Client{
		Timeout:   projectProviderActionCallTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("provider action redirect rejected")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, projectProviderActionMaxResponseBytes))
	if err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), fmt.Errorf("read provider action response: %w", err)
	}
	var envelope projectProviderActionEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), fmt.Errorf("decode provider action response: %w", err)
	}
	if err := normalizeAndValidateProviderActionEnvelope(&envelope, provider, action, version, ref, r.Header.Get("X-Request-ID")); err != nil {
		return http.StatusBadGateway, providerActionUnavailableEnvelope(provider, action, version, ref), err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, envelope, nil
	}
	if envelope.Error != nil {
		return http.StatusBadGateway, envelope, nil
	}
	return resp.StatusCode, envelope, nil
}

// projectProviderActionTransport is intentionally independent of the MCP
// transport. Provider Actions carry caller credentials and may reach a
// production hub, so this path always uses certificate verification and never
// retries with InsecureSkipVerify, even when the development MCP option is
// enabled for unrelated assistant calls. An explicitly configured CA bundle
// is appended to the system pool; it never replaces the host's normal trust.
func projectProviderActionTransport(caBundle string) (http.RoundTripper, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		if strings.TrimSpace(caBundle) != "" {
			return nil, errors.New("custom Provider Actions CA requires an HTTP transport with TLS configuration")
		}
		return http.DefaultTransport, nil
	}
	transport := base.Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	tlsConfig.InsecureSkipVerify = false
	if bundle := strings.TrimSpace(caBundle); bundle != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(bundle)) {
			return nil, errors.New("configured Provider Actions CA bundle contains no PEM certificates")
		}
		tlsConfig.RootCAs = roots
	}
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

func normalizeAndValidateProviderActionEnvelope(envelope *projectProviderActionEnvelope, provider, action, version string, ref *aiv1alpha1.ProjectProviderResourceReference, requestID string) error {
	if envelope == nil {
		return errors.New("provider action response was empty")
	}
	if envelope.RequestID == "" {
		envelope.RequestID = strings.TrimSpace(requestID)
	}
	if envelope.Provider == "" {
		envelope.Provider = provider
	}
	if envelope.Action == "" {
		envelope.Action = action
	}
	if envelope.ActionVersion == "" {
		envelope.ActionVersion = version
	}
	if envelope.ResourceRef == nil {
		envelope.ResourceRef = normalizeIntegrationResourceRef(ref)
	}
	if !strings.EqualFold(strings.TrimSpace(envelope.Provider), strings.TrimSpace(provider)) ||
		!strings.EqualFold(strings.TrimSpace(envelope.Action), strings.TrimSpace(action)) ||
		!strings.EqualFold(strings.TrimSpace(envelope.ActionVersion), strings.TrimSpace(version)) {
		return errors.New("provider action response identity did not match the bound action")
	}
	if !sameProjectProviderResourceReference(envelope.ResourceRef, ref) {
		return errors.New("provider action response resourceRef did not match the bound resource")
	}
	if envelope.Error == nil && len(envelope.Result) == 0 {
		return errors.New("provider action response contained neither result nor error")
	}
	if envelope.Error != nil && len(envelope.Result) != 0 {
		return errors.New("provider action response contained both result and error")
	}
	return nil
}

func sameProjectProviderResourceReference(left, right *aiv1alpha1.ProjectProviderResourceReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(left.Name) == strings.TrimSpace(right.Name) &&
		strings.TrimSpace(left.APIVersion) == strings.TrimSpace(right.APIVersion) &&
		strings.TrimSpace(left.Kind) == strings.TrimSpace(right.Kind) &&
		strings.TrimSpace(left.Resource) == strings.TrimSpace(right.Resource)
}

func providerActionUnavailableEnvelope(provider, action, version string, ref *aiv1alpha1.ProjectProviderResourceReference) projectProviderActionEnvelope {
	return projectProviderActionEnvelope{
		Provider: provider, Action: action, ActionVersion: version, ResourceRef: normalizeIntegrationResourceRef(ref),
		Error: &projectProviderActionError{Code: "provider_action_unavailable", Message: "provider action gateway is unavailable", Retryable: true},
	}
}

func writeProviderActionForwardError(w http.ResponseWriter, statusCode int, envelope projectProviderActionEnvelope, err error) {
	if envelope.Error != nil {
		if statusCode < 400 {
			statusCode = http.StatusBadGateway
		}
		writeJSON(w, statusCode, envelope)
		return
	}
	if statusCode < 400 {
		statusCode = http.StatusBadGateway
	}
	writeJSON(w, statusCode, providerActionUnavailableEnvelope(envelope.Provider, envelope.Action, envelope.ActionVersion, envelope.ResourceRef))
}

// muxVars is kept in one helper so handlers remain straightforward and tests
// can exercise them with a mux route exactly as production does.
func muxVars(r *http.Request) map[string]string {
	return mux.Vars(r)
}
