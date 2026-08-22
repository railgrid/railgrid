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
// generated from that Template's instanceCRD with farosMode: development —
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
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

var templatesGVR = schema.GroupVersionResource{
	Group:    "infrastructure.faros.sh",
	Version:  "v1alpha1",
	Resource: "templates",
}

var templateResource = tenant.Resource{GVR: templatesGVR, Kind: "Template", Plural: "Templates"}

const projectTemplateImmutableInputsAnnotation = "faros.sh/immutable-inputs"

const (
	projectTemplatePlatformOwnedLabel = "faros.sh/platform-owned"
	projectTemplatePlatformOwnedValue = "true"
)

// projectTemplateInfo is the slice of an infrastructure Template App Studio
// needs: the instance kind the development binding creates, and the declared
// development components keyed by name with their workspace subdirectories.
type projectTemplateInfo struct {
	Name string

	// PlatformOwned is true only for the exact server-owned label value. A
	// missing or malformed label keeps the existing public-template behavior;
	// callers use this bit only at selection and user-facing catalog boundaries.
	PlatformOwned bool

	// Instance CRD coordinates from Template.spec.instanceCRD.
	APIVersion string
	Kind       string
	Resource   string

	// ProductionSchema is the Template's tenant-facing production input
	// contract. App Studio forwards it to the portal so production settings are
	// rendered from the selected Template rather than from a provider-specific
	// JSON blob.
	ProductionSchema map[string]any

	// PreviewAccessModes is non-empty when the Template exposes the standard
	// access input consumed by faros-access-proxy. App Studio uses this
	// capability instead of guessing from a URL or a particular Template name.
	PreviewAccessModes []string

	// ImmutableProductionInputs are tenant-facing schema paths that cannot be
	// changed after the first production instance is created.
	ImmutableProductionInputs []string

	// Components maps a development component name to its contract. Non-empty
	// iff the template declares spec.development.
	Components map[string]projectTemplateComponent

	// ScaffoldRepo / ScaffoldRef pin the starter-code repository whose tree
	// seeds a fresh project's workspace (spec.development.scaffold). Empty
	// when the template ships no scaffold — the project then starts empty and
	// the assistant generates from scratch.
	ScaffoldRepo string
	ScaffoldRef  string

	// BuildWorkflowPath is the repository-owned CI workflow declared by
	// spec.development.build. Empty means the Template declares no CI.
	BuildWorkflowPath string
}

