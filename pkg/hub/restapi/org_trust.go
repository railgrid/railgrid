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
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/railgrid/railgrid/pkg/hub/internal/hijackio"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/sharedstore"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const (
	orgTrustModel               = "organization-v1"
	orgTrustOpaqueSecretType    = "Opaque"
	orgTrustStreamCheckInterval = 5 * time.Second
)

// OrgTrustStore is the hub-only durable approval store. Implementations must
// provide cross-replica reads and writes; the hub wires the controllers
// workspace sharedstore collection in production.
type OrgTrustStore interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte, time.Time) error
	Delete(context.Context, string) error
	CreateIfAbsent(context.Context, string, []byte, time.Time) (bool, error)
	Take(context.Context, string) ([]byte, error)
}

// OrgTrustApproval is the durable administrative approval for one provider
// source in one organization. Revision changes on each approval, and removal
// invalidates existing opaque backend credentials on their next authorization
// check.
type OrgTrustApproval struct {
	Model         string    `json:"model"`
	OrgUUID       string    `json:"orgUUID"`
	Provider      string    `json:"provider"`
	SourceCluster string    `json:"sourceCluster"`
	Revision      string    `json:"revision"`
	ApprovedAt    time.Time `json:"approvedAt"`
	ApprovedBy    string    `json:"approvedBy"`
}

