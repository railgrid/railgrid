/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package bindings holds the pure desired-state and status-fold logic for a
// Project's provider-resource bindings. Both consumers — the HTTP layer (as
// the calling user, read-through status) and the Project reconciler (as the
// provider identity, converging instances) — must agree on what an instance
// should look like and how its phase is read, so that logic lives here once.
package bindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// ProjectLabel attributes an instance back to its Project.
const ProjectLabel = "app-studio.faros.sh/project"

// Provider Actions fields are platform-owned instance inputs. The prefix is
// intentionally broader than the currently-known field list: a binding must
// never be able to smuggle a future reserved Actions field into an instance.
const ActionsFieldPrefix = "farosActions"

const (
	ActionsExchangeURLField = "farosActionsExchangeURL"
	ActionsBaseURLField     = "farosActionsBaseURL"
	ActionsCABundleField    = "farosActionsCABundle"
	ActionsTenantPathField  = "farosActionsTenantPath"
	ActionsOrgField         = "farosActionsOrg"
	ActionsWorkspaceField   = "farosActionsWorkspace"
	ActionsProjectField     = "farosActionsProject"
	ActionsProjectUIDField  = "farosActionsProjectUID"
	ActionsEnvironmentField = "farosActionsEnvironment"
	ActionsInstanceField    = "farosActionsInstance"
)

const tenantPathPrefix = "root:faros:tenants:"

// ActionsIdentity is the server-derived identity bound to one development
// instance. It is separate from ActionsTransport so action grants can be
// revoked without losing the instance's non-authorizing identity metadata.
type ActionsIdentity struct {
	TenantPath  string
	Org         string
	Workspace   string
	Project     string
	ProjectUID  string
	Environment string
	Instance    string
}

// ActionsRuntimeConfig contains operator-owned Provider Actions transport
// configuration. CABundleErr is retained by the API server until a project
// actually has an active grant, so unrelated actionless projects remain
// usable.
type ActionsRuntimeConfig struct {
	ExternalURL string
	CABundle    string
	CABundleErr error
}

// ActionsOverlay is the complete platform-owned overlay applied to binding
// values. Empty transport fields mean that the project is actionless or has a
// revoked grant; ApplyActionsOverlay removes any stale persisted values first.
type ActionsOverlay struct {
	ActionsIdentity
	ExchangeURL string
	BaseURL     string
	CABundle    string
}

// ValidateActionsExternalURL accepts only an operator-supplied HTTPS origin.
// Paths, query strings, fragments, credentials, and non-HTTPS schemes are not
// part of the configuration contract.
func ValidateActionsExternalURL(raw string) (string, error) {
	origin := strings.TrimRight(strings.TrimSpace(raw), "/")
	if origin == "" {
		return "", fmt.Errorf("FAROS_ACTIONS_EXTERNAL_URL is required for action-enabled development runtimes")
	}
	u, err := url.Parse(origin)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", fmt.Errorf("FAROS_ACTIONS_EXTERNAL_URL must be an absolute HTTPS URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("FAROS_ACTIONS_EXTERNAL_URL must use HTTPS")
	}
	return origin, nil
}

// ActionsTransportForOrigin derives the only two Provider Actions URLs that
// development workloads may use. Callers must not accept user-provided paths
// for either endpoint.
func ActionsTransportForOrigin(raw string) (ActionsOverlay, error) {
	origin, err := ValidateActionsExternalURL(raw)
	if err != nil {
		return ActionsOverlay{}, err
	}
	return ActionsOverlay{
		ExchangeURL: origin + "/api/provider-actions/workload/exchange",
		BaseURL:     origin + "/services/providers/app-studio",
	}, nil
}

