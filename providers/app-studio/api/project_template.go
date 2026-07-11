/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Template-backed development environments
// (docs/app-studio-template-sandboxes.md §4). A Project that names an
// infrastructure Template in spec.template gets its development binding
// generated from that Template's instanceCRD with kedgeMode: development —
// the dev overlay the infrastructure provider synthesizes runs the declared
// components on platform dev images. App Studio reads the Template CR live
// from the tenant workspace catalog (the same CachedResource surface the
// portal and MCP use), so the component/workspacePath map file sync routes by
// never drifts from what the provider actually serves.

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

var templatesGVR = schema.GroupVersionResource{
	Group:    "infrastructure.kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "templates",
}

var templateResource = tenant.Resource{GVR: templatesGVR, Kind: "Template", Plural: "Templates"}

// projectTemplateInfo is the slice of an infrastructure Template App Studio
// needs: the instance kind the development binding creates, and the declared
// development components keyed by name with their workspace subdirectories.
type projectTemplateInfo struct {
	Name string

	// Instance CRD coordinates from Template.spec.instanceCRD.
	APIVersion string
	Kind       string
	Resource   string

	// Components maps a development component name to its workspacePath
	// ("." claims the whole workspace). Non-empty iff the template declares
	// spec.development.
	Components map[string]string
}

// fetchProjectTemplate reads the named Template from the tenant workspace
// catalog and extracts the development contract. Returns a NotFound API error
// when the catalog has no such template (surfaced as 404).
func fetchProjectTemplate(ctx context.Context, c *asclient.Client, name string) (projectTemplateInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return projectTemplateInfo{}, fmt.Errorf("template name is required")
	}
	obj, err := c.Resource(templateResource, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return projectTemplateInfo{}, err
	}
	return projectTemplateInfoFromUnstructured(obj)
}

func projectTemplateInfoFromUnstructured(obj *unstructured.Unstructured) (projectTemplateInfo, error) {
	if obj == nil {
		return projectTemplateInfo{}, fmt.Errorf("template is nil")
	}
	info := projectTemplateInfo{Name: obj.GetName()}

	group, _, _ := unstructured.NestedString(obj.Object, "spec", "instanceCRD", "group")
	version, _, _ := unstructured.NestedString(obj.Object, "spec", "instanceCRD", "version")
	info.Kind, _, _ = unstructured.NestedString(obj.Object, "spec", "instanceCRD", "kind")
	info.Resource, _, _ = unstructured.NestedString(obj.Object, "spec", "instanceCRD", "resource")
	if group == "" || version == "" || info.Kind == "" || info.Resource == "" {
		return projectTemplateInfo{}, fmt.Errorf("template %q has an incomplete spec.instanceCRD", info.Name)
	}
	info.APIVersion = group + "/" + version

	components, found, err := unstructured.NestedMap(obj.Object, "spec", "development", "components")
	if err != nil {
		return projectTemplateInfo{}, fmt.Errorf("template %q spec.development is malformed: %w", info.Name, err)
	}
	if found && len(components) > 0 {
		info.Components = make(map[string]string, len(components))
		for name, raw := range components {
			comp, ok := raw.(map[string]any)
			if !ok {
				return projectTemplateInfo{}, fmt.Errorf("template %q development component %q is malformed", info.Name, name)
			}
			wp, _ := comp["workspacePath"].(string)
			wp = strings.TrimSpace(wp)
			if wp == "" {
				return projectTemplateInfo{}, fmt.Errorf("template %q development component %q has no workspacePath", info.Name, name)
			}
			info.Components[name] = wp
		}
	}
	return info, nil
}

// projectTemplateInstanceNameMaxBase bounds the project-name part of the
// instance name. Template graphs derive child names from the instance name
// with suffixes like "-dev-<component>-control" (a Service, DNS label ≤63),
// so the base must stay well under that — long project names switch to a
// truncated-plus-hash form, still deterministic.
const projectTemplateInstanceNameMaxBase = 30

// projectTemplateInstanceName is the deterministic instance name for a
// Project's template-backed development environment. Deterministic so
// re-selection and status lookups converge without storing the name.
func projectTemplateInstanceName(p *aiv1alpha1.Project) string {
	if p == nil || strings.TrimSpace(p.Name) == "" {
		return ""
	}
	base := strings.TrimSpace(p.Name)
	if len(base) > projectTemplateInstanceNameMaxBase {
		sum := sha256.Sum256([]byte(base))
		base = strings.TrimRight(base[:projectTemplateInstanceNameMaxBase-9], "-") + "-" + hex.EncodeToString(sum[:4])
	}
	return base + "-dev"
}

