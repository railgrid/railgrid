/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"log"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/tenant"
)

// Session CRs and the Studio singleton — the API-side halves of the
// control-plane projections the reconcilers converge (controller/session,
// controller/studio). Both writes are best-effort as the provider: a workspace
// without them loses `kubectl` visibility or web search, not the ability to
// build.

var (
	sessionResource = tenant.Resource{GVR: aiv1alpha1.SchemeGroupVersion.WithResource("sessions"), Kind: "Session", Plural: "Sessions"}
	studioResource  = tenant.Resource{GVR: aiv1alpha1.SchemeGroupVersion.WithResource("studios"), Kind: "Studio", Plural: "Studios"}
)

const (
	managedBrowserTemplateName    = "browser"
	managedBrowserTemplateVersion = "0.2.0"
	managedBrowserImage           = "mcr.microsoft.com/playwright/mcp@sha256:18c0a9c934004fe9580cc79f1e8e6e6cde7c667348b215335e8a23fd3e509804"
)

var managedBrowserArgs = []string{
	"--port", "8931",
	"--host", "0.0.0.0",
	"--allowed-hosts", "*",
	"--ignore-https-errors",
	"--isolated",
}

// Keep these CEL expressions in lockstep with the browser Template's RGD.
// The second is projected into each Instance only after kro observes the live
// Deployment with its complete isolated-context contract and current replica.
const managedBrowserDeploymentReadyExpression = `${deployment.status.observedGeneration == deployment.metadata.generation && deployment.spec.replicas == 1 && deployment.spec.strategy.type == "Recreate" && deployment.status.updatedReplicas == 1 && deployment.status.readyReplicas == 1 && deployment.status.availableReplicas == 1}`

const managedBrowserIsolationStatusExpression = `${deployment.metadata.generation == deployment.status.observedGeneration && deployment.spec.replicas == 1 && deployment.spec.strategy.type == "Recreate" && deployment.status.updatedReplicas == 1 && deployment.status.readyReplicas == 1 && deployment.status.availableReplicas == 1 && deployment.spec.template.spec.containers.size() == 1 && !has(deployment.spec.template.spec.initContainers) && deployment.spec.template.spec.containers[0].image == "mcr.microsoft.com/playwright/mcp@sha256:18c0a9c934004fe9580cc79f1e8e6e6cde7c667348b215335e8a23fd3e509804" && deployment.spec.template.spec.containers[0].args == ["--port", "8931", "--host", "0.0.0.0", "--allowed-hosts", "*", "--ignore-https-errors", "--isolated"] && deployment.spec.template.spec.containers[0].args.exists(arg, arg == "--isolated") && !deployment.spec.template.spec.containers[0].args.exists(arg, arg == "--shared-browser-context") && !has(deployment.spec.template.spec.containers[0].command) && !has(deployment.spec.template.spec.containers[0].env) && !has(deployment.spec.template.spec.containers[0].envFrom) && !has(deployment.spec.template.spec.containers[0].lifecycle) && deployment.spec.template.spec.containers[0].readinessProbe.tcpSocket.port == 8931 && !has(deployment.spec.template.spec.containers[0].readinessProbe.exec) && !has(deployment.spec.template.spec.containers[0].readinessProbe.httpGet) && !has(deployment.spec.template.spec.containers[0].readinessProbe.grpc) && !has(deployment.spec.template.spec.containers[0].livenessProbe) && !has(deployment.spec.template.spec.containers[0].startupProbe) && deployment.spec.template.spec.containers[0].volumeMounts.size() == 1 && deployment.spec.template.spec.containers[0].volumeMounts[0].name == "dshm" && deployment.spec.template.spec.containers[0].volumeMounts[0].mountPath == "/dev/shm" && deployment.spec.template.spec.volumes.size() == 1 && deployment.spec.template.spec.volumes[0].name == "dshm" && deployment.spec.template.spec.volumes[0].emptyDir.medium == "Memory"}`

