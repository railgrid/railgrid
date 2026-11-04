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

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/apiexportgen"
	sdkinstall "github.com/railgrid/provider-sdk/install"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	providersv1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const (
	orgProviderBootstrapSchemaLimit         = 128
	orgProviderBootstrapExportResourceLimit = 1024
	orgProviderBootstrapVirtualLimit        = 16
	orgProviderBootstrapBodyLimit           = 8 << 20
)

// OrgProviderBootstrapHandler accepts a provider declaration only from the
// restricted ServiceAccount in the Hub-registered organization workspace.
// The Hub validates and applies every object that can affect routing or RBAC.
type OrgProviderBootstrapHandler struct {
	handler http.Handler
}

func (h *OrgProviderBootstrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

// NewOrgProviderBootstrapHandler creates the authenticated bootstrap endpoint.
// An empty relay base is allowed at construction so a platform that does not
// configure provider relay can still serve ordinary platform providers; an
// org bootstrap will receive a clear 503 when it needs the relay.
func NewOrgProviderBootstrapHandler(config *rest.Config, reg *Registry, relayBase string) (*OrgProviderBootstrapHandler, error) {
	authenticate, err := NewOrgProviderBootstrapAuthenticator(config)
	if err != nil {
		return nil, err
	}
	return newOrgProviderBootstrapHandler(config, reg, relayBase, authenticate), nil
}

func newOrgProviderBootstrapHandler(config *rest.Config, reg *Registry, relayBase string, authenticate OrgProviderBootstrapAuthenticator) *OrgProviderBootstrapHandler {
	if reg == nil {
		reg = NewRegistry()
	}
	provisioner := NewProvisioner(config)
	h := &OrgProviderBootstrapHandler{}
	authInflight := make(chan struct{}, 16)
	admission := newOrgBootstrapAdmission()
	h.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := orgBootstrapRequestName(r)
		if !ok || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		select {
		case authInflight <- struct{}{}:
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "provider bootstrap is busy; retry shortly", http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		authCtx, authCancel := context.WithTimeout(ctx, 10*time.Second)
		identity, err := authenticate(authCtx, r, name)
		authCancel()
		<-authInflight
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrOrgProviderBootstrapUnavailable) {
				status = http.StatusServiceUnavailable
				klog.FromContext(ctx).Error(err, "organization provider bootstrap authentication unavailable", "provider", name)
			}
			http.Error(w, "provider bootstrap authentication failed", status)
			return
		}
		release, ok := admission.acquire(identity.OrgUUID)
		if !ok {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "provider bootstrap is busy; retry shortly", http.StatusTooManyRequests)
			return
		}
		defer release()
		if err := setOptionalBootstrapReadDeadline(w); err != nil {
			http.Error(w, "bootstrap request deadlines unavailable", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = http.NewResponseController(w).SetReadDeadline(time.Time{}) }()
		var request sdkinstall.OrganizationBootstrap
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, orgProviderBootstrapBodyLimit))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid bootstrap declaration", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "expected one bootstrap declaration", http.StatusBadRequest)
			return
		}
		if err := http.NewResponseController(w).SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(w, "bootstrap request deadline reset failed", http.StatusServiceUnavailable)
			return
		}
		admitted, err := validateOrgBootstrap(name, identity.OrgUUID, reg, request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		relayURL, err := ProviderDataPlaneURL(relayBase, identity.Cluster, name)
		if err != nil {
			http.Error(w, "provider transport is not configured", http.StatusServiceUnavailable)
			return
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, identity.Cluster)
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			http.Error(w, "bootstrap client unavailable", http.StatusServiceUnavailable)
			return
		}
		// A provider's APIExport name is part of the binding coordinate. Once
		// installed, changing it would strand existing tenant bindings.
		existing, err := client.Resource(orgCatalogGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			http.Error(w, "could not read existing declaration", http.StatusServiceUnavailable)
			return
		}
		if err == nil {
			previous, _, _ := unstructured.NestedString(existing.Object, "spec", "export", "name")
			if previous != "" && previous != admitted.APIExport.GetName() {
				http.Error(w, "provider export name cannot change after registration", http.StatusConflict)
				return
			}
		}
		reservations, err := orgGroupClient(config)
		if err == nil {
			parentConfig := rest.CopyConfig(config)
			parentConfig.Host = apiurl.KCPClusterURL(config.Host, kcppaths.OrgProvidersParent(identity.OrgUUID))
			registrations, clientErr := dynamic.NewForConfig(parentConfig)
			if clientErr != nil {
				err = clientErr
			} else {
				err = reserveOrgProviderGroups(ctx, reservations, registrations, identity, admitted)
			}
		}
		if err != nil {
			status := http.StatusServiceUnavailable
			if apierrors.IsConflict(err) {
				status = http.StatusConflict
			}
			http.Error(w, "could not reserve provider API groups; retry or use a different group", status)
			return
		}
		if err := removeOrgProviderBindGrants(ctx, client); err != nil {
			klog.FromContext(ctx).Error(err, "could not remove legacy organization bind grants", "provider", name)
			http.Error(w, "could not retire legacy provider grants", http.StatusServiceUnavailable)
			return
		}
		if err := ensureOrgProviderClaimPolicy(ctx, config, identity, admitted); err != nil {
			klog.FromContext(ctx).Error(err, "could not prepare organization provider permission claim policy",
				"provider", name, "org", identity.OrgUUID)
			http.Error(w, "bootstrap could not be applied; retry the install", http.StatusServiceUnavailable)
			return
		}
		if err := applyOrgBootstrap(ctx, client, provisioner, identity, admitted, relayURL); err != nil {
			// Keep details in the Hub log for operators; never echo or log the
			// request body, transport configuration, or credentials.
			klog.FromContext(ctx).Error(err, "could not apply organization provider bootstrap",
				"provider", name, "org", identity.OrgUUID)
			http.Error(w, "bootstrap could not be applied; retry the install", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return h
}

func orgBootstrapRequestName(r *http.Request) (string, bool) {
	const prefix, suffix = "/api/providers/", "/bootstrap"
	if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
	if len(validation.IsDNS1123Label(name)) != 0 || r.URL.Path != prefix+name+suffix {
		return "", false
	}
	return name, true
}

func setOptionalBootstrapReadDeadline(w http.ResponseWriter) error {
	err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

var (
	orgCatalogGVR    = schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
	orgSchemaGVR     = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	orgExportGVR     = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	orgCRDGVR        = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	orgCacheGVR      = schema.GroupVersionResource{Group: "cache.kcp.io", Version: "v1alpha1", Resource: "clustercachedresources"}
	orgCacheSliceGVR = schema.GroupVersionResource{Group: "cache.kcp.io", Version: "v1alpha1", Resource: "clustercachedresourceendpointslices"}
)

// sanitizeDeclaration keeps only object identity and spec. Provider-supplied
// status, owner references, labels, annotations, and kcp replication metadata
// never reach the Hub client.
func sanitizeDeclaration(object *unstructured.Unstructured, apiVersion, kind string) (*unstructured.Unstructured, error) {
	if object == nil || object.GetAPIVersion() != apiVersion || object.GetKind() != kind || len(validation.IsDNS1123Subdomain(object.GetName())) != 0 {
		return nil, fmt.Errorf("expected a named %s %s", apiVersion, kind)
	}
	spec, ok, err := unstructured.NestedMap(object.Object, "spec")
	if err != nil || !ok {
		return nil, fmt.Errorf("%s requires spec", kind)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": object.GetName()}, "spec": spec,
	}}, nil
}

func validateOrgBootstrap(name, org string, reg *Registry, request sdkinstall.OrganizationBootstrap) (sdkinstall.OrganizationBootstrap, error) {
	var out sdkinstall.OrganizationBootstrap
	var err error
	out.CatalogEntry, err = sanitizeDeclaration(request.CatalogEntry, "providers.railgrid.ai/v1alpha1", "CatalogEntry")
	if err != nil {
		return out, err
	}
	if out.CatalogEntry.GetName() != name {
		return out, fmt.Errorf("CatalogEntry must name the registered provider")
	}
	var entry providersv1.CatalogEntry
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(out.CatalogEntry.Object, &entry); err != nil {
		return out, fmt.Errorf("invalid CatalogEntry")
	}
	if err := validateExportDeclaration(&entry); err != nil {
		return out, err
	}
	if entry.Spec.Export == nil || entry.Spec.Export.Name == "" {
		return out, fmt.Errorf("provider requires spec.export")
	}
	if entry.Spec.Serving != nil && entry.Spec.Serving.Backend != nil {
		if err := validateOrgBackendURL(entry.Spec.Serving.Backend.URL); err != nil {
			return out, err
		}
	}
	if catalogDeclaresSubresources(&entry) && (entry.Spec.Serving == nil || entry.Spec.Serving.Backend == nil) {
		return out, fmt.Errorf("provider with custom subresources requires a backend Service URL")
	}
	// Serialize the typed spec back into the admitted object so unrecognized
	// fields from the unstructured request cannot survive into the CatalogEntry.
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&entry.Spec)
	if err != nil {
		return out, fmt.Errorf("invalid CatalogEntry spec")
	}
	out.CatalogEntry.Object["spec"] = spec
	if reg != nil {
		if err := (&CatalogReconciler{reg: reg}).validateRequiresDeclaration(org, &entry); err != nil {
			return out, err
		}
	}
	for _, requirement := range entry.Spec.Requires {
		if requirement.Provider != "" {
			if reg != nil {
				dependency, ok := reg.GetForOrg(org, requirement.Provider)
				if !ok || !containsString(dependency.APIGroups, requirement.Group) {
					return out, fmt.Errorf("required provider must be registered and serve its declared group")
				}
			}
			continue
		}
		for _, resource := range requirement.Resources {
			allowed := requirement.Group == "" && (resource.Name == "secrets" || resource.Name == "configmaps")
			allowed = allowed || requirement.Group == "authorization.k8s.io" && resource.Name == "subjectaccessreviews"
			if !allowed {
				return out, fmt.Errorf("requirement %s/%s is not an allowed builtin; name its provider", requirement.Group, resource.Name)
			}
		}
	}
	for _, resource := range entry.Spec.Export.Resources {
		group := strings.Split(resource.APIVersion, "/")[0]
		if err := validateOrgProviderGroupOwnership(org, name, group, reg); err != nil {
			return out, err
		}
	}

	out.APIExport, err = sanitizeDeclaration(request.APIExport, "apis.kcp.io/v1alpha2", "APIExport")
	if err != nil {
		return out, err
	}
	if out.APIExport.GetName() != entry.Spec.Export.Name {
		return out, fmt.Errorf("APIExport must match CatalogEntry spec.export.name")
	}
	if len(request.Schemas) == 0 || len(request.Schemas) > orgProviderBootstrapSchemaLimit {
		return out, fmt.Errorf("schema count must be between 1 and %d", orgProviderBootstrapSchemaLimit)
	}
	if len(request.PrivateCRDs) > OrgProviderPrivateResourceLimit {
		return out, fmt.Errorf("private CRD count exceeds %d", OrgProviderPrivateResourceLimit)
	}
	if len(request.VirtualStorage) > orgProviderBootstrapVirtualLimit {
		return out, fmt.Errorf("virtual storage count exceeds %d", orgProviderBootstrapVirtualLimit)
	}
	schemas := map[string]*unstructured.Unstructured{}
	schemaCoordinates := map[string]bool{}
	for _, raw := range request.Schemas {
		s, err := sanitizeDeclaration(raw, "apis.kcp.io/v1alpha1", "APIResourceSchema")
		if err != nil {
			return out, err
		}
		group, _, _ := unstructured.NestedString(s.Object, "spec", "group")
		resource, _, _ := unstructured.NestedString(s.Object, "spec", "names", "plural")
		coordinate := schema.GroupResource{Group: group, Resource: resource}
		if err := ValidateOrgProviderPrivateResource(coordinate); err != nil {
			return out, err
		}
		if err := validateOrgProviderGroupOwnership(org, name, group, reg); err != nil {
			return out, err
		}
		key := group + "/" + resource
		if schemaCoordinates[key] {
			return out, fmt.Errorf("duplicate schema resource %s", key)
		}
		schemaCoordinates[key] = true
		if _, ok := schemas[s.GetName()]; ok {
			return out, fmt.Errorf("duplicate schema %s", s.GetName())
		}
		if err := rejectConversion(s); err != nil {
			return out, err
		}
		schemas[s.GetName()] = s
		out.Schemas = append(out.Schemas, s)
	}

	privateCRDs, privateResources, privateVersions, err := sanitizePrivateCRDs(org, name, reg, request.PrivateCRDs)
	if err != nil {
		return out, err
	}
	out.PrivateCRDs = privateCRDs
	virtualStorage, err := validateOrgVirtualStorage(request.VirtualStorage, schemas, privateResources, privateVersions)
	if err != nil {
		return out, err
	}
	privateScopes := map[string]string{}
	for _, crd := range privateCRDs {
		group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
		resource, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "plural")
		scope, _, _ := unstructured.NestedString(crd.Object, "spec", "scope")
		privateScopes[group+"/"+resource] = scope
	}
	for _, projection := range virtualStorage {
		if privateScopes[projection.Group+"/"+projection.Resource] != "Cluster" {
			return out, fmt.Errorf("virtual storage requires a cluster-scoped private resource")
		}
	}
	out.VirtualStorage = virtualStorage
	aliases, err := validateOrgEndpointSliceAliases(out.APIExport.GetName(), request.APIExportEndpointSliceAliases)
	if err != nil {
		return out, err
	}
	out.APIExportEndpointSliceAliases = aliases

	resources, found, err := unstructured.NestedSlice(out.APIExport.Object, "spec", "resources")
	if err != nil || !found || len(resources) == 0 || len(resources) > orgProviderBootstrapExportResourceLimit {
		return out, fmt.Errorf("APIExport resource count must be between 1 and %d", orgProviderBootstrapExportResourceLimit)
	}
	parentScopes := map[string]string{}
	for _, raw := range resources {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		resource, _ := item["name"].(string)
		group, _ := item["group"].(string)
		schemaName, _ := item["schema"].(string)
		if strings.Contains(resource, "/") || schemas[schemaName] == nil {
			continue
		}
		scope, _, _ := unstructured.NestedString(schemas[schemaName].Object, "spec", "scope")
		parentScopes[group+"/"+resource] = scope
	}
	declared := map[string]bool{}
	for _, resource := range entry.Spec.Export.Resources {
		group := strings.Split(resource.APIVersion, "/")[0]
		for _, verb := range resource.Verbs {
			declared[group+"/"+resource.Name+"/"+verb.Name] = true
		}
		for _, action := range resource.Actions {
			declared[group+"/"+resource.Name+"/"+action.Name] = true
		}
	}
	virtualByResource := make(map[string]sdkinstall.VirtualStorageResource, len(out.VirtualStorage))
	for _, virtual := range out.VirtualStorage {
		virtualByResource[virtual.Group+"/"+virtual.Resource] = virtual
	}
	seen := map[string]bool{}
	cleanResources := make([]any, 0, len(resources))
	for _, raw := range resources {
		item, ok := raw.(map[string]any)
		if !ok {
			return out, fmt.Errorf("invalid exported resource")
		}
		group, _ := item["group"].(string)
		resource, _ := item["name"].(string)
		schemaName, _ := item["schema"].(string)
		parts := strings.Split(resource, "/")
		if len(parts) > 2 || parts[0] == "" {
			return out, fmt.Errorf("invalid resource coordinate")
		}
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: group, Resource: parts[0]}); err != nil {
			return out, err
		}
		if err := validateOrgProviderGroupOwnership(org, name, group, reg); err != nil {
			return out, err
		}
		key := group + "/" + resource
		if seen[key] {
			return out, fmt.Errorf("duplicate resource coordinate")
		}
		seen[key] = true
		resourceSchema := schemas[schemaName]
		if resourceSchema == nil {
			return out, fmt.Errorf("resource requires a supplied schema")
		}
		schemaGroup, _, _ := unstructured.NestedString(resourceSchema.Object, "spec", "group")
		if schemaGroup != group {
			return out, fmt.Errorf("resource and schema groups differ")
		}
		for _, declaration := range entry.Spec.Export.Resources {
			gv, _ := schema.ParseGroupVersion(declaration.APIVersion)
			if declaration.Name != parts[0] || gv.Group != group {
				continue
			}
			versions, _, _ := unstructured.NestedSlice(resourceSchema.Object, "spec", "versions")
			served := false
			for _, rawVersion := range versions {
				version, ok := rawVersion.(map[string]any)
				if ok && version["name"] == gv.Version && version["served"] == true {
					served = true
				}
			}
			if !served {
				return out, fmt.Errorf("schema %s does not serve declared version %s", schemaName, declaration.APIVersion)
			}
		}
		storage, ok := item["storage"].(map[string]any)
		if !ok {
			return out, fmt.Errorf("resource requires storage")
		}
		clean := map[string]any{"group": group, "name": resource, "schema": schemaName}
		if len(parts) == 2 {
			if parentScopes[group+"/"+parts[0]] != "Cluster" {
				return out, fmt.Errorf("custom subresource %s requires a cluster-scoped parent", resource)
			}
			if !declared[key] || parts[1] == "status" || parts[1] == "scale" || !validSubresourceName(parts[1]) {
				return out, fmt.Errorf("undeclared custom subresource")
			}
			clean["storage"] = map[string]any{"virtual": map[string]any{"reference": map[string]any{
				"apiGroup": "dataplane.railgrid.ai", "kind": "DataPlaneEndpointSlice", "name": out.APIExport.GetName(),
			}}}
		} else {
			plural, _, _ := unstructured.NestedString(resourceSchema.Object, "spec", "names", "plural")
			if plural != resource {
				return out, fmt.Errorf("ordinary resource and schema names differ")
			}
			if virtual, ok := virtualByResource[key]; ok {
				clean["storage"] = map[string]any{"virtual": map[string]any{"reference": map[string]any{
					"apiGroup": "cache.kcp.io", "kind": "ClusterCachedResourceEndpointSlice", "name": virtual.Name,
				}}}
			} else {
				if _, ok := storage["crd"].(map[string]any); !ok || len(storage) != 1 {
					return out, fmt.Errorf("ordinary BYO resources require CRD storage or an admitted local cache projection")
				}
				clean["storage"] = map[string]any{"crd": map[string]any{}}
			}
		}
		cleanResources = append(cleanResources, clean)
	}
	for key := range declared {
		if !seen[key] {
			return out, fmt.Errorf("declared subresource missing from APIExport")
		}
	}
	for key := range virtualByResource {
		if !seen[key] {
			return out, fmt.Errorf("virtual storage resource %s is not exported", key)
		}
	}
	rawEntry, _ := json.Marshal(out.CatalogEntry.Object)
	requirements, err := apiexportgen.ParseRequirements(rawEntry)
	if err != nil {
		return out, err
	}
	claims, err := apiexportgen.ExportClaims(requirements)
	if err != nil {
		return out, err
	}
	// Reconstruct the entire spec. Hidden claims, identities, arbitrary storage,
	// URLs, and provider-supplied policy fields are never forwarded to kcp.
	out.APIExport.Object["spec"] = map[string]any{"resources": cleanResources, "permissionClaims": claims}
	if err := sdkinstall.ValidateClaimScopes(out.APIExport); err != nil {
		return out, err
	}
	return out, nil
}