// projectTemplateDevBinding builds the development binding for a
// template-backed Project: an instance of the Template's kind provisioned in
// development mode. The infrastructure provider's dev overlay does the rest.
func projectTemplateDevBinding(p *aiv1alpha1.Project, info projectTemplateInfo) (aiv1alpha1.ProjectProviderBindingSpec, error) {
	name := projectTemplateInstanceName(p)
	if name == "" {
		return aiv1alpha1.ProjectProviderBindingSpec{}, fmt.Errorf("project has no name")
	}
	values, err := json.Marshal(map[string]any{
		"name":      name,
		"kedgeMode": "development",
	})
	if err != nil {
		return aiv1alpha1.ProjectProviderBindingSpec{}, err
	}
	return aiv1alpha1.ProjectProviderBindingSpec{
		Name:     projectDevelopmentBindingName,
		Provider: projectDevelopmentProviderAppStudio,
		Kind:     aiv1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{
			Name:       name,
			APIVersion: info.APIVersion,
			Kind:       info.Kind,
			Resource:   info.Resource,
		},
		Values: runtime.RawExtension{Raw: values},
	}, nil
}

// selectProjectTemplate switches the Project's development environment onto
// the named Template (docs/app-studio-template-sandboxes.md §4.3, minus the
// git re-hydrate that lands with the Code provider checkout):
//
//  1. Read the Template from the tenant catalog; require a development block.
//  2. Delete the previous development binding's instance (kro GC tears the
//     old graph down — workspace files live in App Studio's store and are
//     re-synced after the switch).
//  3. Rewrite spec.template + the development binding; update the Project.
//  4. Reconcile the new binding (creates the instance in development mode).
func (s *Server) selectProjectTemplate(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, templateName string) (*aiv1alpha1.Project, projectTemplateInfo, error) {
	info, err := fetchProjectTemplate(ctx, c, templateName)
	if err != nil {
		return nil, projectTemplateInfo{}, err
	}
	if len(info.Components) == 0 {
		return nil, projectTemplateInfo{}, fmt.Errorf("template %q declares no development components; it cannot back a development environment", info.Name)
	}
	if p.Spec.Template != nil && strings.TrimSpace(p.Spec.Template.Name) == info.Name {
		// Already selected — reconcile and return (idempotent re-select).
		next, err := s.reconcileProjectLiveBindings(ctx, c, p, id)
		if err != nil {
			return nil, projectTemplateInfo{}, err
		}
		return next, info, nil
	}

	binding, err := projectTemplateDevBinding(p, info)
	if err != nil {
		return nil, projectTemplateInfo{}, err
	}

	// Tear down the previous development instance before rewriting the spec:
	// after the update its binding is gone from the Project, and nothing
	// would ever delete it.
	if err := s.deleteProjectDevelopmentBindingResources(ctx, c, p, id); err != nil {
		return nil, projectTemplateInfo{}, err
	}

	next := p.DeepCopy()
	next.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: info.Name}
	replaced := false
	for i := range next.Spec.Environments {
		env := &next.Spec.Environments[i]
		if strings.TrimSpace(env.Name) != projectDevelopmentEnvironmentName {
			continue
		}
		kept := env.Bindings[:0]
		for _, b := range env.Bindings {
			if strings.TrimSpace(b.Name) == projectDevelopmentBindingName {
				continue
			}
			kept = append(kept, b)
		}
		env.Bindings = append(kept, binding)
		replaced = true
	}
	if !replaced {
		next.Spec.Environments = append(next.Spec.Environments, aiv1alpha1.ProjectEnvironmentSpec{
			Name:      projectDevelopmentEnvironmentName,
			Mode:      aiv1alpha1.ProjectEnvironmentModeLive,
			Promotion: aiv1alpha1.ProjectPromotionManual,
			Bindings:  []aiv1alpha1.ProjectProviderBindingSpec{binding},
		})
	}

	updated, err := c.Projects().Update(ctx, next, metav1.UpdateOptions{})
	if err != nil {
		return nil, projectTemplateInfo{}, err
	}
	reconciled, err := s.reconcileProjectLiveBindings(ctx, c, updated, id)
	if err != nil {
		return nil, projectTemplateInfo{}, err
	}
	return reconciled, info, nil
}