// ensureSessionCR projects a freshly-created thread into a Session CR. The
// Session reconciler mirrors status and purges the store on CR delete; the
// ownerReference makes Project deletion take the conversations with it.
func (s *Server) ensureSessionCR(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, threadID, actorID string) {
	if c == nil || p == nil {
		return
	}
	sess := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Session",
		"metadata": map[string]any{
			"name": threadID,
			"annotations": map[string]any{
				bindings.OrgUUIDAnnotation:       id.orgUUID,
				bindings.WorkspaceUUIDAnnotation: id.workspaceUUID,
				"ai.railgrid.ai/project-uid":     string(p.UID),
			},
		},
		"spec": map[string]any{
			"projectRef": p.Name,
			"threadID":   threadID,
			"actorID":    actorID,
		},
	}}
	if owner := bindings.OwnerRef(p); owner != nil {
		sess.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}
	s.noteWorkspaceCluster(id)
	if _, err := c.Resource(sessionResource, "").Create(ctx, sess, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("session CR for thread %s: %v", threadID, err)
	}
}

// deleteSessionCR removes the thread's projection after an interactive
// deletion. Best-effort: the Session reconciler also deletes projections
// whose store row is gone.
func (s *Server) deleteSessionCR(ctx context.Context, c *asclient.Client, threadID string) {
	if c == nil {
		return
	}
	if err := c.Resource(sessionResource, "").Delete(ctx, threadID, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.Printf("deleting session CR for thread %s: %v", threadID, err)
	}
}

// The Studio is written when a workspace first creates a project, so the
// reconciler has the search backend warm before the assistant needs it.
// Once per process per workspace — but only once every shared service has
// its instance reference resolved. A fresh workspace races its infrastructure
// APIBinding: the first project can be created before the searxng/browser
// Templates are visible, and a Studio written then carries no resourceRef for
// the service. The reconciler parks such a service in Pending ("waiting for
// the … template to be resolved") and never re-resolves it, so the API keeps
// retrying on later project creations until every reference is in place. A
// Studio someone has since edited is otherwise left exactly as it is.
var studioEnsured sync.Map // clusterID → struct{}

// ensureStudio writes the workspace's Studio if it is missing, and fills in any
// shared-service reference it is still missing.
func (s *Server) ensureStudio(ctx context.Context, c *asclient.Client, id identity) {
	if c == nil || id.clusterID == "" {
		return
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		return
	}
	if existing, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{}); err == nil {
		// A Studio created before a service existed (e.g. browser, added after
		// search), or while its Template was not yet resolvable, is missing
		// that service's reference. Retrofit it so existing workspaces gain
		// the backend without recreating the Studio.
		if s.retrofitStudioServices(ctx, c, existing) {
			studioEnsured.Store(id.clusterID, struct{}{})
		}
		return
	} else if !apierrors.IsNotFound(err) {
		// The APIBinding may not have caught up with the schemas yet.
		return
	}

	st := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"spec": map[string]any{
			"search":  map[string]any{"size": "small"},
			"browser": map[string]any{"size": "small"},
		},
	}}
	resolved := true
	for _, svc := range studioSharedServices {
		ref := svc.resourceRef(s, ctx, c)
		if ref == nil {
			resolved = false
			continue
		}
		setStudioResourceRef(st, svc.name, ref)
	}
	if _, err := c.Resource(studioResource, "").Create(ctx, st, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("creating the studio for workspace %s: %v", id.clusterID, err)
		return
	}
	if resolved {
		studioEnsured.Store(id.clusterID, struct{}{})
	} else {
		log.Printf("studio for workspace %s created with an unresolved shared service; it is retrofitted on the next project creation", id.clusterID)
	}
	log.Printf("studio created for workspace %s (search %s, browser %s)", id.clusterID, studioSearchInstanceName, studioBrowserInstanceName)
}

