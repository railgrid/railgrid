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
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/apiexportgen"
	sdkinstall "github.com/railgrid/provider-sdk/install"
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

	providersv1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

// OrgProviderBootstrapHandler installs validated declarations on behalf of
// restricted organization providers after resource containment is verified.
type OrgProviderBootstrapHandler struct {
	handler             http.Handler
	availabilityCheck   func(context.Context) error
	consumerPreparation func(context.Context, string, string, sdkinstall.OrganizationBootstrap) error
	lifecycleLock       func(context.Context, string) (func(), error)
}

// SetAvailabilityCheck installs the resource-containment readiness gate before
// serving requests. Without it authenticated bootstrap declarations are refused.
func (h *OrgProviderBootstrapHandler) SetAvailabilityCheck(check func(context.Context) error) {
	h.availabilityCheck = check
}

// SetConsumerPreparation installs storage-policy preparation for a validated
// declaration before any schema, export, private-resource or catalog writes.
// An absent callback refuses new declarations.
func (h *OrgProviderBootstrapHandler) SetConsumerPreparation(prepare func(context.Context, string, string, sdkinstall.OrganizationBootstrap) error) {
	h.consumerPreparation = prepare
}

// SetLifecycleLock serializes the validated declaration and all its preparation
// and writes with the org's Register/Enable/Disable/Delete/credential operations.
func (h *OrgProviderBootstrapHandler) SetLifecycleLock(lock func(context.Context, string) (func(), error)) {
	h.lifecycleLock = lock
}