// ParseTenantWorkspacePath validates and splits the canonical KCP tenant
// workspace path. A controller must have both segments before it can derive
// Provider Actions identity.
func ParseTenantWorkspacePath(raw string) (org, workspace string, err error) {
	path := strings.TrimSpace(raw)
	rest, ok := strings.CutPrefix(path, tenantPathPrefix)
	if !ok {
		return "", "", fmt.Errorf("tenant path %q does not use the canonical %s prefix", raw, tenantPathPrefix)
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("tenant path %q must identify exactly one organization and workspace", raw)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// HasActiveProviderActionGrant reports whether a Project contains at least one
// non-revoked action on a provider reference. The check is deliberately kept
// in the shared package so API and controller overlays make the same decision.
func HasActiveProviderActionGrant(p *aiv1alpha1.Project) bool {
	if p == nil {
		return false
	}
	for _, environment := range p.Spec.Environments {
		for _, binding := range environment.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference {
				continue
			}
			for _, action := range binding.AllowedActions {
				if strings.TrimSpace(action.Name) != "" && !action.Revoked {
					return true
				}
			}
		}
	}
	return false
}

// NewActionsOverlay builds a complete overlay from server-derived identity and
// operator-owned transport configuration. Identity is always applied; the
// transport fields are present only for an active grant.
func NewActionsOverlay(identity ActionsIdentity, config ActionsRuntimeConfig, activeGrant bool) (ActionsOverlay, error) {
	overlay := ActionsOverlay{ActionsIdentity: identity}
	if !activeGrant {
		return overlay, nil
	}
	transport, err := ActionsTransportForOrigin(config.ExternalURL)
	if err != nil {
		return ActionsOverlay{}, err
	}
	if config.CABundleErr != nil {
		return ActionsOverlay{}, fmt.Errorf("configured action CA bundle: %w", config.CABundleErr)
	}
	overlay.ExchangeURL = transport.ExchangeURL
	overlay.BaseURL = transport.BaseURL
	overlay.CABundle = config.CABundle
	return overlay, nil
}

// ApplyActionsOverlay returns a new map. It first removes every reserved
// farosActions* value, including fields unknown to this version, then adds the
// current server-derived identity and (when active) transport fields. Neither
// the persisted binding map nor the caller's map is mutated.
func ApplyActionsOverlay(values map[string]any, overlay ActionsOverlay) map[string]any {
	out := make(map[string]any, len(values)+len(actionsOverlayFields))
	for key, value := range values {
		if strings.HasPrefix(key, ActionsFieldPrefix) {
			continue
		}
		out[key] = value
	}
	for key, value := range actionsOverlayFieldsFor(overlay) {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

// ApplyActionsOverlayToBinding applies the pure value overlay while retaining
// the rest of the binding contract unchanged.
func ApplyActionsOverlayToBinding(binding aiv1alpha1.ProjectProviderBindingSpec, overlay ActionsOverlay) (aiv1alpha1.ProjectProviderBindingSpec, error) {
	values, err := Values(binding)
	if err != nil {
		return binding, err
	}
	raw, err := json.Marshal(ApplyActionsOverlay(values, overlay))
	if err != nil {
		return binding, fmt.Errorf("marshal provider binding %q values: %w", binding.Name, err)
	}
	binding.Values.Raw = raw
	return binding, nil
}

var actionsOverlayFields = map[string]struct{}{
	ActionsExchangeURLField: {},
	ActionsBaseURLField:     {},
	ActionsCABundleField:    {},
	ActionsTenantPathField:  {},
	ActionsOrgField:         {},
	ActionsWorkspaceField:   {},
	ActionsProjectField:     {},
	ActionsProjectUIDField:  {},
	ActionsEnvironmentField: {},
	ActionsInstanceField:    {},
}

func actionsOverlayFieldsFor(overlay ActionsOverlay) map[string]string {
	return map[string]string{
		ActionsExchangeURLField: overlay.ExchangeURL,
		ActionsBaseURLField:     overlay.BaseURL,
		ActionsCABundleField:    overlay.CABundle,
		ActionsTenantPathField:  overlay.TenantPath,
		ActionsOrgField:         overlay.Org,
		ActionsWorkspaceField:   overlay.Workspace,
		ActionsProjectField:     overlay.Project,
		ActionsProjectUIDField:  overlay.ProjectUID,
		ActionsEnvironmentField: overlay.Environment,
		ActionsInstanceField:    overlay.Instance,
	}
}

// Identity annotations bridging the CR keyspace to the workspace/store
// keyspace (reconcilers only know the cluster; store scopes are keyed by the
// org/workspace UUIDs the hub derives from the tenant path). Stamped by the
// API layer at resource creation.
const (
	OrgUUIDAnnotation       = "ai.faros.sh/org-uuid"
	WorkspaceUUIDAnnotation = "ai.faros.sh/workspace-uuid"
)

// GVR derives the instance GroupVersionResource from a binding's resourceRef
// (recorded from Template.spec.instanceCRD at bind time — self-contained, no
// Template read needed).
func GVR(ref *aiv1alpha1.ProjectProviderResourceReference) (schema.GroupVersionResource, error) {
	if ref == nil {
		return schema.GroupVersionResource{}, fmt.Errorf("resourceRef is required")
	}
	gv, err := schema.ParseGroupVersion(strings.TrimSpace(ref.APIVersion))
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	resource := strings.TrimSpace(ref.Resource)
	if resource == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("resourceRef.resource is required")
	}
	return gv.WithResource(resource), nil
}

// Values decodes the binding's raw values into the instance spec fields.
func Values(binding aiv1alpha1.ProjectProviderBindingSpec) (map[string]any, error) {
	if len(binding.Values.Raw) == 0 {
		return map[string]any{}, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		return nil, fmt.Errorf("decode provider binding %q values: %w", binding.Name, err)
	}
	return values, nil
}

// MergeProviderSpec overlays desired binding values onto an observed provider
// spec without erasing fields that the provider computes. Maps merge
// recursively, scalar and list values are replaced by the explicit desired
// value, and a top-level nil is an explicit desired value. Platform-owned
// Provider Actions fields are removed from the observed map first so a grant
// revocation or action removal cannot leave stale transport/identity values on
// the instance. The inputs and all nested values remain untouched.
func MergeProviderSpec(observed, desired map[string]any) map[string]any {
	merged := make(map[string]any, len(observed)+len(desired))
	for key, value := range observed {
		if strings.HasPrefix(key, ActionsFieldPrefix) {
			continue
		}
		merged[key] = runtime.DeepCopyJSONValue(value)
	}
	mergeProviderSpecMap(merged, desired)
	return merged
}

func mergeProviderSpecMap(dst, desired map[string]any) {
	for key, desiredValue := range desired {
		desiredMap, desiredIsMap := desiredValue.(map[string]any)
		if !desiredIsMap {
			dst[key] = runtime.DeepCopyJSONValue(desiredValue)
			continue
		}
		observedMap, observedIsMap := dst[key].(map[string]any)
		if !observedIsMap {
			observedMap = map[string]any{}
		}
		mergedMap := make(map[string]any, len(observedMap)+len(desiredMap))
		for nestedKey, nestedValue := range observedMap {
			mergedMap[nestedKey] = runtime.DeepCopyJSONValue(nestedValue)
		}
		mergeProviderSpecMap(mergedMap, desiredMap)
		dst[key] = mergedMap
	}
}

// ResourceName resolves the instance name: explicit resourceRef.name, then a
// "name" value, then "<project>-<binding>".
func ResourceName(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, values map[string]any) string {
	if binding.ResourceRef != nil && strings.TrimSpace(binding.ResourceRef.Name) != "" {
		return strings.TrimSpace(binding.ResourceRef.Name)
	}
	if name, ok := values["name"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	projectName := ""
	if p != nil {
		projectName = strings.TrimSpace(p.Name)
	}
	bindingName := strings.TrimSpace(binding.Name)
	if projectName == "" || bindingName == "" {
		return ""
	}
	return projectName + "-" + bindingName
}

// OwnerRef points an instance back at its Project so kcp garbage-collects it
// with the Project even if the finalizer never ran.
func OwnerRef(p *aiv1alpha1.Project) *metav1.OwnerReference {
	if p == nil || p.UID == "" || strings.TrimSpace(p.Name) == "" {
		return nil
	}
	controller := true
	return &metav1.OwnerReference{
		APIVersion: aiv1alpha1.SchemeGroupVersion.String(),
		Kind:       "Project",
		Name:       p.Name,
		UID:        p.UID,
		Controller: &controller,
	}
}

// InvalidBindingError marks a binding whose desired state cannot be computed
// at all (bad ref, undecodable values, no name) — retrying cannot help, only
// a spec change can.
type InvalidBindingError struct{ Err error }

func (e *InvalidBindingError) Error() string { return e.Err.Error() }
func (e *InvalidBindingError) Unwrap() error { return e.Err }

// IsInvalidBinding reports whether err (anywhere in its chain) is an
// InvalidBindingError.
func IsInvalidBinding(err error) bool {
	var invalid *InvalidBindingError
	return errors.As(err, &invalid)
}

// Desired builds the desired instance object for a binding. Returns the GVR
// alongside so callers can address the right resource. Errors are
// InvalidBindingError — the spec, not the world, is wrong.
func Desired(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	gvr, err := GVR(binding.ResourceRef)
	if err != nil {
		return nil, schema.GroupVersionResource{}, &InvalidBindingError{Err: err}
	}
	values, err := Values(binding)
	if err != nil {
		return nil, schema.GroupVersionResource{}, &InvalidBindingError{Err: err}
	}
	name := ResourceName(p, binding, values)
	if name == "" {
		return nil, schema.GroupVersionResource{}, &InvalidBindingError{Err: fmt.Errorf("provider binding %q has no resource name", binding.Name)}
	}
	spec := map[string]any{}
	maps.Copy(spec, values)
	want := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": binding.ResourceRef.APIVersion,
			"kind":       binding.ResourceRef.Kind,
			"metadata": map[string]any{
				"name": name,
				"labels": map[string]any{
					ProjectLabel: p.Name,
				},
			},
			"spec": spec,
		},
	}
	if owner := OwnerRef(p); owner != nil {
		want.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}
	return want, gvr, nil
}

// Phase reads an instance's phase: status.phase, then the Ready condition,
// then state=ACTIVE. Empty when nothing is published yet.
func Phase(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); strings.TrimSpace(phase) != "" {
		return strings.TrimSpace(phase)
	}
	if conditionStatus, ok := conditionStatus(obj, "Ready"); ok {
		if strings.EqualFold(conditionStatus, "True") {
			return "Ready"
		}
		return "Pending"
	}
	if state, _, _ := unstructured.NestedString(obj.Object, "status", "state"); strings.EqualFold(strings.TrimSpace(state), "ACTIVE") {
		return "Ready"
	}
	return ""
}