// studioSharedService is one backend the Studio owns and every project shares.
// Its resourceRef is resolved by the API from the infrastructure Template, so
// the reconciler never reads Templates. Keep in step with controller/studio.
type studioSharedService struct {
	name        string // spec block: "search" / "browser"
	resourceRef func(s *Server, ctx context.Context, c *asclient.Client) *aiv1alpha1.ProjectProviderResourceReference
}

var studioSharedServices = []studioSharedService{
	{name: "search", resourceRef: (*Server).searchResourceRef},
	{name: "browser", resourceRef: (*Server).browserResourceRef},
}

// retrofitStudioServices fills in the spec block of every shared service the
// Studio is missing a resourceRef for, whether the service was introduced after
// the Studio was first created or its Template was simply not resolvable at the
// time. It patches only what is missing, so a workspace whose Studio predates a
// backend gains it without a recreate, and a service the workspace disabled is
// left alone. It reports whether every service now has its reference, so the
// caller knows whether to stop retrying. It is a no-op until the studios schema
// carries the field — kcp prunes unknown fields, so the write simply does not
// persist before then.
func (s *Server) retrofitStudioServices(ctx context.Context, c *asclient.Client, st *unstructured.Unstructured) (complete bool) {
	complete = true
	changed := false
	for _, svc := range studioSharedServices {
		if disabled, _, _ := unstructured.NestedBool(st.Object, "spec", svc.name, "disabled"); disabled {
			continue
		}
		if resource, _, _ := unstructured.NestedString(st.Object, "spec", svc.name, "resourceRef", "resource"); strings.TrimSpace(resource) != "" {
			continue // already present
		}
		ref := svc.resourceRef(s, ctx, c)
		if ref == nil {
			complete = false
			continue
		}
		if size, _, _ := unstructured.NestedString(st.Object, "spec", svc.name, "size"); strings.TrimSpace(size) == "" {
			_ = unstructured.SetNestedField(st.Object, "small", "spec", svc.name, "size")
		}
		setStudioResourceRef(st, svc.name, ref)
		changed = true
	}
	if !changed {
		return complete
	}
	if _, err := c.Resource(studioResource, "").Update(ctx, st, metav1.UpdateOptions{}); err != nil {
		log.Printf("retrofitting shared services onto studio for workspace %s: %v", st.GetName(), err)
		return false
	}
	return complete
}

// setStudioResourceRef writes a resolved instance reference under
// spec.<service>.resourceRef on the Studio being created.
func setStudioResourceRef(st *unstructured.Unstructured, service string, ref *aiv1alpha1.ProjectProviderResourceReference) {
	_ = unstructured.SetNestedMap(st.Object, map[string]any{
		"name":       ref.Name,
		"apiVersion": ref.APIVersion,
		"kind":       ref.Kind,
		"resource":   ref.Resource,
	}, "spec", service, "resourceRef")
}

// studioSearchInstanceName is the workspace's shared search backend — fixed,
// because there is exactly one and every project addresses it. Must match
// controller/studio.SearchInstanceName.
const studioSearchInstanceName = "app-studio-search"

// studioBrowserInstanceName is the workspace's shared headless browser — fixed,
// like the search backend. Must match controller/studio.BrowserInstanceName.
const studioBrowserInstanceName = "app-studio-browser"

// searchResourceRef resolves the searxng Template's instanceCRD as the
// caller, so the Studio reconciler never has to read Templates.
func (s *Server) searchResourceRef(ctx context.Context, c *asclient.Client) *aiv1alpha1.ProjectProviderResourceReference {
	info, err := fetchProjectTemplate(ctx, c, "searxng")
	if err != nil || strings.TrimSpace(info.Resource) == "" || strings.TrimSpace(info.Kind) == "" {
		log.Printf("web search unavailable: resolving the searxng template: %v", err)
		return nil
	}
	return &aiv1alpha1.ProjectProviderResourceReference{
		Name:       studioSearchInstanceName,
		APIVersion: info.APIVersion,
		Kind:       info.Kind,
		Resource:   info.Resource,
	}
}