func catalogDeclaresSubresources(entry *providersv1.CatalogEntry) bool {
	if entry == nil || entry.Spec.Export == nil {
		return false
	}
	for _, resource := range entry.Spec.Export.Resources {
		if len(resource.Verbs) > 0 || len(resource.Actions) > 0 {
			return true
		}
	}
	return false
}

func validateOrgProviderGroupOwnership(org, name, group string, reg *Registry) error {
	if reg == nil || group == "" {
		return nil
	}
	for _, existing := range reg.ListForOrg(org) {
		if existing.Name != name && containsString(existing.APIGroups, group) {
			return fmt.Errorf("API group %q is already served by provider %q", group, existing.Name)
		}
	}
	return nil
}

func validateOrgBackendURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("organization provider backend must name a Service on its selected Edge")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("organization provider backend must be an http(s) Service URL on its selected Edge")
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	host = strings.TrimSuffix(host, ".cluster.local")
	labels := strings.Split(host, ".")
	if len(labels) != 3 || labels[2] != "svc" || len(validation.IsDNS1123Label(labels[0])) != 0 || len(validation.IsDNS1123Label(labels[1])) != 0 {
		return fmt.Errorf("organization provider backend must use <service>.<namespace>.svc[.cluster.local] DNS")
	}
	if port := u.Port(); port != "" {
		for _, r := range port {
			if r < '0' || r > '9' {
				return fmt.Errorf("organization provider backend has an invalid Service port")
			}
		}
		var value int
		if _, err := fmt.Sscan(port, &value); err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("organization provider backend has an invalid Service port")
		}
	}
	return nil
}