// projectTemplateComponent is one development component's contract: where its
// source lives AND what will execute it in the development sandbox.
//
// The runtime half (Toolchain, StartCommand) is here because omitting it is
// what let an assistant write a Go backend into a component whose sandbox runs
// a Node image with "npm run dev || npm start": knowing only the directory, a
// Go layout is a reasonable guess. The template's agent.usage prose describes
// this too, but prose can be truncated, unread, or drift from the actual
// startCommand — these fields cannot.
type projectTemplateComponent struct {
	// WorkspacePath is the workspace subdirectory whose files belong to this
	// component ("." claims the whole workspace).
	WorkspacePath string `json:"workspacePath"`

	// Toolchain is the component's development runtime, derived from the
	// template's ${faros.devImage.<toolchain>} token (e.g. "node"). Empty when
	// the template declares no parseable devImage.
	Toolchain string `json:"toolchain,omitempty"`

	// StartCommand is the shell command the dev agent supervises in the
	// sandbox — the ground truth for what the component's source must be. May
	// be long (a template can inline a config shim); callers that surface it
	// to an agent should bound it.
	StartCommand string `json:"startCommand,omitempty"`

	// Port is the named container port the dev process serves on. Empty means
	// the component serves no traffic (e.g. a worker).
	Port string `json:"port,omitempty"`

	// ImageInput is the production schema input this component's built image
	// feeds on launch (TemplateDevelopmentComponent.imageInput). Empty means
	// the component produces no launchable image.
	ImageInput string `json:"imageInput,omitempty"`
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
	info := projectTemplateInfo{
		Name:          obj.GetName(),
		PlatformOwned: projectTemplateIsPlatformOwned(obj),
	}

	// Instance coordinates are the FLATTENED tenant-facing kind: every
	// template's instances are authored as instances.infrastructure.faros.sh
	// with the template name in spec.template. Template.spec.instanceCRD
	// describes only the runtime-cluster kind and is no consumer's business.
	info.APIVersion = "infrastructure.faros.sh/v1alpha1"
	info.Kind = "Instance"
	info.Resource = "instances"

	productionSchema, found, err := unstructured.NestedMap(obj.Object, "spec", "schema")
	if err != nil {
		return projectTemplateInfo{}, fmt.Errorf("template %q spec.schema is malformed: %w", info.Name, err)
	}
	if found {
		info.ProductionSchema = productionSchema
		info.PreviewAccessModes = bindings.TemplatePreviewAccessModes(productionSchema)
	}
	for _, path := range strings.Split(obj.GetAnnotations()[projectTemplateImmutableInputsAnnotation], ",") {
		if path = strings.TrimSpace(path); path != "" {
			info.ImmutableProductionInputs = append(info.ImmutableProductionInputs, path)
		}
	}
	sort.Strings(info.ImmutableProductionInputs)

	components, found, err := unstructured.NestedMap(obj.Object, "spec", "development", "components")
	if err != nil {
		return projectTemplateInfo{}, fmt.Errorf("template %q spec.development is malformed: %w", info.Name, err)
	}
	if found && len(components) > 0 {
		info.Components = make(map[string]projectTemplateComponent, len(components))
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
			devImage, _ := comp["devImage"].(string)
			startCommand, _ := comp["startCommand"].(string)
			port, _ := comp["port"].(string)
			imageInput, _ := comp["imageInput"].(string)
			info.Components[name] = projectTemplateComponent{
				WorkspacePath: wp,
				Toolchain:     projectTemplateToolchain(devImage),
				StartCommand:  strings.TrimSpace(startCommand),
				Port:          strings.TrimSpace(port),
				ImageInput:    strings.TrimSpace(imageInput),
			}
		}
	}

	// Scaffold: the starter-code repo whose tree seeds the workspace at
	// creation. Optional — a template without one starts projects empty.
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "development", "scaffold", "repository")
	ref, _, _ := unstructured.NestedString(obj.Object, "spec", "development", "scaffold", "ref")
	info.ScaffoldRepo = strings.TrimSpace(repo)
	info.ScaffoldRef = strings.TrimSpace(ref)
	workflowPath, _, _ := unstructured.NestedString(obj.Object, "spec", "development", "build", "workflowPath")
	info.BuildWorkflowPath = strings.TrimSpace(workflowPath)

	return info, nil
}

// projectTemplateIsPlatformOwned identifies templates reserved for App Studio
// and other platform internals. Kubernetes labels are strings, so only the
// exact contract value is authoritative; absent, malformed, or alternate
// values retain the historical tenant-visible behavior.
func projectTemplateIsPlatformOwned(obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}
	return obj.GetLabels()[projectTemplatePlatformOwnedLabel] == projectTemplatePlatformOwnedValue
}

// WorkspacePaths projects the components down to the name → workspacePath map
// the portal DTOs and path-routing callers use. Keeps their JSON shape stable
// now that the internal component carries the runtime contract too.
func (i projectTemplateInfo) WorkspacePaths() map[string]string {
	if len(i.Components) == 0 {
		return nil
	}
	out := make(map[string]string, len(i.Components))
	for name, comp := range i.Components {
		out[name] = comp.WorkspacePath
	}
	return out
}

// projectTemplateDevImageTokenPrefix mirrors the infrastructure provider's
// reserved token family for platform-managed development images. The
// Template CRD validates devImage against ^\$\{faros\.devImage\.[a-z][a-z0-9-]*\}$,
// so the toolchain is exactly the token's suffix.
const projectTemplateDevImageTokenPrefix = "${faros.devImage."