// OrgProviderTrustView is the stable portal response. Unapproved providers
// expose neither a revision nor a source cluster.
type OrgProviderTrustView struct {
	Approved bool   `json:"approved"`
	Model    string `json:"model,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type setOrgProviderTrustRequest struct {
	TrustModel string `json:"trustModel"`
}

// CurrentOrgTrust reads the live approval and verifies that it is still bound
// to the provider source cluster named by the signed backend identity.
func (m *Manager) CurrentOrgTrust(ctx context.Context, claims serviceaccounts.OrgBackendClaims) (*OrgTrustApproval, bool, error) {
	if m == nil || m.orgTrustStore == nil || m.providers == nil || m.orgProviders == nil {
		return nil, false, fmt.Errorf("%w: organization provider trust is not configured", errOrgBackendStateUnavailable)
	}
	if claims.OrgUUID == "" || claims.Provider == "" || claims.ProviderCluster == "" {
		return nil, false, errOrgBackendIdentityUnavailable
	}
	if claims.ExpiresAt != 0 && time.Now().Unix() >= claims.ExpiresAt {
		return nil, false, errors.New("organization provider identity expired")
	}
	current, err := m.currentOrgBackendClaims(ctx, claims.OrgUUID, claims.WorkspaceUUID, claims.User, claims.Provider)
	if err != nil {
		return nil, false, fmt.Errorf("rechecking organization provider identity: %w", err)
	}
	if !sameOrgBackendClaims(current, claims) {
		return nil, false, errors.New("organization provider identity is no longer current")
	}
	approval, ok, err := m.readOrgTrust(ctx, claims.OrgUUID, claims.Provider, current.ProviderCluster)
	if err != nil {
		return nil, false, fmt.Errorf("%w: reading organization provider approval: %w", errOrgBackendStateUnavailable, err)
	}
	if !ok {
		return nil, ok, err
	}
	if claims.TrustRevision != "" && claims.TrustRevision != approval.Revision {
		return nil, false, errors.New("organization provider approval was replaced")
	}
	return &approval, true, nil
}

// ValidateOrgTrustClaims re-resolves the complete signed identity tuple and
// current owner-org approval. Signed coordinates are snapshots, never current
// authority.
func (m *Manager) ValidateOrgTrustClaims(ctx context.Context, claims serviceaccounts.OrgBackendClaims) error {
	_, approved, err := m.CurrentOrgTrust(ctx, claims)
	if err != nil {
		return err
	}
	if !approved {
		return errors.New("organization provider trust is not approved")
	}
	return nil
}

// orgTrustRequestMarker records the one fresh authorization performed before a
// opaque backend request is dispatched. It is request-local and private to
// this package; a marker is reusable only by the same Manager and exact claims.
// The origin gateway remains responsible for periodic stream revalidation.
type orgTrustRequestMarker struct {
	mu         sync.Mutex
	manager    *Manager
	claims     serviceaccounts.OrgBackendClaims
	route      *orgProviderRoute
	method     string
	outerGuard bool
	dispatched bool
}

type orgTrustRequestMarkerKey struct{}

// AuthorizeOrgTrustRequest performs fresh source, membership, binding, and
// approval checks once and places a private, request-local result in the
// context. DispatchOrgTrust consumes it only for an exact claims match.
func (m *Manager) AuthorizeOrgTrustRequest(ctx context.Context, claims serviceaccounts.OrgBackendClaims) (context.Context, error) {
	return m.authorizeOrgTrustRequest(ctx, claims, true)
}

func (m *Manager) authorizeOrgTrustRequest(ctx context.Context, claims serviceaccounts.OrgBackendClaims, outerGuard bool) (context.Context, error) {
	if claims.TrustRevision == "" {
		return nil, errors.New("organization provider trust revision is required")
	}
	authorizeCtx, cancel := context.WithTimeout(ctx, orgProviderAuthorizationTimeout)
	defer cancel()
	if err := m.ValidateOrgTrustClaims(authorizeCtx, claims); err != nil {
		return nil, err
	}
	marker := &orgTrustRequestMarker{manager: m, claims: claims, outerGuard: outerGuard}
	return context.WithValue(ctx, orgTrustRequestMarkerKey{}, marker), nil
}

// ValidateOrgTrustRequest refreshes live caller/source/approval state and, once
// dispatch has bound a route to this context, refreshes that route's binding
// and provider contract too. The origin gateway calls this at its stream-check
// cadence; initial redemption contexts have no route marker and validate only
// the session's principal/source/approval.
func (m *Manager) ValidateOrgTrustRequest(ctx context.Context, claims serviceaccounts.OrgBackendClaims) error {
	authorizeCtx, cancel := context.WithTimeout(ctx, orgProviderAuthorizationTimeout)
	defer cancel()
	if err := m.ValidateOrgTrustClaims(authorizeCtx, claims); err != nil {
		return err
	}
	marker := orgTrustMarkerFromContext(ctx)
	if marker == nil {
		return nil
	}
	marker.mu.Lock()
	defer marker.mu.Unlock()
	if marker.manager != m || !sameOrgTrustRequestClaims(marker.claims, claims) {
		return errors.New("organization provider request marker does not match its scope")
	}
	if marker.route != nil && !orgTrustRouteAllowed(authorizeCtx, *marker.route, marker.method, claims, m) {
		return errors.New("organization provider request route is no longer authorized")
	}
	return nil
}

func (m *Manager) bindOrgTrustRequestRoute(marker *orgTrustRequestMarker, claims serviceaccounts.OrgBackendClaims, route orgProviderRoute, method string) error {
	marker.mu.Lock()
	defer marker.mu.Unlock()
	if marker.manager != m || !sameOrgTrustRequestClaims(marker.claims, claims) || marker.dispatched {
		return errors.New("organization provider request marker is invalid or already dispatched")
	}
	marker.route = &route
	marker.method = method
	marker.dispatched = true
	return nil
}

func orgTrustMarkerFromContext(ctx context.Context) *orgTrustRequestMarker {
	if ctx == nil {
		return nil
	}
	marker, _ := ctx.Value(orgTrustRequestMarkerKey{}).(*orgTrustRequestMarker)
	return marker
}

func sameOrgTrustRequestClaims(a, b serviceaccounts.OrgBackendClaims) bool {
	return sameOrgBackendClaims(a, b) && a.TrustRevision == b.TrustRevision &&
		a.IssuedAt == b.IssuedAt && a.ExpiresAt == b.ExpiresAt
}

func sameOrgBackendClaims(a, b serviceaccounts.OrgBackendClaims) bool {
	return a.User == b.User && a.UserUID == b.UserUID && a.OrgUUID == b.OrgUUID && a.WorkspaceUUID == b.WorkspaceUUID &&
		a.ClusterID == b.ClusterID && a.Provider == b.Provider && a.ProviderCluster == b.ProviderCluster &&
		a.ExportPath == b.ExportPath && a.ExportName == b.ExportName && a.BindingUID == b.BindingUID
}

func (m *Manager) currentOrgProviderSource(ctx context.Context, orgUUID, providerName string) (providers.Provider, *kcp.OrgProviderWorkspace, error) {
	provider, found := m.providers.GetForOrg(orgUUID, providerName)
	if !found || provider.OrgUUID != orgUUID || provider.Name != providerName || provider.APIExportPath == "" || provider.APIExportName == "" {
		return providers.Provider{}, nil, errors.New("organization provider is unavailable")
	}
	workspace, err := m.orgProviders.GetOrgProviderWorkspace(ctx, orgUUID, providerName)
	if err != nil || workspace == nil || workspace.Name != providerName || workspace.Cluster == "" || workspace.Terminating ||
		workspace.Phase != "Ready" || workspace.BootstrapPolicy != providers.OrgProviderBootstrapPolicy ||
		provider.WorkspaceCluster != workspace.Cluster || provider.CatalogEntryCluster != workspace.Cluster {
		return providers.Provider{}, nil, errors.New("organization provider source is unavailable")
	}
	return provider, workspace, nil
}

func (m *Manager) readOrgTrust(ctx context.Context, orgUUID, providerName, sourceCluster string) (OrgTrustApproval, bool, error) {
	data, err := m.orgTrustStore.Get(ctx, orgTrustKey(orgUUID, providerName))
	if errors.Is(err, sharedstore.ErrNotFound) {
		return OrgTrustApproval{}, false, nil
	}
	if err != nil {
		return OrgTrustApproval{}, false, err
	}
	var approval OrgTrustApproval
	if err := json.Unmarshal(data, &approval); err != nil {
		return OrgTrustApproval{}, false, fmt.Errorf("decoding organization provider trust: %w", err)
	}
	if approval.Model != orgTrustModel || approval.OrgUUID != orgUUID || approval.Provider != providerName ||
		approval.SourceCluster == "" || approval.SourceCluster != sourceCluster || approval.Revision == "" || approval.ApprovedBy == "" {
		return OrgTrustApproval{}, false, nil
	}
	return approval, true, nil
}

func orgTrustKey(orgUUID, providerName string) string {
	return "org-provider-trust:" + orgUUID + ":" + providerName
}

func (h *Handler) getOrgProviderTrust(w http.ResponseWriter, r *http.Request) {
	tc, provider, workspace, ok := h.requireOrgTrustProvider(w, r, false)
	if !ok {
		return
	}
	approval, approved, err := h.mgr.readOrgTrust(r.Context(), tc.OrgUUID, provider.Name, workspace.Cluster)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider trust is unavailable")
		return
	}
	view := OrgProviderTrustView{Approved: approved}
	if approved {
		view.Model, view.Revision = approval.Model, approval.Revision
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) setOrgProviderTrust(w http.ResponseWriter, r *http.Request) {
	tc, provider, workspace, ok := h.requireOrgTrustProvider(w, r, true)
	if !ok {
		return
	}
	var request setOrgProviderTrustRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid trust request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "trust request must contain one JSON object")
		return
	}
	if request.TrustModel != orgTrustModel {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "trustModel must be organization-v1")
		return
	}
	var revisionBytes [24]byte
	if _, err := rand.Read(revisionBytes[:]); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating organization provider trust revision failed")
		return
	}
	approval := OrgTrustApproval{
		Model: orgTrustModel, OrgUUID: tc.OrgUUID, Provider: provider.Name,
		SourceCluster: workspace.Cluster, Revision: hex.EncodeToString(revisionBytes[:]),
		ApprovedAt: time.Now().UTC(), ApprovedBy: tc.User,
	}
	data, err := json.Marshal(approval)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "encoding organization provider trust failed")
		return
	}
	// The sharedstore requires an expiry. Year 9999 is its representable
	// effectively durable horizon; the explicit DELETE remains the revocation.
	const durableTrustExpiry = "9999-12-31T23:59:59Z"
	expiresAt, _ := time.Parse(time.RFC3339, durableTrustExpiry)
	key := orgTrustKey(tc.OrgUUID, provider.Name)
	created, err := h.mgr.orgTrustStore.CreateIfAbsent(r.Context(), key, data, expiresAt)
	if err == nil && !created {
		err = h.mgr.orgTrustStore.Put(r.Context(), key, data, expiresAt)
	}
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "saving organization provider trust failed")
		return
	}
	writeJSON(w, http.StatusOK, OrgProviderTrustView{Approved: true, Model: approval.Model, Revision: approval.Revision})
}

func (h *Handler) deleteOrgProviderTrust(w http.ResponseWriter, r *http.Request) {
	tc, provider, _, ok := h.requireOrgTrustProvider(w, r, true)
	if !ok {
		return
	}
	if err := h.mgr.orgTrustStore.Delete(r.Context(), orgTrustKey(tc.OrgUUID, provider.Name)); err != nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "revoking organization provider trust failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireOrgTrustProvider(w http.ResponseWriter, r *http.Request, admin bool) (tenant.TenantContext, providers.Provider, *kcp.OrgProviderWorkspace, bool) {
	tc, ok := h.requireTenantContext(w, r, false, false)
	if !ok {
		return tenant.TenantContext{}, providers.Provider{}, nil, false
	}
	if h.mgr.orgTrustStore == nil || h.mgr.orgProviders == nil || h.mgr.providers == nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider trust is unavailable")
		return tenant.TenantContext{}, providers.Provider{}, nil, false
	}
	if admin && !h.requireOrgAdminMembership(w, r, tc, "organization provider trust requires organization admin") {
		return tenant.TenantContext{}, providers.Provider{}, nil, false
	}
	name := mux.Vars(r)["name"]
	provider, workspace, err := h.mgr.currentOrgProviderSource(r.Context(), tc.OrgUUID, name)
	if err != nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "organization provider is unavailable")
		return tenant.TenantContext{}, providers.Provider{}, nil, false
	}
	return tc, provider, workspace, true
}

// DispatchOrgTrust is the KCP API hop for an organization-trusted provider.
// It accepts a request-local authorization marker from the origin gateway, or
// performs fresh authorization itself for the backend-token path. It fences
// the exact workspace cluster and retains native RBAC by using the shared
// dispatcher as the live human.
func (m *Manager) DispatchOrgTrust(w http.ResponseWriter, r *http.Request, claims serviceaccounts.OrgBackendClaims) {
	if m == nil || m.orgBackendDispatcher == nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider dispatch is unavailable")
		return
	}
	if r == nil || r.URL == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "provider API method is not supported")
		return
	}
	requestCtx := r.Context()
	marker := orgTrustMarkerFromContext(requestCtx)
	if marker != nil {
		marker.mu.Lock()
		markerMatches := marker.manager == m && sameOrgTrustRequestClaims(marker.claims, claims) && !marker.dispatched
		marker.mu.Unlock()
		if !markerMatches {
			writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider request authorization is invalid")
			return
		}
	}
	authorizeCtx, cancelAuthorize := context.WithTimeout(requestCtx, orgProviderAuthorizationTimeout)
	defer cancelAuthorize()
	if marker == nil {
		if err := m.ValidateOrgTrustClaims(authorizeCtx, claims); err != nil {
			writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider trust is no longer authorized")
			return
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	route, err := parseOrgProviderRoute(r, path, claims.ClusterID)
	if err != nil || !orgTrustRouteAllowed(authorizeCtx, route, r.Method, claims, m) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider API route is not allowed")
		return
	}
	if marker == nil {
		marker = &orgTrustRequestMarker{manager: m, claims: claims}
		requestCtx = context.WithValue(requestCtx, orgTrustRequestMarkerKey{}, marker)
	}
	if err := m.bindOrgTrustRequestRoute(marker, claims, route, r.Method); err != nil {
		writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider request authorization is invalid")
		return
	}
	marker.mu.Lock()
	outerGuard := marker.outerGuard
	marker.mu.Unlock()
	secretRequest := route.group == "" && route.resource == "secrets"
	if secretRequest {
		if r.Method == http.MethodPatch || r.Method == http.MethodGet && route.name == "" ||
			(r.Method == http.MethodPost && route.name != "") ||
			((r.Method == http.MethodPut || r.Method == http.MethodDelete) && route.name == "") {
			writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider Secret route is not allowed")
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if r.ContentLength > 1<<20 {
				writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "Secret request exceeds 1 MiB")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
	}
	ctx, cancel := context.WithCancel(requestCtx)
	defer cancel()
	dispatch := r.Clone(ctx)
	dispatch.Header = r.Header.Clone()
	stripOrgBackendIdentityHeaders(dispatch.Header)
	dispatch.Host = ""
	var guard *orgTrustRefreshGuard
	guard = &orgTrustRefreshGuard{
		ctx: ctx,
		check: func() error {
			// Hijack replaces ctx with a stream-scoped context that survives the
			// HTTP handler return. Read it while checkDue holds guard.mu.
			return m.ValidateOrgTrustRequest(guard.ctx, claims)
		},
		now:       time.Now,
		nextCheck: time.Now().Add(orgTrustStreamCheckInterval),
	}
	if !outerGuard && dispatch.Body != nil && dispatch.Body != http.NoBody {
		dispatch.Body = &orgTrustCheckedReadCloser{ReadCloser: dispatch.Body, guard: guard}
	}
	if secretRequest && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
		body, name, namespace, ok := readOrgTrustOpaqueSecret(dispatch.Body, route, r.Method)
		if !ok {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "Secret writes must contain a JSON Opaque Secret for this route")
			return
		}
		dispatch.Body = io.NopCloser(bytes.NewReader(body))
		dispatch.ContentLength = int64(len(body))
		_ = name
		_ = namespace
	}
	if secretRequest && r.Method == http.MethodGet {
		dispatch.Header.Set("Accept", "application/json")
		dispatch.Header.Set("Accept-Encoding", "identity")
		buffered := &orgTrustSecretBuffer{header: make(http.Header)}
		m.orgBackendDispatcher(buffered, dispatch, claims)
		if buffered.tooLarge {
			writeStatus(w, http.StatusBadGateway, "BadGateway", "Secret response exceeds 1 MiB")
			return
		}
		if buffered.status == 0 {
			buffered.status = http.StatusOK
		}
		if !outerGuard && guard.checkDue() != nil {
			writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider trust was revoked")
			return
		}
		if buffered.status >= 200 && buffered.status < 300 && !orgTrustOpaqueSecretResponse(buffered.header, buffered.body.Bytes(), route) {
			writeStatus(w, http.StatusForbidden, "Forbidden", "only Opaque Secrets are available through organization provider trust")
			return
		}
		for key, values := range buffered.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		status := buffered.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write(buffered.body.Bytes())
		}
		return
	}
	if outerGuard {
		m.orgBackendDispatcher(w, dispatch, claims)
		return
	}
	gate := &orgTrustGatedWriter{target: w, header: make(http.Header), guard: guard, cancel: cancel}
	m.orgBackendDispatcher(gate, dispatch, claims)
	if err := gate.finish(); err != nil && !gate.committed {
		writeStatus(w, http.StatusForbidden, "Forbidden", "organization provider trust was revoked")
	}
}

func orgTrustRouteAllowed(ctx context.Context, route orgProviderRoute, method string, claims serviceaccounts.OrgBackendClaims, m *Manager) bool {
	// Authority and publication APIs remain hub-owned even when ordinary tenant
	// data access has been approved for this provider.
	if orgTrustProtectedGroup(route.group) {
		return false
	}
	if route.resource == "customresourcedefinitions" || route.resource == "tokenreviews" || route.resource == "subjectaccessreviews" ||
		route.resource == "serviceaccounts" || route.subresource == "token" || route.subresource == "impersonate" {
		return false
	}
	if route.group == "" && (route.resource == "serviceaccounts" || route.resource == "namespaces" && method != http.MethodGet && method != http.MethodHead) {
		return false
	}
	if route.resource == "resourcequotas" && method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if route.namespace == "kube-system" || route.namespace == "kube-public" || route.namespace == "kube-node-lease" || route.namespace == "admin" {
		return false
	}
	if route.group == "coordination.k8s.io" {
		// Leases are the one ordinary non-core Kubernetes API needed by tenant
		// workloads. Do not extend this exception to other coordination APIs or
		// custom subresources.
		return route.resource == "leases" && route.subresource == ""
	}
	if route.group == "" && route.resource == "secrets" && route.name == "" && (method == http.MethodGet || strings.EqualFold(route.subresource, "watch")) {
		// Listing/watching Secrets would include ServiceAccount-token Secrets.
		// Named Opaque Secret reads remain available under org approval.
		return false
	}
	if route.group == "" {
		return route.subresource == "" || route.subresource == "status" || route.subresource == "scale"
	}
	// Every non-core provider resource must be present in one of the consumer's
	// current APIBindings and resolve to the same APIExport in the registry. This
	// is a server-side proof that the resource belongs to this tenant's enabled
	// API surface, not a duplicate browser resource allowlist.
	bindings, err := m.bootstrapper.ListProviderAPIBindings(ctx, claims.OrgUUID, claims.WorkspaceUUID)
	if err != nil {
		return false
	}
	for providerName, binding := range bindings {
		if binding.Terminating || binding.Name != providerName || binding.ExportPath == "" || binding.ExportName == "" ||
			!orgProviderBoundResource(binding.BoundResources, route.group, route.resource) {
			continue
		}
		provider, found := (&Handler{mgr: m}).providerForBinding(claims.OrgUUID, binding)
		if !found || provider.Name != providerName || (provider.OrgUUID != "" && provider.OrgUUID != claims.OrgUUID) ||
			provider.APIExportPath != binding.ExportPath || provider.APIExportName != binding.ExportName ||
			!containsOrgProviderGroup(provider.APIGroups, route.group) {
			continue
		}
		if route.subresource == "" || route.subresource == "status" || route.subresource == "scale" {
			return true
		}
		if orgProviderCoordinateDeclared(provider, route.group, route.version, route.resource, route.subresource) {
			return true
		}
	}
	return false
}

func orgTrustProtectedGroup(group string) bool {
	if group != "" && (group == "k8s.io" || group == "kcp.io" || strings.HasSuffix(group, ".k8s.io") || strings.HasSuffix(group, ".kcp.io")) {
		return group != "coordination.k8s.io"
	}
	switch group {
	case "rbac.authorization.k8s.io", "authentication.k8s.io", "authorization.k8s.io",
		"apis.kcp.io", "core.kcp.io", "tenancy.kcp.io", "apiextensions.k8s.io", "apiregistration.k8s.io",
		"providers.railgrid.ai", "tenants.railgrid.ai", "tenancy.railgrid.ai", "admin.railgrid.ai", "dataplane.railgrid.ai":
		return true
	}
	return false
}

type orgTrustGatedWriter struct {
	target    http.ResponseWriter
	header    http.Header
	status    int
	committed bool
	failed    bool
	guard     *orgTrustRefreshGuard
	cancel    context.CancelFunc
}

func (w *orgTrustGatedWriter) Header() http.Header { return w.header }
func (w *orgTrustGatedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *orgTrustGatedWriter) Write(p []byte) (int, error) {
	if w.failed {
		return 0, errOrgProviderAuthorizationDenied
	}
	if err := w.guard.checkDue(); err != nil {
		w.fail()
		return 0, err
	}
	w.commit()
	return w.target.Write(p)
}
func (w *orgTrustGatedWriter) Flush() {
	if w.failed || w.guard.checkDue() != nil {
		w.fail()
		return
	}
	w.commit()
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (w *orgTrustGatedWriter) FlushError() error {
	if w.failed {
		return errOrgProviderAuthorizationDenied
	}
	if err := w.guard.checkDue(); err != nil {
		w.fail()
		return err
	}
	w.commit()
	if flusher, ok := w.target.(interface{ FlushError() error }); ok {
		return flusher.FlushError()
	}
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
func (w *orgTrustGatedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.failed {
		return nil, nil, errOrgProviderAuthorizationDenied
	}
	if err := w.guard.checkDue(); err != nil {
		w.fail()
		return nil, nil, err
	}
	hijacker, ok := w.target.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.committed = true
	// The request context is canceled when the HTTP handler returns, which is
	// immediately after a successful WebSocket hijack. Give the hijacked stream
	// its own context and make periodic authorization use that same live context.
	streamCtx, streamCancel := context.WithCancel(context.WithoutCancel(w.guard.ctx))
	w.guard.mu.Lock()
	w.guard.ctx = streamCtx
	w.guard.mu.Unlock()
	if err := w.guard.checkDue(); err != nil {
		streamCancel()
		_ = conn.Close()
		return nil, nil, err
	}
	wrappedConn, rebound, err := hijackio.Rebind(conn, rw, func(conn net.Conn) net.Conn {
		return &orgTrustGatedConn{Conn: conn, guard: w.guard, cancel: streamCancel, done: make(chan struct{})}
	})
	if err != nil {
		streamCancel()
		return nil, nil, err
	}
	gated, ok := wrappedConn.(*orgTrustGatedConn)
	if !ok {
		streamCancel()
		_ = wrappedConn.Close()
		return nil, nil, errors.New("organization trust hijack I/O wrapper returned an unexpected connection")
	}
	gated.startMonitor()
	return gated, rebound, nil
}
func (w *orgTrustGatedWriter) Unwrap() http.ResponseWriter { return w.target }
func (w *orgTrustGatedWriter) commit() {
	if w.committed {
		return
	}
	for key, values := range w.header {
		for _, value := range values {
			w.target.Header().Add(key, value)
		}
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.target.WriteHeader(status)
	w.committed = true
}
func (w *orgTrustGatedWriter) fail() {
	w.failed = true
	if w.cancel != nil {
		w.cancel()
	}
}
func (w *orgTrustGatedWriter) finish() error {
	if w.failed {
		return errOrgProviderAuthorizationDenied
	}
	if err := w.guard.checkDue(); err != nil {
		w.fail()
		return err
	}
	w.commit()
	return nil
}

var _ http.Flusher = (*orgTrustGatedWriter)(nil)
var _ interface{ FlushError() error } = (*orgTrustGatedWriter)(nil)
var _ http.Hijacker = (*orgTrustGatedWriter)(nil)

func readOrgTrustOpaqueSecret(reader io.Reader, route orgProviderRoute, method string) ([]byte, string, string, bool) {
	if reader == nil {
		return nil, "", "", false
	}
	body, err := io.ReadAll(reader)
	if err != nil || len(body) == 0 || len(body) > 1<<20 {
		return nil, "", "", false
	}
	secret, ok := orgTrustSecretIdentity(body)
	if !ok || secret.apiVersion != "v1" || secret.kind != "Secret" || secret.typeName != orgTrustOpaqueSecretType || secret.name == "" {
		return nil, "", "", false
	}
	if method == http.MethodPut && secret.name != route.name || route.namespace != "" && secret.namespace != "" && secret.namespace != route.namespace {
		return nil, "", "", false
	}
	return body, secret.name, secret.namespace, true
}

func orgTrustOpaqueSecretResponse(header http.Header, body []byte, route orgProviderRoute) bool {
	contentType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return false
	}
	secret, ok := orgTrustSecretIdentity(body)
	if !ok || secret.apiVersion != "v1" || secret.kind != "Secret" || secret.typeName != orgTrustOpaqueSecretType || secret.name != route.name {
		return false
	}
	return route.namespace == "" || secret.namespace == "" || secret.namespace == route.namespace
}

type orgTrustSecret struct {
	apiVersion string
	kind       string
	typeName   string
	name       string
	namespace  string
}

func orgTrustSecretIdentity(body []byte) (orgTrustSecret, bool) {
	var secret struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Type       string `json:"type"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &secret) != nil {
		return orgTrustSecret{}, false
	}
	return orgTrustSecret{
		apiVersion: secret.APIVersion, kind: secret.Kind, typeName: secret.Type,
		name: secret.Metadata.Name, namespace: secret.Metadata.Namespace,
	}, secret.APIVersion != "" && secret.Kind != ""
}

