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

package restapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const (
	providerSandboxAuthorizationTimeout = 2 * time.Second
	providerSandboxMaxRequestBytes      = 8 << 20
	providerSandboxMaxResponseBytes     = 32 << 20
)

type providerSandboxHandler struct{ mgr *Manager }

func newProviderSandboxHandler(mgr *Manager) http.Handler { return &providerSandboxHandler{mgr: mgr} }

type sandboxRoute struct {
	cluster     string
	group       string
	version     string
	apiPath     []string
	resource    string
	name        string
	subresource string
	namespace   string
}

func (h *providerSandboxHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mgr == nil || h.mgr.sandboxDispatcher == nil || h.mgr.sandboxAuthorizeCluster == nil || h.mgr.providers == nil || h.mgr.bootstrapper == nil || h.mgr.hubAccess == nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider sandbox authorization is unavailable")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "method is not supported by the provider sandbox")
		return
	}
	if r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "protocol upgrades are not supported")
		return
	}
	tc, ok := (&Handler{mgr: h.mgr}).requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	if tc.User == "" || tc.OrgUUID == "" || tc.WorkspaceUUID == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "authenticated tenant workspace is required")
		return
	}
	vars := mux.Vars(r)
	providerName := vars["name"]
	prov, found := h.mgr.providers.GetForOrg(tc.OrgUUID, providerName)
	if !found || prov.Name != providerName || prov.OrgUUID != tc.OrgUUID || prov.UIURL == nil || prov.APIExportPath == "" || prov.APIExportName == "" {
		writeStatus(w, http.StatusNotFound, "NotFound", "organization-owned provider is unavailable")
		return
	}
	lookupCtx, lookupCancel := context.WithTimeout(r.Context(), providerSandboxAuthorizationTimeout)
	clusterID, err := h.mgr.bootstrapper.GetChildWorkspaceClusterName(lookupCtx, tc.OrgUUID, tc.WorkspaceUUID)
	lookupCancel()
	if err != nil || clusterID == "" {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "workspace cluster could not be verified")
		return
	}
	route, err := parseProviderSandboxRoute(r, vars["rest"], clusterID)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if r.ContentLength > providerSandboxMaxRequestBytes {
		writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "provider request exceeds 8 MiB")
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(nil, r.Body, providerSandboxMaxRequestBytes)
	}
	secretSelector, allowed, unavailable := h.authorize(r.Context(), tc, prov, route, r.Method, r.URL.Query())
	if unavailable {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider sandbox authorization state is unavailable")
		return
	}
	if !allowed {
		writeStatus(w, http.StatusForbidden, "Forbidden", "provider sandbox capability is not currently authorized")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	dispatch := r.Clone(ctx)
	u := *r.URL
	u.Path, u.RawPath = "/"+vars["rest"], ""
	if !strings.HasPrefix(u.Path, "/clusters/") {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid provider sandbox KCP path")
		return
	}
	dispatch.URL = &u
	dispatch.RequestURI = u.RequestURI()
	dispatch.Body = r.Body
	if secretSelector != nil {
		dispatch.Header = r.Header.Clone()
		dispatch.Header.Set("Accept", "application/json")
		dispatch.Header.Set("Accept-Encoding", "identity")
		buffered := &sandboxBufferWriter{header: make(http.Header)}
		h.mgr.sandboxDispatcher.ServeHTTP(buffered, dispatch)
		if buffered.tooLarge {
			writeStatus(w, http.StatusBadGateway, "BadGateway", "provider sandbox response exceeds 32 MiB")
			return
		}
		checkedSelector, stillAllowed, unavailable := h.authorize(r.Context(), tc, prov, route, r.Method, r.URL.Query())
		if unavailable {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider sandbox authorization state is unavailable")
			return
		}
		if !stillAllowed || !sameSandboxSelector(secretSelector, checkedSelector) {
			writeStatus(w, http.StatusForbidden, "Forbidden", "provider sandbox capability is no longer authorized")
			return
		}
		if buffered.status == 0 {
			buffered.status = http.StatusOK
		}
		if buffered.status >= 200 && buffered.status < 300 && (!jsonSandboxResponse(buffered.header) || !matchesSandboxSecretSelector(buffered.body.Bytes(), checkedSelector)) {
			writeStatus(w, http.StatusForbidden, "Forbidden", "Secret response is outside the accepted selector")
			return
		}
		providers.CopyOrgProviderResponseHeaders(w.Header(), buffered.header)
		w.WriteHeader(buffered.status)
		if r.Method != http.MethodHead && buffered.body.Len() > 0 {
			_, _ = w.Write(buffered.body.Bytes())
		}
		return
	}
	gate := &sandboxGatedWriter{target: w, header: make(http.Header), authorize: func() error {
		_, allowed, unavailable := h.authorize(r.Context(), tc, prov, route, r.Method, r.URL.Query())
		if unavailable {
			return errSandboxAuthorizationUnavailable
		}
		if !allowed {
			return errSandboxAuthorizationDenied
		}
		return nil
	}, cancel: cancel}
	h.mgr.sandboxDispatcher.ServeHTTP(gate, dispatch)
	if err := gate.finish(); err != nil && !gate.committed {
		if err == errSandboxAuthorizationUnavailable {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider sandbox authorization state is unavailable")
		} else {
			writeStatus(w, http.StatusForbidden, "Forbidden", "provider sandbox capability is no longer authorized")
		}
	}
}