// deleteProjectDevelopmentBindingResources deletes the instances behind the
// development environment's current bindings (the old sandbox runner or the
// previous template's instance). NotFound is success.
func (s *Server) deleteProjectDevelopmentBindingResources(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) error {
	for _, env := range p.Spec.Environments {
		if strings.TrimSpace(env.Name) != projectDevelopmentEnvironmentName {
			continue
		}
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			gvr, err := projectProviderResourceGVR(binding.ResourceRef)
			if err != nil {
				return err
			}
			values, err := projectProviderBindingValues(binding)
			if err != nil {
				return err
			}
			name := projectProviderBindingResourceName(p, binding, values, id)
			if name == "" {
				continue
			}
			err = c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Delete(ctx, name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// templateDevelopmentPreview resolves the preview for a template-backed
// development environment: the instance's own public URL (status.url). The
// dev overlay keeps the production HTTPRoute wiring, so the dev instance is
// served exactly where a production one would be — no signed sandbox-gateway
// URL involved. Access control is the template's own exposure model.
func (s *Server) templateDevelopmentPreview(ctx context.Context, c *asclient.Client, target projectDevelopmentSyncTargetInfo) (projectSandboxPreviewURLResponse, error) {
	res, err := target.instanceResource()
	if err != nil {
		return projectSandboxPreviewURLResponse{}, err
	}
	obj, err := c.Resource(res, "").Get(ctx, target.ResourceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "development_instance_not_found",
			Message: "Preview is getting ready. The development environment has not been created yet.",
		}, nil
	}
	if err != nil {
		return projectSandboxPreviewURLResponse{}, err
	}
	url, _, _ := unstructured.NestedString(obj.Object, "status", "url")
	if strings.TrimSpace(url) == "" {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "development_url_not_ready",
			Message: "Preview is getting ready. The development environment does not have a URL yet.",
		}, nil
	}
	// status.url exists as soon as the HTTPRoute does, but the Gateway edge
	// (DNS + TLS certificate at Cloudflare) can take minutes to provision —
	// until then the URL is a broken iframe. Keep reporting "provisioning"
	// until the edge actually serves it (see preview_edge.go).
	url = strings.TrimSpace(url)
	if !s.previewEdgeReady(ctx, url) {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  previewReasonEdgeProvisioning,
			Message: previewEdgeProvisioningMessage,
		}, nil
	}
	return projectSandboxPreviewURLResponse{Ready: true, PreviewURL: url}, nil
}

// --- HTTP surface -----------------------------------------------------------

// projectDevelopmentTemplateView is one catalog entry the portal offers when
// selecting (or switching) a project's development template.
type projectDevelopmentTemplateView struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName,omitempty"`
	Description string            `json:"description,omitempty"`
	Category    string            `json:"category,omitempty"`
	Components  map[string]string `json:"components"`
}

// listDevelopmentTemplates is GET /api/projects/development-templates: the
// tenant catalog filtered to templates that can back a development
// environment (those declaring development components). The portal's
// template picker reads this instead of the raw infrastructure catalog.
func (s *Server) listDevelopmentTemplates(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	serveDevelopmentTemplates(w, r, c)
}

// serveDevelopmentTemplates is the client-independent part of the handler,
// split from the auth plumbing so tests can drive it over HTTP.
func serveDevelopmentTemplates(w http.ResponseWriter, r *http.Request, c *asclient.Client) {
	list, err := c.Resource(templateResource, "").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		// Same mapping as the other /api/projects handlers: workspace
		// initialization gets 503 + Retry-After, kcp API errors keep their
		// status, instead of a blanket 502.
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": developmentTemplateViews(list.Items)})
}

// developmentTemplateViews filters the raw Template list down to templates
// declaring development components and shapes them for the portal picker,
// sorted by name for stable ordering. Templates with a malformed spec are
// skipped, never surfaced as errors — a broken catalog entry must not hide
// the rest of the catalog.
func developmentTemplateViews(items []unstructured.Unstructured) []projectDevelopmentTemplateView {
	out := make([]projectDevelopmentTemplateView, 0, len(items))
	for i := range items {
		obj := &items[i]
		info, err := projectTemplateInfoFromUnstructured(obj)
		if err != nil || len(info.Components) == 0 {
			continue
		}
		view := projectDevelopmentTemplateView{
			Name:       info.Name,
			Components: info.Components,
		}
		view.DisplayName, _, _ = unstructured.NestedString(obj.Object, "spec", "displayName")
		view.Description, _, _ = unstructured.NestedString(obj.Object, "spec", "description")
		view.Category, _, _ = unstructured.NestedString(obj.Object, "spec", "category")
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type projectTemplateSelectRequest struct {
	Template string `json:"template"`
}

type projectTemplateSelectResponse struct {
	Template   string            `json:"template"`
	Components map[string]string `json:"components"`
	Project    json.RawMessage   `json:"project,omitempty"`
}

// putProjectTemplate is PUT /api/projects/{project}/template — the portal's
// catalog shortcut that skips the assistant interview. Selecting the same
// template is an idempotent reconcile; a different template is a switch.
func (s *Server) putProjectTemplate(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req projectTemplateSelectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	updated, info, err := s.selectProjectTemplate(r.Context(), c, id, p, req.Template)
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
			return
		}
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	// Push the workspace into the fresh instance's components; failures are
	// non-fatal (the instance may still be provisioning — the post-mutation
	// sync hook and manual sync retry).
	go s.syncDevelopmentAfterMutation(id, updated, "select_project_template")

	raw, _ := json.Marshal(updated)
	writeJSON(w, http.StatusOK, projectTemplateSelectResponse{
		Template:   info.Name,
		Components: info.Components,
		Project:    raw,
	})
}
