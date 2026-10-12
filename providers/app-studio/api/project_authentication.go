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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/hubapi"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-sdk/dataplane"
)

type projectAuthenticationEnvironment struct {
	Environment  string `json:"environment"`
	Instance     string `json:"instance"`
	Configured   bool   `json:"configured"`
	RuntimeReady bool   `json:"runtimeReady"`
	Reason       string `json:"reason,omitempty"`
}

type projectAuthenticationResponse struct {
	Enabled             bool                               `json:"enabled"`
	Supported           bool                               `json:"supported"`
	Environments        []projectAuthenticationEnvironment `json:"environments"`
	ApplicationVerified bool                               `json:"applicationVerified"`
	Message             string                             `json:"message"`
}

func projectAuthenticationSupported(info projectTemplateInfo) bool {
	properties, _ := info.ProductionSchema["properties"].(map[string]any)
	field, _ := properties[bindings.AuthEnabledField].(map[string]any)
	if field["type"] != "boolean" || info.Components["api"].WorkspacePath == "" {
		return false
	}
	for _, name := range []string{bindings.AuthAppIDField, bindings.AuthEnvironmentField, "railgridAuthInstanceUID"} {
		value, _ := properties[name].(map[string]any)
		if value["type"] != "string" {
			return false
		}
	}
	return true
}

func (s *Server) authenticationResponse(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (projectAuthenticationResponse, error) {
	response := projectAuthenticationResponse{Enabled: p.Spec.Authentication.Enabled, Environments: []projectAuthenticationEnvironment{}, Message: "Railgrid Auth identifies approved users. Application middleware and ownership checks must still be implemented and tested."}
	if p.Spec.Template != nil {
		info, err := fetchProjectTemplate(ctx, c, p.Spec.Template.Name)
		if err != nil {
			return response, err
		}
		response.Supported = projectAuthenticationSupported(info)
	}
	for _, env := range p.Spec.Environments {
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil || binding.Provider != "app-studio" {
				continue
			}
			values, err := bindings.Values(binding)
			if err != nil {
				return response, err
			}
			entry := projectAuthenticationEnvironment{Environment: env.Name, Instance: bindings.ResourceName(p, binding, values), Reason: "runtime_not_ready"}
			gvr, err := bindings.GVR(binding.ResourceRef)
			if err != nil {
				return response, err
			}
			resource := tenant.Resource{GVR: gvr, Kind: binding.ResourceRef.Kind, Plural: gvr.Resource}
			obj, err := c.Resource(resource, "").Get(ctx, entry.Instance, metav1.GetOptions{})
			if err == nil {
				enabled, _, _ := unstructured.NestedBool(obj.Object, "spec", "values", bindings.AuthEnabledField)
				audience, _, _ := unstructured.NestedString(obj.Object, "spec", "values", bindings.AuthAppIDField)
				observedEnvironment, _, _ := unstructured.NestedString(obj.Object, "spec", "values", bindings.AuthEnvironmentField)
				observedInstanceUID, _, _ := unstructured.NestedString(obj.Object, "spec", "values", "railgridAuthInstanceUID")
				entry.Configured = enabled == p.Spec.Authentication.Enabled && (!enabled || (audience == string(p.UID)+"/"+env.Name && observedEnvironment == env.Name && observedInstanceUID == string(obj.GetUID())))
				status := bindings.StatusFromObject(binding, obj)
				entry.RuntimeReady = entry.Configured && status.Phase == "Ready"
				if entry.RuntimeReady {
					entry.Reason = ""
				}
			}
			response.Environments = append(response.Environments, entry)
		}
	}
	return response, nil
}