func parseProviderSandboxRoute(r *http.Request, rest, expectedCluster string) (sandboxRoute, error) {
	if rest == "" || strings.Contains(rest, "%") || strings.ContainsAny(rest, "\\") || strings.Contains(rest, "//") || r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") {
		return sandboxRoute{}, fmt.Errorf("provider sandbox path is not canonical")
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 5 || parts[0] != "clusters" || parts[1] != expectedCluster || !canonicalClusterID(expectedCluster) {
		return sandboxRoute{}, fmt.Errorf("provider sandbox must target the selected workspace cluster")
	}
	group, version, pathStart := "", "", 0
	switch parts[2] {
	case "api":
		version = parts[3]
		pathStart = 4
	case "apis":
		if len(parts) < 6 || parts[3] == "" {
			return sandboxRoute{}, fmt.Errorf("invalid Kubernetes API path")
		}
		group, version, pathStart = parts[3], parts[4], 5
	default:
		return sandboxRoute{}, fmt.Errorf("only Kubernetes API paths are allowed")
	}
	if version == "" || pathStart >= len(parts) {
		return sandboxRoute{}, fmt.Errorf("provider sandbox requires a Kubernetes API resource path")
	}
	for _, segment := range parts {
		if segment == "" || segment == "." || segment == ".." {
			return sandboxRoute{}, fmt.Errorf("provider sandbox path is not canonical")
		}
	}
	apiPath := append([]string(nil), parts[pathStart:]...)
	resourceIndex, namespace := 0, ""
	if apiPath[0] == "namespaces" {
		if len(apiPath) < 3 || apiPath[1] == "" {
			return sandboxRoute{}, fmt.Errorf("namespace resource path is incomplete")
		}
		namespace, resourceIndex = apiPath[1], 2
	}
	if resourceIndex >= len(apiPath) {
		return sandboxRoute{}, fmt.Errorf("provider sandbox requires a Kubernetes API resource path")
	}
	name, subresource := "", ""
	if len(apiPath) > resourceIndex+1 {
		name = apiPath[resourceIndex+1]
	}
	if len(apiPath) > resourceIndex+2 {
		subresource = apiPath[resourceIndex+2]
	}
	return sandboxRoute{cluster: parts[1], group: group, version: version, apiPath: apiPath, resource: apiPath[resourceIndex], name: name, subresource: subresource, namespace: namespace}, nil
}

func canonicalClusterID(value string) bool {
	return value != "" && !strings.HasPrefix(value, ":") && !strings.HasSuffix(value, ":") && !strings.Contains(value, "::") && !strings.ContainsAny(value, "/\\%?#")
}