// projectTemplateToolchain extracts the toolchain name from a component's
// devImage token ("${faros.devImage.node}" → "node"). Anything that is not a
// well-formed token yields "" — App Studio never resolves the token to a real
// image (that is the infrastructure provider's job), it only needs the name to
// tell an agent which runtime its code has to target.
func projectTemplateToolchain(devImage string) string {
	devImage = strings.TrimSpace(devImage)
	if !strings.HasPrefix(devImage, projectTemplateDevImageTokenPrefix) || !strings.HasSuffix(devImage, "}") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(devImage, projectTemplateDevImageTokenPrefix), "}")
}

// projectBuildComponent is one launchable component of a template-backed
// project: the App Studio build derives one OCI image from Context (the
// component's workspace subdirectory) and, on launch, sets ImageInput to that
// image. Only components whose template declares an imageInput are buildable.
type projectBuildComponent struct {
	// Name is the template development component key (e.g. "frontend").
	Name string
	// Context is the build context: the component's workspacePath relative to
	// the repository root ("." for a whole-repo, single-component template).
	Context string
	// ImageInput is the production schema field the built image feeds on
	// launch (e.g. "frontendImage", or "image" for a single-component app).
	ImageInput string
}

// projectBuildComponents returns the launchable components of a template,
// sorted by name for deterministic build config and workflow output. A
// template with development components but no imageInputs (e.g. a worker-only
// dev template) yields none.
func projectBuildComponents(info projectTemplateInfo) []projectBuildComponent {
	out := make([]projectBuildComponent, 0, len(info.Components))
	for name, comp := range info.Components {
		if comp.ImageInput == "" {
			continue
		}
		out = append(out, projectBuildComponent{
			Name:       name,
			Context:    comp.WorkspacePath,
			ImageInput: comp.ImageInput,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	return projectTemplateScopedInstanceName(p, "dev")
}

// projectTemplateProdInstanceName is the deterministic instance name for a
// Project's template-backed production environment — the sibling of the dev
// instance, so a project runs a "<base>-dev" and a "<base>-prod" instance of
// the same template side by side.
func projectTemplateProdInstanceName(p *aiv1alpha1.Project) string {
	return projectTemplateScopedInstanceName(p, "prod")
}

// projectTemplateScopedInstanceName derives a deterministic, DNS-safe instance
// name "<base>-<scope>" from the project name, truncating with a content hash
// when the project name is long (child resources suffix this further).
func projectTemplateScopedInstanceName(p *aiv1alpha1.Project, scope string) string {
	if p == nil || strings.TrimSpace(p.Name) == "" {
		return ""
	}
	base := strings.TrimSpace(p.Name)
	if len(base) > projectTemplateInstanceNameMaxBase {
		sum := sha256.Sum256([]byte(base))
		base = strings.TrimRight(base[:projectTemplateInstanceNameMaxBase-9], "-") + "-" + hex.EncodeToString(sum[:4])
	}
	return base + "-" + scope
}

type projectTemplateBindingContext struct {
	ActionsExchangeURL string
	ActionsBaseURL     string
	ActionsCABundle    string
	TenantPath         string
	Org                string
	Workspace          string
	Project            string
	ProjectUID         string
	Environment        string
	Instance           string
}

func projectTemplateDevBindingWithContext(p *aiv1alpha1.Project, info projectTemplateInfo, context projectTemplateBindingContext) (aiv1alpha1.ProjectProviderBindingSpec, error) {
	name := projectTemplateInstanceName(p)
	if name == "" {
		return aiv1alpha1.ProjectProviderBindingSpec{}, fmt.Errorf("project has no name")
	}
	if context.Project == "" {
		context.Project = p.Name
	}
	if context.ProjectUID == "" && p.UID != "" {
		context.ProjectUID = string(p.UID)
	}
	if context.Environment == "" {
		context.Environment = projectDevelopmentEnvironmentName
	}
	if context.Instance == "" {
		context.Instance = name
	}
	valuesMap := map[string]any{
		"name":      name,
		"farosMode": "development",
	}
	if len(info.PreviewAccessModes) > 0 {
		valuesMap[bindings.PreviewAccessField] = effectiveProjectPreviewAccess(p.Spec.Sharing.Preview.Mode)
	}
	valuesMap = bindings.ApplyActionsOverlay(valuesMap, bindings.ActionsOverlay{
		ExchangeURL: context.ActionsExchangeURL,
		BaseURL:     context.ActionsBaseURL,
		CABundle:    context.ActionsCABundle,
		ActionsIdentity: bindings.ActionsIdentity{
			TenantPath:  context.TenantPath,
			Org:         context.Org,
			Workspace:   context.Workspace,
			Project:     context.Project,
			ProjectUID:  context.ProjectUID,
			Environment: context.Environment,
			Instance:    context.Instance,
		},
	})
	values, err := json.Marshal(valuesMap)
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

func validateProjectDevelopmentTemplate(info projectTemplateInfo) error {
	if strings.TrimSpace(info.Name) == "" {
		return fmt.Errorf("template name is required")
	}
	if len(info.Components) == 0 {
		return fmt.Errorf("template %q declares no development components; it cannot back a development environment", info.Name)
	}
	return nil
}

func validateProjectTemplateSelection(info projectTemplateInfo) error {
	if info.PlatformOwned {
		return fmt.Errorf("template %q is platform-owned and cannot be selected for a project", info.Name)
	}
	return nil
}

func applyProjectDevelopmentTemplateWithContext(p *aiv1alpha1.Project, info projectTemplateInfo, context projectTemplateBindingContext) error {
	if p == nil {
		return fmt.Errorf("project is required")
	}
	if err := validateProjectDevelopmentTemplate(info); err != nil {
		return err
	}
	binding, err := projectTemplateDevBindingWithContext(p, info, context)
	if err != nil {
		return err
	}

	p.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: info.Name}
	replaced := false
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		if strings.TrimSpace(env.Name) != projectDevelopmentEnvironmentName {
			continue
		}
		kept := env.Bindings[:0]
		for _, existing := range env.Bindings {
			if strings.TrimSpace(existing.Name) == projectDevelopmentBindingName && existing.Kind != aiv1alpha1.ProjectBindingKindProviderReference {
				continue
			}
			kept = append(kept, existing)
		}
		env.Bindings = append(kept, binding)
		replaced = true
	}
	if !replaced {
		p.Spec.Environments = append(p.Spec.Environments, aiv1alpha1.ProjectEnvironmentSpec{
			Name:      projectDevelopmentEnvironmentName,
			Mode:      aiv1alpha1.ProjectEnvironmentModeLive,
			Promotion: aiv1alpha1.ProjectPromotionManual,
			Bindings:  []aiv1alpha1.ProjectProviderBindingSpec{binding},
		})
	}
	return nil
}

func validateActionsExternalURL(raw string) (string, error) {
	return bindings.ValidateActionsExternalURL(raw)
}

func projectHasProviderActionGrant(p *aiv1alpha1.Project) bool {
	return bindings.HasActiveProviderActionGrant(p)
}

func (s *Server) projectTemplateBindingContext(p *aiv1alpha1.Project, id identity) (projectTemplateBindingContext, error) {
	context := projectTemplateBindingContext{
		TenantPath:  id.tenantPath,
		Org:         id.orgUUID,
		Workspace:   id.workspaceUUID,
		Project:     strings.TrimSpace(p.Name),
		ProjectUID:  string(p.UID),
		Environment: projectDevelopmentEnvironmentName,
		Instance:    projectTemplateInstanceName(p),
	}
	// The external URL is meaningful only for an active Provider Actions
	// grant. A globally configured origin must not opt actionless or revoked
	// runtimes into the exchange contract; App Studio owns these fields and
	// should leave them absent so the infrastructure schema defaults resolve to
	// empty strings.
	if !projectHasProviderActionGrant(p) {
		return context, nil
	}
	externalRaw := strings.TrimSpace(s.actionsExternalURL)
	if externalRaw == "" {
		return projectTemplateBindingContext{}, fmt.Errorf("FAROS_ACTIONS_EXTERNAL_URL is required for action-enabled development runtimes")
	}
	transport, err := bindings.ActionsTransportForOrigin(externalRaw)
	if err != nil {
		return projectTemplateBindingContext{}, err
	}
	context.ActionsExchangeURL = transport.ExchangeURL
	context.ActionsBaseURL = transport.BaseURL
	if s.actionsCABundleErr != nil {
		return projectTemplateBindingContext{}, fmt.Errorf("configured action CA bundle: %w", s.actionsCABundleErr)
	}
	context.ActionsCABundle = s.actionsCABundle
	return context, nil
}

func (s *Server) applyProjectDevelopmentTemplateWithIdentity(p *aiv1alpha1.Project, info projectTemplateInfo, id identity) error {
	context, err := s.projectTemplateBindingContext(p, id)
	if err != nil {
		return err
	}
	return applyProjectDevelopmentTemplateWithContext(p, info, context)
}

// ActionsRuntimeConfig exposes the operator-owned action transport settings to
// the background Project controller. The controller still derives tenant and
// project identity from its authoritative multicluster context.
func (s *Server) ActionsRuntimeConfig() bindings.ActionsRuntimeConfig {
	if s == nil {
		return bindings.ActionsRuntimeConfig{}
	}
	return bindings.ActionsRuntimeConfig{
		ExternalURL: s.actionsExternalURL,
		CABundle:    s.actionsCABundle,
		CABundleErr: s.actionsCABundleErr,
	}
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
	if err := validateProjectTemplateSelection(info); err != nil {
		return nil, projectTemplateInfo{}, err
	}
	if err := validateProjectDevelopmentTemplate(info); err != nil {
		return nil, projectTemplateInfo{}, err
	}
	if p.Spec.Template != nil && strings.TrimSpace(p.Spec.Template.Name) == info.Name {
		// Already selected — the reconciler owns convergence; return fresh
		// observed state (idempotent re-select). Still attempt the scaffold
		// seed: it is a no-op once the workspace has content, but recovers a
		// project whose template was bound before the seed step existed (or
		// whose earlier seed failed).
		if _, err := s.seedProjectScaffold(ctx, id, p, info); err != nil {
			klog.V(1).Infof("scaffold seed on re-select for %s: %v", p.Name, err)
		}
		return projectWithLiveBindingStatus(ctx, c, p, id), info, nil
	}

	// Tear down the previous development instance before rewriting the spec:
	// after the update its binding is gone from the Project, and nothing
	// would ever delete it.
	if err := s.deleteProjectDevelopmentBindingResources(ctx, c, p, id); err != nil {
		return nil, projectTemplateInfo{}, err
	}

	next := p.DeepCopy()
	if err := s.applyProjectDevelopmentTemplateWithIdentity(next, info, id); err != nil {
		return nil, projectTemplateInfo{}, err
	}

	updated, err := c.Projects().Update(ctx, next, metav1.UpdateOptions{})
	if err != nil {
		return nil, projectTemplateInfo{}, err
	}
	// Attach the template's starter code the moment the template is bound —
	// this is the "scaffolding attached to template" step. It is the primary
	// seed point in practice: the assistant usually picks the template mid-
	// turn (select_project_template) rather than at create, so create-time
	// seeding alone would leave most projects empty. Best-effort and a no-op
	// on an already-populated workspace, so switching templates never
	// clobbers existing code.
	if _, err := s.seedProjectScaffold(ctx, id, updated, info); err != nil {
		klog.V(1).Infof("scaffold seed on template select for %s: %v", updated.Name, err)
	}
	// The Project reconciler converges the new binding into an instance; the
	// response reports it Pending until the mirror catches up.
	return updated, info, nil
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
	observedAccess := ""
	if len(target.PreviewAccessModes) > 0 {
		observedAccess, _, _ = unstructured.NestedString(obj.Object, "spec", "values", bindings.PreviewAccessField)
		if strings.TrimSpace(observedAccess) == "" {
			// Both URL-backed seed Templates default an omitted access input to
			// public. Report that effective behavior during legacy convergence.
			observedAccess = bindings.PreviewAccessPublic
		}
	}
	url, _, _ := unstructured.NestedString(obj.Object, "status", "url")
	if strings.TrimSpace(url) == "" {
		return projectSandboxPreviewURLResponse{
			Ready:          false,
			Reason:         "development_url_not_ready",
			Message:        "Preview is getting ready. The development environment does not have a URL yet.",
			ObservedAccess: observedAccess,
		}, nil
	}
	// status.url exists as soon as the HTTPRoute does, but the Gateway edge
	// (DNS + TLS certificate at Cloudflare) can take minutes to provision —
	// until then the URL is a broken iframe. Keep reporting "provisioning"
	// until the edge actually serves it (see preview_edge.go).
	url = strings.TrimSpace(url)
	if !s.previewEdgeReady(ctx, url) {
		return projectSandboxPreviewURLResponse{
			Ready:          false,
			PreviewURL:     url,
			Reason:         previewReasonEdgeProvisioning,
			Message:        previewEdgeProvisioningMessage,
			ObservedAccess: observedAccess,
		}, nil
	}
	return projectSandboxPreviewURLResponse{Ready: true, PreviewURL: url, ObservedAccess: observedAccess}, nil
}

// --- HTTP surface -----------------------------------------------------------

// projectDevelopmentTemplateView is one catalog entry the portal offers when
// selecting (or switching) a project's development template.
type projectDevelopmentTemplateView struct {
	Name               string            `json:"name"`
	DisplayName        string            `json:"displayName,omitempty"`
	Description        string            `json:"description,omitempty"`
	Category           string            `json:"category,omitempty"`
	Components         map[string]string `json:"components"`
	PreviewAccessModes []string          `json:"previewAccessModes,omitempty"`
	// HasScaffold reports whether picking this template seeds the workspace
	// with starter code (spec.development.scaffold) — the wizard shows it so
	// the user knows the project opens on a runnable placeholder.
	HasScaffold bool `json:"hasScaffold,omitempty"`
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
	templates, err := listDevelopmentTemplateViews(r.Context(), c)
	if err != nil {
		// Same mapping as the other /api/projects handlers: workspace
		// initialization gets 503 + Retry-After, kcp API errors keep their
		// status, instead of a blanket 502.
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

func listDevelopmentTemplateViews(ctx context.Context, c *asclient.Client) ([]projectDevelopmentTemplateView, error) {
	list, err := c.Resource(templateResource, "").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return developmentTemplateViews(list.Items), nil
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
		if err != nil || info.PlatformOwned || len(info.Components) == 0 {
			continue
		}
		view := projectDevelopmentTemplateView{
			Name:               info.Name,
			Components:         info.WorkspacePaths(),
			HasScaffold:        info.ScaffoldRepo != "",
			PreviewAccessModes: append([]string(nil), info.PreviewAccessModes...),
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
	release, ok := s.reserveProjectExternalOperation(w, r.Context(), id, p, "switching the development template")
	if !ok {
		return
	}
	defer release()
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
	// Push the workspace into the fresh instance through the per-project
	// ordered queue. Failures are non-fatal because the instance may still be
	// provisioning and operational verification will surface the blocker.
	s.scheduleDevelopmentSyncAfterMutation(id, updated, projectToolSelectTemplate)

	raw, _ := json.Marshal(updated)
	writeJSON(w, http.StatusOK, projectTemplateSelectResponse{
		Template:   info.Name,
		Components: info.WorkspacePaths(),
		Project:    raw,
	})
}