func validSubresourceName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func sanitizePrivateCRDs(org, provider string, reg *Registry, rawCRDs []*unstructured.Unstructured) ([]*unstructured.Unstructured, []schema.GroupResource, map[string]map[string]bool, error) {
	var out []*unstructured.Unstructured
	var resources []schema.GroupResource
	versions := map[string]map[string]bool{}
	seen := map[string]bool{}
	for _, raw := range rawCRDs {
		crd, err := sanitizeDeclaration(raw, "apiextensions.k8s.io/v1", "CustomResourceDefinition")
		if err != nil {
			return nil, nil, nil, err
		}
		var typed apiextensionsv1.CustomResourceDefinition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(crd.Object, &typed); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid private CRD")
		}
		group, plural := typed.Spec.Group, typed.Spec.Names.Plural
		coordinate := schema.GroupResource{Group: group, Resource: plural}
		if err := ValidateOrgProviderPrivateResource(coordinate); err != nil {
			return nil, nil, nil, err
		}
		if err := validateOrgProviderGroupOwnership(org, provider, group, reg); err != nil {
			return nil, nil, nil, err
		}
		name := plural + "." + group
		if crd.GetName() != name || seen[name] || (typed.Spec.Scope != apiextensionsv1.ClusterScoped && typed.Spec.Scope != apiextensionsv1.NamespaceScoped) || len(typed.Spec.Versions) == 0 || len(typed.Spec.Versions) > 16 {
			return nil, nil, nil, fmt.Errorf("invalid or duplicate private CRD")
		}
		if typed.Spec.Conversion != nil && (typed.Spec.Conversion.Strategy != apiextensionsv1.NoneConverter || typed.Spec.Conversion.Webhook != nil) {
			return nil, nil, nil, fmt.Errorf("private CRD conversion webhooks are not permitted")
		}
		storageVersions := 0
		versionSet := map[string]bool{}
		for _, version := range typed.Spec.Versions {
			if version.Name == "" || versionSet[version.Name] {
				return nil, nil, nil, fmt.Errorf("private CRD %s has an invalid version list", name)
			}
			versionSet[version.Name] = true
			if version.Storage {
				storageVersions++
			}
		}
		if storageVersions != 1 {
			return nil, nil, nil, fmt.Errorf("private CRD %s must declare exactly one storage version", name)
		}
		// Rebuild from the typed CRD so unknown fields cannot reach the Hub API.
		cleanSpec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&typed.Spec)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid private CRD spec")
		}
		clean := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": name}, "spec": cleanSpec,
		}}
		seen[name] = true
		out = append(out, clean)
		resources = append(resources, coordinate)
		versions[group+"/"+plural] = versionSet
	}
	return out, resources, versions, nil
}