type orgTrustSecretBuffer struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	tooLarge bool
}

func (w *orgTrustSecretBuffer) Header() http.Header { return w.header }
func (w *orgTrustSecretBuffer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *orgTrustSecretBuffer) Write(body []byte) (int, error) {
	if w.tooLarge || w.body.Len()+len(body) > 1<<20 {
		w.tooLarge = true
		return 0, errors.New("secret response exceeds 1 MiB")
	}
	return w.body.Write(body)
}

type orgTrustGatedConn struct {
	net.Conn
	guard     *orgTrustRefreshGuard
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}
}

func (c *orgTrustGatedConn) startMonitor() {
	if c.done == nil {
		c.done = make(chan struct{})
	}
	go func() {
		for {
			timer := time.NewTimer(c.guard.untilNextCheck())
			select {
			case <-c.done:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
				if err := c.guard.checkDue(); err != nil {
					_ = c.Close()
					return
				}
			}
		}
	}()
}

func (c *orgTrustGatedConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		if c.cancel != nil {
			c.cancel()
		}
		closeErr = c.Conn.Close()
	})
	return closeErr
}

func (c *orgTrustGatedConn) Read(p []byte) (int, error) {
	if err := c.guard.checkDue(); err != nil {
		_ = c.Close()
		return 0, err
	}
	n, err := c.Conn.Read(p)
	if authErr := c.guard.checkDue(); authErr != nil {
		_ = c.Close()
		return 0, authErr
	}
	return n, err
}
func (c *orgTrustGatedConn) Write(p []byte) (int, error) {
	if err := c.guard.checkDue(); err != nil {
		_ = c.Close()
		return 0, err
	}
	return c.Conn.Write(p)
}