func conditionStatus(obj *unstructured.Unstructured, conditionType string) (string, bool) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawType, _ := condition["type"].(string)
		if rawType != conditionType {
			continue
		}
		status, _ := condition["status"].(string)
		return strings.TrimSpace(status), strings.TrimSpace(status) != ""
	}
	return "", false
}

// StatusFromObject folds one fetched instance (nil = not found → Pending)
// into the binding's status entry.
func StatusFromObject(binding aiv1alpha1.ProjectProviderBindingSpec, obj *unstructured.Unstructured) aiv1alpha1.ProjectProviderBindingStatus {
	status := aiv1alpha1.ProjectProviderBindingStatus{
		Name:     binding.Name,
		Provider: binding.Provider,
	}
	if obj == nil {
		status.Phase = "Pending"
		return status
	}
	status.Phase = Phase(obj)
	if previewURL, _, _ := unstructured.NestedString(obj.Object, "status", "previewURL"); previewURL != "" {
		status.PreviewURL = previewURL
	}
	if url, _, _ := unstructured.NestedString(obj.Object, "status", "url"); url != "" {
		status.URL = url
	}
	if outputs, ok := NestedStringMap(obj.Object, "status", "outputs"); ok {
		status.Outputs = outputs
	}
	return status
}

// InvalidStatus is the fold for a binding whose desired state cannot even be
// computed (bad ref, bad values, no name).
func InvalidStatus(binding aiv1alpha1.ProjectProviderBindingSpec) aiv1alpha1.ProjectProviderBindingStatus {
	return aiv1alpha1.ProjectProviderBindingStatus{
		Name:     binding.Name,
		Provider: binding.Provider,
		Phase:    "Invalid",
	}
}