func validateOrgVirtualStorage(raw []sdkinstall.VirtualStorageResource, schemas map[string]*unstructured.Unstructured, privateResources []schema.GroupResource, privateVersions map[string]map[string]bool) ([]sdkinstall.VirtualStorageResource, error) {
	private := map[string]bool{}
	for _, resource := range privateResources {
		private[resource.Group+"/"+resource.Resource] = true
	}
	seenResource, seenName := map[string]bool{}, map[string]bool{}
	out := make([]sdkinstall.VirtualStorageResource, 0, len(raw))
	for _, declaration := range raw {
		coordinate := schema.GroupResource{Group: declaration.Group, Resource: declaration.Resource}
		if err := ValidateOrgProviderPrivateResource(coordinate); err != nil {
			return nil, err
		}
		key := declaration.Group + "/" + declaration.Resource
		if seenResource[key] || !private[key] || len(validation.IsDNS1123Label(declaration.Name)) != 0 || seenName[declaration.Name] {
			return nil, fmt.Errorf("invalid or duplicate local virtual storage declaration")
		}
		seenResource[key], seenName[declaration.Name] = true, true
		storageVersion := ""
		for _, object := range schemas {
			group, _, _ := unstructured.NestedString(object.Object, "spec", "group")
			plural, _, _ := unstructured.NestedString(object.Object, "spec", "names", "plural")
			if group != declaration.Group || plural != declaration.Resource {
				continue
			}
			scope, _, _ := unstructured.NestedString(object.Object, "spec", "scope")
			if scope != "Cluster" {
				return nil, fmt.Errorf("virtual storage resource %s requires a cluster-scoped schema", key)
			}
			versions, found, err := unstructured.NestedSlice(object.Object, "spec", "versions")
			if err != nil || !found {
				return nil, fmt.Errorf("virtual storage resource %s has no schema versions", key)
			}
			for _, rawVersion := range versions {
				version, ok := rawVersion.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("virtual storage resource %s has an invalid schema version", key)
				}
				name, _ := version["name"].(string)
				storage, _ := version["storage"].(bool)
				served, _ := version["served"].(bool)
				if storage && served {
					if storageVersion != "" {
						return nil, fmt.Errorf("virtual storage schema %s has multiple storage versions", key)
					}
					storageVersion = name
				}
			}
			break
		}
		if storageVersion == "" || !privateVersions[key][storageVersion] {
			return nil, fmt.Errorf("virtual storage resource %s must be backed by a matching private CRD", key)
		}
		if declaration.Version != "" && declaration.Version != storageVersion {
			return nil, fmt.Errorf("virtual storage resource %s has a mismatched version", key)
		}
		declaration.Version = storageVersion
		out = append(out, declaration)
	}
	return out, nil
}