func (h *providerSandboxHandler) authorize(ctx context.Context, tc tenant.TenantContext, prov providers.Provider, route sandboxRoute, method string, query url.Values) (map[string]string, bool, bool) {
	authCtx, cancel := context.WithTimeout(ctx, providerSandboxAuthorizationTimeout)
	defer cancel()
	clusterID, err := h.mgr.bootstrapper.GetChildWorkspaceClusterName(authCtx, tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil {
		return nil, false, true
	}
	if clusterID != route.cluster {
		return nil, false, false
	}
	if !h.mgr.sandboxAuthorizeCluster(authCtx, tc.User, clusterID) {
		return nil, false, false
	}
	currentProvider, found := h.mgr.providers.GetForOrg(tc.OrgUUID, prov.Name)
	if !found || currentProvider.Name != prov.Name || currentProvider.OrgUUID != tc.OrgUUID || currentProvider.APIExportPath != prov.APIExportPath || currentProvider.APIExportName != prov.APIExportName {
		return nil, false, false
	}
	bindings, err := h.mgr.bootstrapper.ListProviderAPIBindings(authCtx, tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil {
		return nil, false, true
	}
	providerBinding, ok := bindings[prov.Name]
	if !ok || providerBinding.Terminating || providerBinding.Name != prov.Name || providerBinding.ExportPath != prov.APIExportPath || providerBinding.ExportName != prov.APIExportName {
		return nil, false, false
	}
	if containsSandbox(currentProvider.APIGroups, route.group) && sandboxBoundResource(providerBinding.BoundResources, route.group, route.resource) {
		return nil, sandboxOwnAPIRouteAllowed(currentProvider, route, method, query), false
	}
	capability, isSubresource, ok := sandboxCapability(route, method, query)
	if !ok {
		return nil, false, false
	}
	for _, coordinate := range providersv1alpha1.RequiredCoordinates(currentProvider.Requires) {
		if coordinate.Group != route.group {
			continue
		}
		baseResource, requiredVerb, _ := strings.Cut(coordinate.Resource, "/")
		if baseResource != route.resource {
			continue
		}
		if requiredVerb != "" {
			if !isSubresource || route.name == "" || route.subresource != requiredVerb || method != http.MethodPost || len(route.apiPath) != sandboxNamespaceOffset(route)+3 || capability != "subresource" {
				continue
			}
		} else if isSubresource || capability == "subresource" || !sandboxRequiredVerb(coordinate, capability) {
			continue
		}
		if coordinate.Provider != "" {
			dependency, exists := h.mgr.providers.GetForOrg(tc.OrgUUID, coordinate.Provider)
			if !exists || (dependency.OrgUUID != "" && dependency.OrgUUID != tc.OrgUUID) {
				return nil, false, false
			}
			if !containsSandbox(dependency.APIGroups, coordinate.Group) {
				return nil, false, false
			}
			binding, exists := bindings[dependency.Name]
			if !exists || binding.Terminating || binding.Name != dependency.Name || binding.ExportPath != dependency.APIExportPath || binding.ExportName != dependency.APIExportName {
				return nil, false, false
			}
			if !sandboxBoundResource(binding.BoundResources, coordinate.Group, baseResource) && !sandboxBoundResource(binding.BoundResources, coordinate.Group, coordinate.Resource) {
				return nil, false, false
			}
			if requiredVerb != "" && !sandboxProviderCoordinateDeclared(dependency, route.group, route.version, baseResource, requiredVerb) {
				return nil, false, false
			}
			grant, err := h.mgr.hubAccess.GetUncached(authCtx, hubaccess.GrantKey{OrgUUID: tc.OrgUUID, WorkspaceUUID: tc.WorkspaceUUID, Provider: currentProvider.Name, ProviderOrgUUID: currentProvider.OrgUUID})
			if err != nil {
				return nil, false, true
			}
			composition := hubaccess.CompositionRequirement{Dependency: coordinate.Provider, Group: coordinate.Group, Resource: coordinate.Resource}
			if hubaccess.DecideComposition(grant, composition) != hubaccess.Accepted {
				return nil, false, false
			}
		}
		claim := sandboxAcceptedClaim(providerBinding.Claims.Accepted, coordinate.Group, coordinate.Resource)
		if claim == nil || providerBinding.Claims.Mismatch != "" || !sandboxClaimVerbAllows(claim.Verbs, capability) {
			continue
		}
		if route.group == "" && route.resource == "secrets" {
			if method != http.MethodGet || route.namespace != "default" || route.name == "" || route.subresource != "" || len(route.apiPath) != 4 || !sandboxWatchFalseOrAbsent(query) {
				continue
			}
			selector, ok := sandboxSecretSelector(coordinate.Selector, claim.Selector)
			if ok {
				return selector, true, false
			}
			continue
		}
		if !claim.Selector.MatchAll || len(claim.Selector.MatchLabels) != 0 || (coordinate.Selector != nil && len(coordinate.Selector.MatchLabels) != 0) {
			continue
		}
		if route.resource == "secrets" && route.group == "" {
			continue
		}
		if route.subresource == "status" || route.subresource == "scale" {
			continue
		}
		return nil, true, false
	}
	return nil, false, false
}

func sandboxCapability(route sandboxRoute, method string, query url.Values) (string, bool, bool) {
	watch := query["watch"]
	if len(watch) > 1 || (len(watch) == 1 && watch[0] != "true" && watch[0] != "false") {
		return "", false, false
	}
	baseLength := sandboxNamespaceOffset(route) + 1
	if method == http.MethodGet && len(watch) == 1 && watch[0] == "true" {
		if len(route.apiPath) != baseLength {
			return "", false, false
		}
		return "watch", false, true
	}
	if route.subresource != "" {
		return "subresource", true, method == http.MethodPost
	}
	collection, named := len(route.apiPath) == baseLength, len(route.apiPath) == baseLength+1
	if !collection && !named {
		return "", false, false
	}
	switch method {
	case http.MethodGet:
		if collection {
			return "list", false, true
		}
		return "get", false, true
	case http.MethodHead:
		return "", false, false
	case http.MethodPost:
		return "create", false, collection
	case http.MethodPut:
		return "update", false, named
	case http.MethodPatch:
		return "patch", false, named
	case http.MethodDelete:
		return "delete", false, named
	default:
		return "", false, false
	}
}

func sandboxOwnAPIRouteAllowed(prov providers.Provider, route sandboxRoute, method string, query url.Values) bool {
	if len(route.apiPath) == 0 || (method != http.MethodGet && method != http.MethodHead && method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete) {
		return false
	}
	watch := query["watch"]
	if len(watch) > 1 || (len(watch) == 1 && watch[0] != "true" && watch[0] != "false") {
		return false
	}
	if route.subresource == "" {
		baseLength := sandboxNamespaceOffset(route) + 1
		return len(route.apiPath) == baseLength || len(route.apiPath) == baseLength+1
	}
	if route.name == "" || len(route.apiPath) < sandboxNamespaceOffset(route)+3 {
		return false
	}
	if route.subresource == "status" || route.subresource == "scale" {
		return len(route.apiPath) == sandboxNamespaceOffset(route)+3
	}
	return sandboxProviderCoordinateDeclared(prov, route.group, route.version, route.resource, route.subresource)
}

func sandboxProviderCoordinateDeclared(prov providers.Provider, group, version, resource, verb string) bool {
	if prov.Export == nil {
		return false
	}
	for _, declared := range prov.Export.Coordinates() {
		gv := declared.GroupVersion()
		if declared.APIVersion == group+"/"+version && gv.Group == group && declared.Resource == resource && declared.Verb == verb {
			return true
		}
	}
	return false
}

func sandboxBoundResource(resources []kcp.BoundResource, group, resource string) bool {
	for _, bound := range resources {
		if bound.Group == group && bound.Resource == resource {
			return true
		}
	}
	return false
}

func sandboxNamespaceOffset(route sandboxRoute) int {
	if route.namespace != "" {
		return 2
	}
	return 0
}

func sandboxRequiredVerb(coordinate providersv1alpha1.RequiredCoordinate, verb string) bool {
	for _, required := range coordinate.Verbs {
		if string(required) == verb {
			return true
		}
	}
	return false
}

func sandboxAcceptedClaim(claims []kcp.AcceptedClaimScope, group, resource string) *kcp.AcceptedClaimScope {
	for i := range claims {
		if claims[i].Group == group && claims[i].Resource == resource {
			return &claims[i]
		}
	}
	return nil
}

func sandboxClaimVerbAllows(verbs []string, capability string) bool {
	for _, verb := range verbs {
		if verb == "*" || verb == capability || (capability == "subresource" && verb == "create") {
			return true
		}
	}
	return false
}

func containsSandbox(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sandboxWatchFalseOrAbsent(query url.Values) bool {
	values := query["watch"]
	return len(values) == 0 || (len(values) == 1 && values[0] == "false")
}

func sandboxSecretSelector(declared *providersv1alpha1.ProviderLabelSelector, accepted kcp.AcceptedClaimSelector) (map[string]string, bool) {
	if declared == nil || len(declared.MatchLabels) == 0 {
		return nil, false
	}
	labels := map[string]string{}
	if !accepted.MatchAll {
		if len(accepted.MatchLabels) == 0 {
			return nil, false
		}
		for key, value := range accepted.MatchLabels {
			labels[key] = value
		}
	}
	for key, value := range declared.MatchLabels {
		if prior, exists := labels[key]; exists && prior != value {
			return nil, false
		}
		labels[key] = value
	}
	return labels, len(labels) > 0
}

func sameSandboxSelector(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func jsonSandboxResponse(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")) && (header.Get("Content-Encoding") == "" || strings.EqualFold(header.Get("Content-Encoding"), "identity"))
}

func matchesSandboxSecretSelector(body []byte, selector map[string]string) bool {
	var object struct {
		Type     string `json:"type"`
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &object) != nil || len(object.Metadata.Labels) == 0 || object.Type == "kubernetes.io/service-account-token" {
		return false
	}
	for key, expected := range selector {
		if object.Metadata.Labels[key] != expected {
			return false
		}
	}
	return true
}

type sandboxBufferWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	tooLarge bool
}

func (w *sandboxBufferWriter) Header() http.Header { return w.header }
func (w *sandboxBufferWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *sandboxBufferWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(p) > providerSandboxMaxResponseBytes {
		w.tooLarge = true
		return 0, fmt.Errorf("provider sandbox response exceeds limit")
	}
	return w.body.Write(p)
}

var (
	errSandboxAuthorizationDenied      = fmt.Errorf("provider sandbox authorization denied")
	errSandboxAuthorizationUnavailable = fmt.Errorf("provider sandbox authorization unavailable")
)

type sandboxGatedWriter struct {
	target    http.ResponseWriter
	header    http.Header
	authorize func() error
	cancel    context.CancelFunc
	status    int
	written   int64
	committed bool
	failed    error
}

func (w *sandboxGatedWriter) Header() http.Header { return w.header }
func (w *sandboxGatedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *sandboxGatedWriter) Write(p []byte) (int, error) {
	if w.failed != nil {
		return 0, w.failed
	}
	if w.written+int64(len(p)) > providerSandboxMaxResponseBytes {
		w.fail(errSandboxAuthorizationDenied)
		return 0, fmt.Errorf("provider sandbox response exceeds limit")
	}
	if err := w.authorize(); err != nil {
		w.fail(err)
		return 0, err
	}
	w.commit()
	n, err := w.target.Write(p)
	w.written += int64(n)
	return n, err
}
func (w *sandboxGatedWriter) Flush() {
	if w.failed != nil {
		return
	}
	if err := w.authorize(); err != nil {
		w.fail(err)
		return
	}
	w.commit()
	if f, ok := w.target.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *sandboxGatedWriter) FlushError() error {
	if w.failed != nil {
		return w.failed
	}
	if err := w.authorize(); err != nil {
		w.fail(err)
		return err
	}
	w.commit()
	if f, ok := w.target.(interface{ FlushError() error }); ok {
		return f.FlushError()
	}
	if f, ok := w.target.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
func (w *sandboxGatedWriter) commit() {
	if w.committed {
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	providers.CopyOrgProviderResponseHeaders(w.target.Header(), w.header)
	w.target.WriteHeader(status)
	w.committed = true
}
func (w *sandboxGatedWriter) fail(err error) {
	if w.failed == nil {
		w.failed = err
		w.cancel()
	}
}
func (w *sandboxGatedWriter) finish() error {
	if w.failed != nil {
		return w.failed
	}
	if err := w.authorize(); err != nil {
		w.fail(err)
		return err
	}
	w.commit()
	return nil
}

var _ http.Flusher = (*sandboxGatedWriter)(nil)
var _ interface{ FlushError() error } = (*sandboxGatedWriter)(nil)