// FoldEnvironment assembles one environment's status from its per-binding
// statuses (the environment phase is the first non-empty binding phase).
func FoldEnvironment(env aiv1alpha1.ProjectEnvironmentSpec, bindingStatuses []aiv1alpha1.ProjectProviderBindingStatus) aiv1alpha1.ProjectEnvironmentStatus {
	envStatus := aiv1alpha1.ProjectEnvironmentStatus{
		Name:     env.Name,
		Mode:     env.Mode,
		Bindings: bindingStatuses,
	}
	for _, binding := range bindingStatuses {
		if envStatus.Phase == "" && binding.Phase != "" {
			envStatus.Phase = binding.Phase
		}
	}
	return envStatus
}

// MergeEnvironmentStatuses overlays live environment statuses onto the
// existing list, preserving entries (and order) the live set doesn't cover.
func MergeEnvironmentStatuses(existing, live []aiv1alpha1.ProjectEnvironmentStatus) []aiv1alpha1.ProjectEnvironmentStatus {
	liveByName := map[string]aiv1alpha1.ProjectEnvironmentStatus{}
	for _, st := range live {
		liveByName[st.Name] = st
	}
	out := make([]aiv1alpha1.ProjectEnvironmentStatus, 0, len(existing)+len(liveByName))
	for _, st := range existing {
		if liveStatus, ok := liveByName[st.Name]; ok {
			out = append(out, liveStatus)
			delete(liveByName, st.Name)
			continue
		}
		out = append(out, st)
	}
	for _, st := range liveByName {
		out = append(out, st)
	}
	return out
}

// NestedStringMap reads a map[string]string from an unstructured object,
// tolerating map[string]any with string values.
func NestedStringMap(obj map[string]any, fields ...string) (map[string]string, bool) {
	raw, ok, _ := unstructured.NestedStringMap(obj, fields...)
	if ok {
		return raw, true
	}
	values, ok, _ := unstructured.NestedMap(obj, fields...)
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	for key, value := range values {
		if s, ok := value.(string); ok {
			out[key] = s
		}
	}
	return out, len(out) > 0
}