func validateOrgEndpointSliceAliases(exportName string, aliases []string) ([]string, error) {
	seen := map[string]bool{exportName: true}
	out := make([]string, 0, len(aliases))
	if len(aliases) > 16 {
		return nil, fmt.Errorf("APIExport endpoint slice alias count exceeds 16")
	}
	for _, alias := range aliases {
		if len(validation.IsDNS1123Label(alias)) != 0 || seen[alias] {
			return nil, fmt.Errorf("invalid or duplicate APIExport endpoint slice alias")
		}
		seen[alias] = true
		out = append(out, alias)
	}
	return out, nil
}

func rejectConversion(object *unstructured.Unstructured) error {
	conversion, found, err := unstructured.NestedMap(object.Object, "spec", "conversion")
	if err != nil {
		return fmt.Errorf("invalid schema conversion")
	}
	if found {
		strategy, _ := conversion["strategy"].(string)
		if strategy != "None" || len(conversion) != 1 {
			return fmt.Errorf("BYO schema conversion webhooks are not permitted")
		}
	}
	return nil
}

func applyOrgBootstrap(ctx context.Context, client dynamic.Interface, provisioner *Provisioner, identity OrgBootstrapIdentity, request sdkinstall.OrganizationBootstrap, relayURL string) error {
	for _, object := range request.Schemas {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			_, err := client.Resource(orgSchemaGVR).Create(ctx, object, metav1.CreateOptions{})
			return err
		})
		if apierrors.IsAlreadyExists(err) {
			existing, getErr := client.Resource(orgSchemaGVR).Get(ctx, object.GetName(), metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			if !apiequality.Semantic.DeepDerivative(object.Object["spec"], existing.Object["spec"]) {
				return fmt.Errorf("schema %s already exists with different contents; use a new versioned name", object.GetName())
			}
		} else if err != nil {
			return err
		}
	}
	privateResources := make([]schema.GroupResource, 0, len(request.PrivateCRDs))
	for _, crd := range request.PrivateCRDs {
		if err := applyOrgObject(ctx, client, orgCRDGVR, crd); err != nil {
			return err
		}
		group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
		resource, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "plural")
		privateResources = append(privateResources, schema.GroupResource{Group: group, Resource: resource})
	}
	for _, virtual := range request.VirtualStorage {
		cacheResource := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cache.kcp.io/v1alpha1", "kind": "ClusterCachedResource",
			"metadata": map[string]any{"name": virtual.Name},
			"spec":     map[string]any{"group": virtual.Group, "version": virtual.Version, "resource": virtual.Resource},
		}}
		if err := applyOrgObject(ctx, client, orgCacheGVR, cacheResource); err != nil {
			return err
		}
		cacheSlice := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cache.kcp.io/v1alpha1", "kind": "ClusterCachedResourceEndpointSlice",
			"metadata": map[string]any{"name": virtual.Name},
			"spec": map[string]any{
				"clusterCachedResource": map[string]any{"name": virtual.Name},
				"export":                map[string]any{"name": request.APIExport.GetName(), "path": identity.WorkspacePath},
			},
		}}
		if err := applyOrgObject(ctx, client, orgCacheSliceGVR, cacheSlice); err != nil {
			return err
		}
	}
	if sdkinstall.ExportDeclaresSubresources(request.APIExport) {
		if err := sdkinstall.ApplyDataPlaneEndpointCRD(ctx, client); err != nil {
			return err
		}
		if err := sdkinstall.EnsureDataPlaneEndpointSlice(ctx, client, request.APIExport.GetName(), relayURL); err != nil {
			return err
		}
	}
	if err := applyOrgObject(ctx, client, orgExportGVR, request.APIExport); err != nil {
		return err
	}
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, client, request.APIExport.GetName(), request.APIExport.GetName(), identity.WorkspacePath); err != nil {
		return err
	}
	for _, alias := range request.APIExportEndpointSliceAliases {
		if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, client, alias, request.APIExport.GetName(), identity.WorkspacePath); err != nil {
			return err
		}
	}
	if err := provisioner.EnsureOrgProviderPrivateResourceRBAC(ctx, identity.WorkspacePath, privateResources); err != nil {
		return err
	}
	return applyOrgObject(ctx, client, orgCatalogGVR, request.CatalogEntry)
}

func applyOrgObject(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, desired *unstructured.Unstructured) error {
	return applyUnstructured(ctx, client, gvr, desired)
}