// browserResourceRef resolves the browser Template's instanceCRD as the
// caller, so the Studio reconciler never has to read Templates.
func (s *Server) browserResourceRef(ctx context.Context, c *asclient.Client) *aiv1alpha1.ProjectProviderResourceReference {
	info, err := fetchProjectTemplate(ctx, c, "browser")
	if err != nil || strings.TrimSpace(info.Resource) == "" || strings.TrimSpace(info.Kind) == "" {
		log.Printf("preview browser unavailable: resolving the browser template: %v", err)
		return nil
	}
	return &aiv1alpha1.ProjectProviderResourceReference{
		Name:       studioBrowserInstanceName,
		APIVersion: info.APIVersion,
		Kind:       info.Kind,
		Resource:   info.Resource,
	}
}

// searchBackend reports the workspace's shared search backend for a turn.
// Empty when there is none — web_search then says so rather than failing
// obscurely.
func (s *Server) searchBackend(ctx context.Context, c *asclient.Client) (resource, name string) {
	return s.studioBackend(ctx, c, "search")
}

// browserBackendRef returns the Studio's Ready browser endpoint and reports
// whether the live Instance, its current Template, and the Template's
// runtime-projected Deployment proof all match the managed browser contract.
// A Ready custom or drifted endpoint is returned with false so callers can
// distinguish it from a workspace with no Ready backend, but it is never
// asserted safe for concurrent MCP contexts.
func (s *Server) browserBackendRef(ctx context.Context, c *asclient.Client) (dataPlaneRef, bool) {
	if c == nil {
		return dataPlaneRef{}, false
	}
	st, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return dataPlaneRef{}, false
	}
	resource, name := studioBackendFromObject(st, "browser")
	if strings.TrimSpace(resource) == "" || strings.TrimSpace(name) == "" {
		return dataPlaneRef{}, false
	}
	ref := dataPlaneRef{Resource: resource, Name: name}
	if resource != "instances" {
		return ref, false
	}
	instance, err := c.Resource(tenant.InfrastructureInstancesResource, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ref, false
	}
	ref.InstanceUID = string(instance.GetUID())
	templateName, found, err := unstructured.NestedString(instance.Object, "spec", "template")
	if err != nil || !found || strings.TrimSpace(templateName) == "" {
		return ref, false
	}
	template, err := c.Resource(templateResource, "").Get(ctx, templateName, metav1.GetOptions{})
	if err != nil {
		return ref, false
	}
	ref.TemplateVersion, _, _ = unstructured.NestedString(template.Object, "spec", "version")
	ref.TemplateGeneration = template.GetGeneration()
	ref.IsolatedMCPContexts = browserMCPContextsAreIsolated(instance, template, st, ref)
	return ref, ref.IsolatedMCPContexts
}