type orgTrustRefreshGuard struct {
	mu        sync.Mutex
	ctx       context.Context
	check     func() error
	now       func() time.Time
	nextCheck time.Time
	failure   error
}

func (g *orgTrustRefreshGuard) checkDue() error {
	if g == nil {
		return errOrgProviderAuthorizationDenied
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure != nil {
		return g.failure
	}
	if g.ctx != nil {
		if err := g.ctx.Err(); err != nil {
			g.failure = errOrgProviderAuthorizationDenied
			return g.failure
		}
	}
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	if now().Before(g.nextCheck) {
		return nil
	}
	if g.check == nil {
		g.failure = errOrgProviderAuthorizationDenied
		return g.failure
	}
	g.nextCheck = now().Add(orgTrustStreamCheckInterval)
	if err := g.check(); err != nil {
		g.failure = err
	}
	return g.failure
}

func (g *orgTrustRefreshGuard) untilNextCheck() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure != nil {
		return 0
	}
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	delay := g.nextCheck.Sub(now())
	if delay < 0 {
		return 0
	}
	return delay
}

type orgTrustCheckedReadCloser struct {
	io.ReadCloser
	guard *orgTrustRefreshGuard
}

func (r *orgTrustCheckedReadCloser) Read(p []byte) (int, error) {
	if err := r.guard.checkDue(); err != nil {
		return 0, err
	}
	n, err := r.ReadCloser.Read(p)
	if postErr := r.guard.checkDue(); postErr != nil {
		return 0, postErr
	}
	return n, err
}