func (s *Server) configureAuthentication(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, enabled bool) (*aiv1alpha1.Project, error) {
	allowed, err := s.authorizeCaller(ctx, id, dataplane.ResourceAttributes{Group: projectsGVR.Group, Version: projectsGVR.Version, Resource: projectsGVR.Resource, Name: p.Name, Verb: "update"})
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, apierrors.NewForbidden(schema.GroupResource{Group: projectsGVR.Group, Resource: projectsGVR.Resource}, p.Name, fmt.Errorf("project update permission required"))
	}
	current, err := c.Projects().Get(ctx, p.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if current.UID != p.UID {
		return nil, apierrors.NewConflict(projectsGVR.GroupResource(), p.Name, fmt.Errorf("project identity changed while configuring authentication"))
	}
	changed := current.Spec.Authentication.Enabled != enabled
	current.Spec.Authentication.Enabled = enabled
	for ei := range current.Spec.Environments {
		for bi := range current.Spec.Environments[ei].Bindings {
			b := current.Spec.Environments[ei].Bindings[bi]
			if b.Provider != "app-studio" {
				continue
			}
			b, err = bindings.ApplyAuthentication(current, current.Spec.Environments[ei].Name, b)
			if err != nil {
				return nil, err
			}
			changed = changed || !apiequality.Semantic.DeepEqual(current.Spec.Environments[ei].Bindings[bi], b)
			current.Spec.Environments[ei].Bindings[bi] = b
		}
	}
	if !changed {
		return current, nil
	}
	if enabled {
		if err := s.checkAuthenticationService(ctx); err != nil {
			return nil, err
		}
		if current.Spec.Template == nil {
			return nil, newValidationError("select a supported application template before enabling Railgrid Auth")
		}
		info, err := fetchProjectTemplate(ctx, c, current.Spec.Template.Name)
		if err != nil {
			return nil, err
		}
		if !projectAuthenticationSupported(info) {
			return nil, newValidationError("the selected template does not support Railgrid Auth; update the application template before enabling it")
		}
	}
	return c.Projects().Update(ctx, current, metav1.UpdateOptions{})
}

func (s *Server) getProjectAuthentication(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	response, err := s.authenticationResponse(r.Context(), c, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) setProjectAuthentication(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "enabled is required")
		return
	}
	p, err := s.configureAuthentication(r.Context(), c, id, p, *req.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := s.authenticationResponse(r.Context(), c, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func projectAuthenticationTools(s *Server) []projectAssistantTool {
	return []projectAssistantTool{
		projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "get_project_authentication", Description: "Inspect Railgrid Auth configuration and runtime readiness. Does not change the app or prove application ownership checks.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Risk: projectAssistantToolRiskRead, ParallelSafe: true}, call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			if s == nil || req.Project == nil {
				return "", fmt.Errorf("a configured Project is required")
			}
			allowed, err := s.authorizeCaller(ctx, req.Identity, dataplane.ResourceAttributes{
				Group: projectsGVR.Group, Version: projectsGVR.Version, Resource: projectsGVR.Resource,
				Name: req.Project.Name, Verb: "get",
			})
			if err != nil {
				return "", err
			}
			if !allowed {
				return "", apierrors.NewForbidden(
					schema.GroupResource{Group: projectsGVR.Group, Resource: projectsGVR.Resource},
					req.Project.Name,
					fmt.Errorf("project get permission required"),
				)
			}
			c, err := s.clientFor(req.Identity)
			if err != nil {
				return "", err
			}
			p, err := c.Projects().Get(ctx, req.Project.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			if p.UID != req.Project.UID {
				return "", fmt.Errorf("project identity changed")
			}
			response, err := s.authenticationResponse(ctx, c, p)
			return projectAssistantToolJSONResult(response, err)
		}},
		projectAssistantToolFunc{spec: projectAssistantToolSpec{Name: "configure_project_authentication", Description: "Enable or disable Railgrid Auth for this Project after a user requests authentication. Does not alter sharing or add membership; integrate the supported SDK and test backend ownership checks separately.", Parameters: json.RawMessage(`{"type":"object","properties":{"enabled":{"type":"boolean"}},"required":["enabled"],"additionalProperties":false}`), Risk: projectAssistantToolRiskRuntime}, call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			if s == nil || req.Project == nil {
				return "", fmt.Errorf("a configured Project is required")
			}
			enabled, ok := req.Arguments["enabled"].(bool)
			if !ok {
				return "", fmt.Errorf("enabled must be a boolean")
			}
			c, err := s.clientFor(req.Identity)
			if err != nil {
				return "", err
			}
			p, err := s.configureAuthentication(ctx, c, req.Identity, req.Project, enabled)
			if err != nil {
				return "", err
			}
			response, err := s.authenticationResponse(ctx, c, p)
			return projectAssistantToolJSONResult(response, err)
		}},
	}
}

// An authentication grant may target an existing account independently of
// visibility. It never calls invite/member creation APIs.
func (s *Server) listProjectAuthenticationGrants(w http.ResponseWriter, r *http.Request) {
	s.listAppAccessGrants(w, r, (*Server).productionRuntime)
}