func (h *OrgProviderBootstrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

// NewOrgProviderBootstrapHandler authenticates the restricted runtime identity;
// it never grants providers direct declaration or RBAC write access.
func NewOrgProviderBootstrapHandler(config *rest.Config, reg *Registry, relayBase string) (*OrgProviderBootstrapHandler, error) {
	authenticate, err := NewOrgProviderBootstrapAuthenticator(config)
	if err != nil {
		return nil, err
	}
	provisioner := NewProvisioner(config)
	h := &OrgProviderBootstrapHandler{}
	// Declaration decoding and schema admission are materially more expensive
	// than ordinary reads. Bound their aggregate in-flight memory/work per hub.
	inflight := make(chan struct{}, 8)
	h.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/providers/"), "/bootstrap")
		if r.Method != http.MethodPost || r.URL.Path != "/api/providers/"+name+"/bootstrap" || len(validation.IsDNS1123Label(name)) != 0 {
			http.NotFound(w, r)
			return
		}
		select {
		case inflight <- struct{}{}:
			defer func() { <-inflight }()
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
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrOrgProviderBootstrapUnavailable) {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, "provider bootstrap authentication failed", status)
			return
		}
		if h.availabilityCheck == nil {
			http.Error(w, "organization provider resource containment is not configured", http.StatusServiceUnavailable)
			return
		}
		if err := h.availabilityCheck(ctx); err != nil {
			http.Error(w, "organization provider resource containment unavailable", http.StatusServiceUnavailable)
			return
		}
		// Context cancellation alone does not interrupt a slow request-body
		// reader. Bound the upload before allowing it to occupy an admission slot.
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			http.Error(w, "bootstrap request deadlines unavailable", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
		var request sdkinstall.OrganizationBootstrap
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid bootstrap declaration", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "expected one bootstrap declaration", http.StatusBadRequest)
			return
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			http.Error(w, "bootstrap request deadline reset failed", http.StatusServiceUnavailable)
			return
		}
		if h.lifecycleLock == nil {
			http.Error(w, "organization provider lifecycle containment is not configured", http.StatusServiceUnavailable)
			return
		}
		unlock, err := h.lifecycleLock(ctx, identity.OrgUUID)
		if err != nil || unlock == nil {
			http.Error(w, "organization provider lifecycle unavailable", http.StatusServiceUnavailable)
			return
		}
		defer unlock()
		// Enrollment may have been deleted or replaced while this request waited
		// for a lifecycle operation. Recheck the live identity under the lock.
		authCtx, authCancel = context.WithTimeout(ctx, 10*time.Second)
		currentIdentity, err := authenticate(authCtx, r, name)
		authCancel()
		if err != nil || currentIdentity != identity {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrOrgProviderBootstrapUnavailable) {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, "provider bootstrap identity changed or is unavailable", status)
			return
		}
		if err := h.availabilityCheck(ctx); err != nil {
			http.Error(w, "organization provider resource containment unavailable", http.StatusServiceUnavailable)
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
		// A changed export name would strand existing tenant bindings.
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
		if h.consumerPreparation == nil {
			http.Error(w, "organization provider consumer containment is not configured", http.StatusServiceUnavailable)
			return
		}
		if err := h.consumerPreparation(ctx, identity.OrgUUID, name, admitted); err != nil {
			http.Error(w, "organization provider consumer containment could not be prepared", http.StatusServiceUnavailable)
			return
		}
		if err := applyOrgBootstrap(ctx, client, provisioner, identity, admitted, relayURL); err != nil {
			// Do not echo transport configuration, API responses or credentials.
			http.Error(w, "bootstrap could not be applied; retry the install", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return h, nil
}

var (
	orgCatalogGVR = schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
	orgSchemaGVR  = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}
	orgExportGVR  = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	orgCRDGVR     = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// sanitizeDeclaration copies only identity and spec. In particular, provider
// supplied status, owner references and kcp replication annotations never reach
// the admin client used to install these objects.
func sanitizeDeclaration(object *unstructured.Unstructured, apiVersion, kind string) (*unstructured.Unstructured, error) {
	if object == nil || object.GetAPIVersion() != apiVersion || object.GetKind() != kind || len(validation.IsDNS1123Subdomain(object.GetName())) != 0 {
		return nil, fmt.Errorf("expected a named %s %s", apiVersion, kind)
	}
	spec, ok, err := unstructured.NestedMap(object.Object, "spec")
	if err != nil || !ok {
		return nil, fmt.Errorf("%s requires spec", kind)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": object.GetName()}, "spec": spec,
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
	if err := (&CatalogReconciler{reg: reg}).validateRequiresDeclaration(org, &entry); err != nil {
		return out, err
	}
	for _, requirement := range entry.Spec.Requires {
		if requirement.Provider != "" {
			dependency, ok := reg.GetForOrg(org, requirement.Provider)
			if !ok || !containsString(dependency.APIGroups, requirement.Group) {
				return out, fmt.Errorf("required provider must be registered and serve its declared group")
			}
			continue
		}
		// A missing provider name must not turn a foreign provider or control
		// plane API into an automatically accepted platform-builtin claim.
		for _, resource := range requirement.Resources {
			allowed := requirement.Group == "" && (resource.Name == "secrets" || resource.Name == "configmaps")
			allowed = allowed || requirement.Group == "authorization.k8s.io" && resource.Name == "subjectaccessreviews"
			if !allowed {
				return out, fmt.Errorf("requirement %s/%s is not an allowed builtin; name its provider", requirement.Group, resource.Name)
			}
		}
	}
	out.APIExport, err = sanitizeDeclaration(request.APIExport, "apis.kcp.io/v1alpha2", "APIExport")
	if err != nil {
		return out, err
	}
	if out.APIExport.GetName() != entry.Spec.Export.Name {
		return out, fmt.Errorf("APIExport must match CatalogEntry spec.export.name")
	}
	if len(request.Schemas) == 0 || len(request.Schemas) > 64 || len(request.PrivateCRDs) > OrgProviderPrivateResourceLimit {
		return out, fmt.Errorf("invalid schema count")
	}
	schemas := map[string]*unstructured.Unstructured{}
	for _, raw := range request.Schemas {
		s, err := sanitizeDeclaration(raw, "apis.kcp.io/v1alpha1", "APIResourceSchema")
		if err != nil {
			return out, err
		}
		group, _, _ := unstructured.NestedString(s.Object, "spec", "group")
		resource, _, _ := unstructured.NestedString(s.Object, "spec", "names", "plural")
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: group, Resource: resource}); err != nil {
			return out, err
		}
		if _, ok := schemas[s.GetName()]; ok {
			return out, fmt.Errorf("duplicate schema %s", s.GetName())
		}
		if err := rejectConversion(s); err != nil {
			return out, err
		}
		schemas[s.GetName()] = s
		out.Schemas = append(out.Schemas, s)
	}
	resources, found, err := unstructured.NestedSlice(out.APIExport.Object, "spec", "resources")
	if err != nil || !found || len(resources) == 0 || len(resources) > 1024 {
		return out, fmt.Errorf("APIExport requires resources")
	}
	declared := map[string]bool{}
	for _, r := range entry.Spec.Export.Resources {
		group := strings.Split(r.APIVersion, "/")[0]
		for _, v := range r.Verbs {
			declared[group+"/"+r.Name+"/"+v.Name] = true
		}
		for _, a := range r.Actions {
			declared[group+"/"+r.Name+"/"+a.Name] = true
		}
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
		key := group + "/" + resource
		if seen[key] {
			return out, fmt.Errorf("duplicate resource coordinate")
		}
		seen[key] = true
		s := schemas[schemaName]
		if s == nil {
			return out, fmt.Errorf("resource requires a supplied schema")
		}
		schemaGroup, _, _ := unstructured.NestedString(s.Object, "spec", "group")
		if schemaGroup != group {
			return out, fmt.Errorf("resource and schema groups differ")
		}
		storage, ok := item["storage"].(map[string]any)
		if !ok {
			return out, fmt.Errorf("resource requires storage")
		}
		clean := map[string]any{"group": group, "name": resource, "schema": schemaName}
		if len(parts) == 2 {
			if !declared[key] || parts[1] == "status" || parts[1] == "scale" || len(validation.IsDNS1123Label(parts[1])) != 0 {
				return out, fmt.Errorf("undeclared custom subresource")
			}
			// Ignore all supplied endpoint references. Only the hub may choose
			// what shared infrastructure dials for this provider.
			clean["storage"] = map[string]any{"virtual": map[string]any{"reference": map[string]any{
				"apiGroup": "dataplane.railgrid.ai", "kind": "DataPlaneEndpointSlice", "name": out.APIExport.GetName(),
			}}}
		} else {
			plural, _, _ := unstructured.NestedString(s.Object, "spec", "names", "plural")
			if plural != resource {
				return out, fmt.Errorf("ordinary resource and schema names differ")
			}
			if _, ok := storage["crd"].(map[string]any); !ok || len(storage) != 1 {
				return out, fmt.Errorf("ordinary BYO resources require CRD storage")
			}
			clean["storage"] = map[string]any{"crd": map[string]any{}}
		}
		cleanResources = append(cleanResources, clean)
	}
	for key := range declared {
		if !seen[key] {
			return out, fmt.Errorf("declared subresource missing from APIExport")
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
	// Reconstruct the whole export spec, so hidden claims, identity Secrets,
	// alternate storage mechanisms and policy fields cannot escape admission.
	out.APIExport.Object["spec"] = map[string]any{"resources": cleanResources, "permissionClaims": claims}
	if err := sdkinstall.ValidateClaimScopes(out.APIExport); err != nil {
		return out, err
	}
	privateSeen := map[string]bool{}
	for _, raw := range request.PrivateCRDs {
		crd, err := sanitizeDeclaration(raw, "apiextensions.k8s.io/v1", "CustomResourceDefinition")
		if err != nil {
			return out, err
		}
		group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
		resource, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "plural")
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: group, Resource: resource}); err != nil {
			return out, err
		}
		if crd.GetName() != resource+"."+group || privateSeen[crd.GetName()] {
			return out, fmt.Errorf("invalid or duplicate private CRD name")
		}
		if err := rejectConversion(crd); err != nil {
			return out, err
		}
		privateSeen[crd.GetName()] = true
		out.PrivateCRDs = append(out.PrivateCRDs, crd)
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
	for _, s := range request.Schemas {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			_, err := client.Resource(orgSchemaGVR).Create(ctx, s, metav1.CreateOptions{})
			return err
		})
		if apierrors.IsAlreadyExists(err) {
			existing, getErr := client.Resource(orgSchemaGVR).Get(ctx, s.GetName(), metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			// Schemas are immutable. Accept server-added defaults, but never
			// silently use different existing contents than those just validated.
			if !apiequality.Semantic.DeepDerivative(s.Object["spec"], existing.Object["spec"]) {
				return fmt.Errorf("schema %s already exists with different contents; use a new versioned name", s.GetName())
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
	if sdkinstall.ExportDeclaresSubresources(request.APIExport) {
		if err := sdkinstall.ApplyDataPlaneEndpointCRD(ctx, client); err != nil {
			return err
		}
	}
	// Replace rather than merge: an old endpoint reference must not survive a
	// later declaration which removed it.
	if err := applyOrgObject(ctx, client, orgExportGVR, request.APIExport); err != nil {
		return err
	}
	// The export quota admits only one name. Publish its endpoint only after
	// that admission succeeds, so failed installs cannot accumulate arbitrary
	// endpoint objects by changing the export name on each retry.
	if sdkinstall.ExportDeclaresSubresources(request.APIExport) {
		if err := sdkinstall.EnsureDataPlaneEndpointSlice(ctx, client, request.APIExport.GetName(), relayURL); err != nil {
			return err
		}
	}
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, client, request.APIExport.GetName(), request.APIExport.GetName(), identity.WorkspacePath); err != nil {
		return err
	}
	if err := provisioner.EnsureOrgProviderPrivateResourceRBAC(ctx, identity.WorkspacePath, privateResources); err != nil {
		return err
	}
	return applyOrgObject(ctx, client, orgCatalogGVR, request.CatalogEntry)
}

func applyOrgObject(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, desired *unstructured.Unstructured) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return applyOrgObjectOnce(ctx, client, gvr, desired)
	})
}

func applyOrgObjectOnce(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, desired *unstructured.Unstructured) error {
	existing, err := client.Resource(gvr).Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Resource(gvr).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	next := desired.DeepCopy()
	next.SetResourceVersion(existing.GetResourceVersion())
	_, err = client.Resource(gvr).Update(ctx, next, metav1.UpdateOptions{})
	return err
}