// browserMCPContextsAreIsolated requires fresh status at every layer. The
// template check pins the desired contract as observed through the tenant
// CachedResource; Instance status proves the controller observed this
// Instance generation; and mcpIsolationReady is projected from Deployment
// status. CachedResource replication is eventually consistent, so this is a
// proof of the objects this request observed, not a synchronous read of the
// provider workspace's latest Template edit.
func browserMCPContextsAreIsolated(instance, template, studio *unstructured.Unstructured, ref dataPlaneRef) bool {
	if instance == nil || template == nil || studio == nil || ref.Resource != "instances" || ref.Name != studioBrowserInstanceName || ref.InstanceUID == "" || ref.InstanceUID != string(instance.GetUID()) {
		return false
	}
	version, found, err := unstructured.NestedString(template.Object, "spec", "version")
	if err != nil || !found || ref.TemplateVersion != version || ref.TemplateGeneration != template.GetGeneration() {
		return false
	}
	browserRef, found, err := unstructured.NestedMap(studio.Object, "spec", "browser", "resourceRef")
	if err != nil || !found {
		return false
	}
	apiVersion, _ := browserRef["apiVersion"].(string)
	kind, _ := browserRef["kind"].(string)
	resource, _ := browserRef["resource"].(string)
	name, _ := browserRef["name"].(string)
	if apiVersion != infrastructureGroupVersion.String() || kind != "Instance" || resource != ref.Resource || name != ref.Name {
		return false
	}
	instanceTemplate, found, err := unstructured.NestedString(instance.Object, "spec", "template")
	if err != nil || !found || instanceTemplate != managedBrowserTemplateName {
		return false
	}
	statusTemplate, found, err := unstructured.NestedString(instance.Object, "status", "template")
	if err != nil || !found || statusTemplate != managedBrowserTemplateName {
		return false
	}
	templateVersion, found, err := unstructured.NestedString(instance.Object, "status", "templateVersion")
	if err != nil || !found || templateVersion != managedBrowserTemplateVersion {
		return false
	}
	generation := instance.GetGeneration()
	observedGeneration, found, err := unstructured.NestedInt64(instance.Object, "status", "observedGeneration")
	if err != nil || !found || generation <= 0 || observedGeneration != generation {
		return false
	}
	phase, found, err := unstructured.NestedString(instance.Object, "status", "phase")
	if err != nil || !found || phase != "Ready" || !browserReadyConditionCurrent(instance, generation) {
		return false
	}
	isolationReady, found, err := unstructured.NestedBool(instance.Object, "status", "mcpIsolationReady")
	if err != nil || !found || !isolationReady {
		return false
	}
	return managedBrowserTemplateHasIsolatedContract(template)
}

func browserReadyConditionCurrent(object *unstructured.Unstructured, generation int64) bool {
	conditions, found, err := unstructured.NestedSlice(object.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok || condition["type"] != "Ready" || condition["status"] != "True" {
			continue
		}
		observed, ok := condition["observedGeneration"].(int64)
		return ok && observed == generation
	}
	return false
}