func (s *Server) revokeProjectAuthenticationGrant(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	allowed, err := s.authorizeCaller(r.Context(), id, dataplane.ResourceAttributes{Group: projectsGVR.Group, Version: projectsGVR.Version, Resource: projectsGVR.Resource, Name: p.Name, Verb: "update"})
	if err != nil {
		writeError(w, err)
		return
	}
	if !allowed {
		writeStatus(w, http.StatusForbidden, "Forbidden", "project update permission required")
		return
	}
	s.revokeAppAccessGrant(w, r, (*Server).productionRuntime)
}

func (s *Server) createProjectAuthenticationGrant(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	allowed, err := s.authorizeCaller(r.Context(), id, dataplane.ResourceAttributes{Group: projectsGVR.Group, Version: projectsGVR.Version, Resource: projectsGVR.Resource, Name: p.Name, Verb: "update"})
	if err != nil {
		writeError(w, err)
		return
	}
	if !allowed {
		writeStatus(w, http.StatusForbidden, "Forbidden", "project update permission required")
		return
	}
	if !p.Spec.Authentication.Enabled {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "enable Railgrid Auth before approving users")
		return
	}
	runtime, err := s.productionRuntime(r.Context(), c, p)
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		User  string `json:"user"`
		AppID string `json:"appId"`
	}
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "existing Railgrid user ID is required")
		return
	}
	if req.AppID != string(p.UID)+"/"+projectProductionEnvironmentName {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "appId must identify this Project production environment")
		return
	}
	member, err := s.existingAuthenticationUser(r.Context(), id, runtime.target, req.User, req.AppID)
	if err != nil {
		writeHubError(w, err)
		return
	}
	if member.User == "" || member.RBACIdentity == "" {
		writeStatus(w, http.StatusBadGateway, "BadGateway", "hub returned incomplete existing user identity")
		return
	}
	grants, err := s.brokerCreateAppAccessGrant(r.Context(), id, p, runtime.target, member.User)
	if err != nil {
		writeAppAccessBrokerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ListResponse[projectPublishingGrantView]{Items: grants})
}

func (s *Server) existingAuthenticationUser(ctx context.Context, id identity, target projectPublishingTargetView, user, appID string) (publishingMember, error) {
	if s.authenticationUserResolver != nil {
		return s.authenticationUserResolver(ctx, id, target, user)
	}
	payload, err := json.Marshal(map[string]string{"user": user, "group": strings.Split(target.APIVersion, "/")[0], "resource": target.Resource, "name": target.Name, "appId": appID})
	if err != nil {
		return publishingMember{}, err
	}
	endpoint := strings.TrimRight(s.hubBase, "/") + hubapi.AppAuthenticationUserPath(id.orgUUID, id.workspaceUUID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return publishingMember{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.setHubCallerHeaders(req.Header, id)
	s.setHubActionProofHeader(req.Header, id)
	response, err := s.hubHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return publishingMember{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return publishingMember{}, fmt.Errorf("existing account lookup refused (HTTP %d)", response.StatusCode)
	}
	var member publishingMember
	err = json.NewDecoder(response.Body).Decode(&member)
	return member, err
}

// Enablement requires a compatible identity service. A stale hub must not
// silently accept an authentication setting it cannot serve.
func (s *Server) checkAuthenticationService(ctx context.Context) error {
	if s.authenticationServiceProbe != nil {
		return s.authenticationServiceProbe(ctx)
	}
	if strings.TrimSpace(s.hubBase) == "" {
		return newValidationError("Railgrid Auth identity service is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.hubBase, "/")+"/auth/apps/identity/jwks", nil)
	if err != nil {
		return err
	}
	response, err := s.hubHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("railgrid auth identity service is unavailable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return newValidationError("Railgrid Auth identity service is unavailable; update the hub before enabling authentication")
	}
	var keys struct {
		Keys []struct {
			KeyType string `json:"kty"`
			Curve   string `json:"crv"`
			ID      string `json:"kid"`
			X       string `json:"x"`
		} `json:"keys"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(nil, response.Body, 64<<10)).Decode(&keys); err != nil {
		return newValidationError("Railgrid Auth identity service returned an incompatible key contract")
	}
	for _, key := range keys.Keys {
		if key.KeyType == "OKP" && key.Curve == "Ed25519" && key.ID != "" && key.X != "" {
			return nil
		}
	}
	return newValidationError("Railgrid Auth identity service is not ready")
}