func managedBrowserTemplateHasIsolatedContract(template *unstructured.Unstructured) bool {
	if template == nil || template.GetName() != managedBrowserTemplateName {
		return false
	}
	generation := template.GetGeneration()
	observedGeneration, found, err := unstructured.NestedInt64(template.Object, "status", "observedGeneration")
	if err != nil || !found || generation <= 0 || observedGeneration != generation || !browserReadyConditionCurrent(template, generation) {
		return false
	}
	backendName, found, err := unstructured.NestedString(template.Object, "status", "backend", "name")
	if err != nil || !found || backendName != "kro" {
		return false
	}
	backendReady, found, err := unstructured.NestedBool(template.Object, "status", "backend", "ready")
	if err != nil || !found || !backendReady {
		return false
	}
	version, found, err := unstructured.NestedString(template.Object, "spec", "version")
	if err != nil || !found || version != managedBrowserTemplateVersion {
		return false
	}
	backend, found, err := unstructured.NestedString(template.Object, "spec", "backend")
	if err != nil || !found || backend != "kro" {
		return false
	}
	resources, found, err := unstructured.NestedSlice(template.Object, "spec", "backendConfig", "resources")
	if err != nil || !found {
		return false
	}
	deploymentCount := 0
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if ok && resource["id"] == "deployment" {
			deploymentCount++
		}
	}
	if deploymentCount != 1 {
		return false
	}
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok || resource["id"] != "deployment" {
			continue
		}
		deploymentTemplate, found, err := unstructured.NestedMap(resource, "template")
		if err != nil || !found || deploymentTemplate["apiVersion"] != "apps/v1" || deploymentTemplate["kind"] != "Deployment" {
			return false
		}
		replicas, found, err := unstructured.NestedInt64(resource, "template", "spec", "replicas")
		if err != nil || !found || replicas != 1 {
			return false
		}
		strategy, found, err := unstructured.NestedString(resource, "template", "spec", "strategy", "type")
		if err != nil || !found || strategy != "Recreate" {
			return false
		}
		containers, found, err := unstructured.NestedSlice(resource, "template", "spec", "template", "spec", "containers")
		if err != nil || !found || len(containers) != 1 {
			return false
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(resource, "template", "spec", "template", "spec", "initContainers"); found {
			return false
		}
		container, ok := containers[0].(map[string]any)
		if !ok {
			return false
		}
		image, found, err := unstructured.NestedString(container, "image")
		if err != nil || !found || image != managedBrowserImage {
			return false
		}
		for _, override := range []string{"command", "env", "envFrom", "lifecycle", "livenessProbe", "startupProbe"} {
			if _, found, _ := unstructured.NestedFieldNoCopy(container, override); found {
				return false
			}
		}
		readinessProbe, found, err := unstructured.NestedMap(container, "readinessProbe")
		if err != nil || !found {
			return false
		}
		readinessPort, found, err := unstructured.NestedInt64(readinessProbe, "tcpSocket", "port")
		if err != nil || !found || readinessPort != 8931 {
			return false
		}
		for _, override := range []string{"exec", "httpGet", "grpc"} {
			if _, found, _ := unstructured.NestedFieldNoCopy(readinessProbe, override); found {
				return false
			}
		}
		mounts, found, err := unstructured.NestedSlice(container, "volumeMounts")
		if err != nil || !found || len(mounts) != 1 {
			return false
		}
		mount, ok := mounts[0].(map[string]any)
		if !ok || mount["name"] != "dshm" || mount["mountPath"] != "/dev/shm" {
			return false
		}
		volumes, found, err := unstructured.NestedSlice(resource, "template", "spec", "template", "spec", "volumes")
		if err != nil || !found || len(volumes) != 1 {
			return false
		}
		volume, ok := volumes[0].(map[string]any)
		if !ok || volume["name"] != "dshm" {
			return false
		}
		medium, found, err := unstructured.NestedString(volume, "emptyDir", "medium")
		if err != nil || !found || medium != "Memory" {
			return false
		}
		args, found, err := unstructured.NestedStringSlice(container, "args")
		if err != nil || !found || len(args) != len(managedBrowserArgs) {
			return false
		}
		for index := range args {
			if args[index] != managedBrowserArgs[index] {
				return false
			}
		}
		readyWhen, found, err := unstructured.NestedStringSlice(resource, "readyWhen")
		if err != nil || !found || len(readyWhen) != 1 || readyWhen[0] != managedBrowserDeploymentReadyExpression {
			return false
		}
		isolationStatus, found, err := unstructured.NestedString(template.Object, "spec", "backendConfig", "status", "mcpIsolationReady")
		return err == nil && found && isolationStatus == managedBrowserIsolationStatusExpression
	}
	return false
}

// studioBackend reads one ready shared service (search/browser) off the Studio
// singleton, returning its plural resource and instance name, or empty when the
// service is absent, disabled, or not yet Ready.
func (s *Server) studioBackend(ctx context.Context, c *asclient.Client, service string) (resource, name string) {
	if c == nil {
		return "", ""
	}
	st, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return "", ""
	}
	return studioBackendFromObject(st, service)
}

func studioBackendFromObject(st *unstructured.Unstructured, service string) (resource, name string) {
	if st == nil {
		return "", ""
	}
	if disabled, _, _ := unstructured.NestedBool(st.Object, "spec", service, "disabled"); disabled {
		return "", ""
	}
	phase, _, _ := unstructured.NestedString(st.Object, "status", service, "phase")
	if phase != aiv1alpha1.StudioServiceReady {
		return "", ""
	}
	resource, _, _ = unstructured.NestedString(st.Object, "status", service, "resource")
	name, _, _ = unstructured.NestedString(st.Object, "status", service, "instance")
	return resource, name
}
