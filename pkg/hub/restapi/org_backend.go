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
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

var errOrgBackendIdentityUnavailable = errors.New("organization provider identity is unavailable")

// IssueOrgBackendToken mints a short-lived opaque identity token for one
// organization-owned provider and one selected consumer workspace. It carries
// no KCP authority; the corresponding handler resolves it against live state
// on every request.
func (m *Manager) IssueOrgBackendToken(ctx context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error) {
	if m == nil || m.client == nil || m.bootstrapper == nil || m.providers == nil || m.orgProviders == nil || m.orgBackendKeys == nil {
		return "", time.Time{}, errOrgBackendIdentityUnavailable
	}
	if orgUUID == "" || wsUUID == "" || user.User == "" || provider.Name == "" || provider.OrgUUID != orgUUID {
		return "", time.Time{}, errOrgBackendIdentityUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, providerSandboxAuthorizationTimeout)
	defer cancel()
	claims, err := m.currentOrgBackendClaims(ctx, orgUUID, wsUUID, user.User, provider.Name)
	if err != nil {
		return "", time.Time{}, err
	}
	return serviceaccounts.MintOrgBackendToken(ctx, m.orgBackendKeys, claims, time.Now())
}

// VerifyOrgBackendCaller verifies the opaque token and its live user, tenant,
// provider, hosting workspace, and APIBinding coordinates. It intentionally
// does not authorize a particular resource; OrgBackendHandler applies the
// sandbox route policy before dispatch.
func (m *Manager) VerifyOrgBackendCaller(r *http.Request) (serviceaccounts.OrgBackendClaims, error) {
	if m == nil || m.orgBackendKeys == nil || r == nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	value := r.Header.Values("Authorization")
	if len(value) != 1 || !strings.HasPrefix(value[0], "Bearer ") {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), providerSandboxAuthorizationTimeout)
	defer cancel()
	claims, err := serviceaccounts.VerifyOrgBackendToken(ctx, m.orgBackendKeys, strings.TrimPrefix(value[0], "Bearer "), time.Now())
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	// Compare every signed snapshot coordinate with the freshly resolved values.
	current, err := m.currentOrgBackendClaims(ctx, claims.OrgUUID, claims.WorkspaceUUID, claims.User, claims.Provider)
	if err != nil || claims.ClusterID != current.ClusterID || claims.ProviderCluster != current.ProviderCluster ||
		claims.ExportPath != current.ExportPath || claims.ExportName != current.ExportName || claims.BindingUID != current.BindingUID || claims.UserUID != current.UserUID {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	return claims, nil
}

// currentOrgBackendClaims verifies that the identity tuple still belongs to a
// live user membership, enabled self-hosted provider, exact APIExport, and
// current APIBinding. No authorization is inferred from the signed snapshot.
func (m *Manager) currentOrgBackendClaims(ctx context.Context, orgUUID, wsUUID, userName, providerName string) (serviceaccounts.OrgBackendClaims, error) {
	if orgUUID == "" || wsUUID == "" || userName == "" || providerName == "" || m == nil || m.client == nil || m.bootstrapper == nil || m.providers == nil || m.orgProviders == nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	user, err := m.client.Users().Get(ctx, userName, metav1.GetOptions{})
	if err != nil || user.UID == "" || user.DeletionTimestamp != nil || user.Status.DeletionRequestedAt != nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	index, err := m.client.UserMembershipIndices().Get(ctx, userName, metav1.GetOptions{})
	if err != nil || !membershipIndexCovers(index, orgUUID, wsUUID) {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	clusterID, err := m.bootstrapper.GetChildWorkspaceClusterName(ctx, orgUUID, wsUUID)
	if err != nil || clusterID == "" {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	provider, found := m.providers.GetForOrg(orgUUID, providerName)
	if !found || provider.Name != providerName || provider.OrgUUID != orgUUID || provider.APIExportPath == "" || provider.APIExportName == "" || len(provider.APIGroups) == 0 {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	providerWorkspace, err := m.orgProviders.GetOrgProviderWorkspace(ctx, orgUUID, providerName)
	if err != nil || providerWorkspace == nil || providerWorkspace.Name != providerName || providerWorkspace.Cluster == "" || providerWorkspace.Phase != "Ready" || providerWorkspace.Terminating ||
		providerWorkspace.BootstrapPolicy != providers.OrgProviderBootstrapPolicy ||
		provider.CatalogEntryCluster == "" || provider.WorkspaceCluster == "" || provider.CatalogEntryCluster != providerWorkspace.Cluster || provider.WorkspaceCluster != providerWorkspace.Cluster {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	bindings, err := m.bootstrapper.ListProviderAPIBindings(ctx, orgUUID, wsUUID)
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	binding, ok := bindings[providerName]
	if !ok || binding.Name != providerName || binding.UID == "" || binding.Terminating || !binding.SelfHosted ||
		binding.ExportPath != provider.APIExportPath || binding.ExportName != provider.APIExportName || !bindingHasOwnProviderResource(provider, binding.BoundResources) {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	return serviceaccounts.OrgBackendClaims{
		User: userName, UserUID: string(user.UID), OrgUUID: orgUUID, WorkspaceUUID: wsUUID,
		ClusterID: clusterID, Provider: providerName, ProviderCluster: providerWorkspace.Cluster,
		ExportPath: provider.APIExportPath, ExportName: provider.APIExportName, BindingUID: binding.UID,
	}, nil
}

func membershipIndexCovers(index *tenancyv1alpha1.UserMembershipIndex, orgUUID, wsUUID string) bool {
	if index == nil {
		return false
	}
	orgAdmin := false
	for _, entry := range index.Spec.Entries {
		if entry.OrgUUID != orgUUID || entry.SoftDeletedAt != nil || !validMembershipRole(entry.Role) {
			continue
		}
		if entry.WorkspaceUUID == wsUUID {
			return true
		}
		if entry.WorkspaceUUID == "" && entry.Role == tenancyv1alpha1.MembershipRoleAdmin {
			orgAdmin = true
		}
	}
	return orgAdmin
}

func validMembershipRole(role string) bool {
	return role == tenancyv1alpha1.MembershipRoleAdmin || role == tenancyv1alpha1.MembershipRoleMember
}

func bindingHasOwnProviderResource(provider providers.Provider, resources []kcp.BoundResource) bool {
	for _, resource := range resources {
		if resource.Resource == "" {
			continue
		}
		for _, group := range provider.APIGroups {
			if resource.Group == group {
				return true
			}
		}
	}
	return false
}

// OrgBackendHandler mediates backend KCP-shaped requests through the current
// provider-consent policy. The injected dispatcher must perform the KCP call
// as the live human named by the verified token, never as the provider.
func (m *Manager) OrgBackendHandler() http.Handler { return &orgBackendHandler{mgr: m} }

type orgBackendHandler struct{ mgr *Manager }

func (h *orgBackendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mgr == nil || h.mgr.orgBackendKeys == nil || h.mgr.orgBackendDispatcher == nil || h.mgr.hubAccess == nil || h.mgr.sandboxAuthorizeCluster == nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider authorization is unavailable")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "method is not supported by the provider backend")
		return
	}
	if r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "protocol upgrades are not supported")
		return
	}
	claims, err := h.mgr.VerifyOrgBackendCaller(r)
	if err != nil {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "organization provider identity is invalid or expired")
		return
	}
	if r.ContentLength > providerSandboxMaxRequestBytes {
		writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "provider request exceeds 8 MiB")
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(nil, r.Body, providerSandboxMaxRequestBytes)
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	route, err := parseProviderSandboxRoute(r, path, claims.ClusterID)
	if err != nil || !orgBackendRouteAllowed(route, r.Method) {
		writeStatus(w, http.StatusForbidden, "Forbidden", "provider backend capability is not currently authorized")
		return
	}
	provider, found := h.mgr.providers.GetForOrg(claims.OrgUUID, claims.Provider)
	if !found {
		writeStatus(w, http.StatusForbidden, "Forbidden", "provider backend capability is not currently authorized")
		return
	}
	tc := tenant.TenantContext{User: claims.User, OrgUUID: claims.OrgUUID, WorkspaceUUID: claims.WorkspaceUUID}
	secretSelector, allowed, unavailable := (&providerSandboxHandler{mgr: h.mgr}).authorize(r.Context(), tc, provider, route, r.Method, r.URL.Query())
	if unavailable {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider authorization state is unavailable")
		return
	}
	if !allowed {
		writeStatus(w, http.StatusForbidden, "Forbidden", "provider backend capability is not currently authorized")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	dispatch := r.Clone(ctx)
	dispatch.Header = r.Header.Clone()
	stripOrgBackendIdentityHeaders(dispatch.Header)
	dispatch.Host = ""
	dispatch.Body = r.Body
	if secretSelector != nil {
		dispatch.Header.Set("Accept", "application/json")
		dispatch.Header.Set("Accept-Encoding", "identity")
		buffered := &sandboxBufferWriter{header: make(http.Header)}
		h.mgr.orgBackendDispatcher(buffered, dispatch, claims)
		if buffered.tooLarge {
			writeStatus(w, http.StatusBadGateway, "BadGateway", "provider response exceeds 32 MiB")
			return
		}
		_, identityErr := h.mgr.VerifyOrgBackendCaller(r)
		checkedSelector, stillAllowed, unavailable := (&providerSandboxHandler{mgr: h.mgr}).authorize(r.Context(), tc, provider, route, r.Method, r.URL.Query())
		if unavailable {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider authorization state is unavailable")
			return
		}
		if identityErr != nil || !stillAllowed || !sameSandboxSelector(secretSelector, checkedSelector) {
			writeStatus(w, http.StatusForbidden, "Forbidden", "provider capability is no longer authorized")
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
		if _, err := h.mgr.VerifyOrgBackendCaller(r); err != nil {
			return errSandboxAuthorizationDenied
		}
		_, ok, unavailable := (&providerSandboxHandler{mgr: h.mgr}).authorize(r.Context(), tc, provider, route, r.Method, r.URL.Query())
		if unavailable {
			return errSandboxAuthorizationUnavailable
		}
		if !ok {
			return errSandboxAuthorizationDenied
		}
		return nil
	}, cancel: cancel}
	h.mgr.orgBackendDispatcher(gate, dispatch, claims)
	if err := gate.finish(); err != nil && !gate.committed {
		if err == errSandboxAuthorizationUnavailable {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "provider authorization state is unavailable")
		} else {
			writeStatus(w, http.StatusForbidden, "Forbidden", "provider capability is no longer authorized")
		}
	}
}

func orgBackendRouteAllowed(route sandboxRoute, method string) bool {
	if route.group == "rbac.authorization.k8s.io" || route.group == "authentication.k8s.io" || route.group == "authorization.k8s.io" {
		return false
	}
	if route.group == "" && (route.resource == "serviceaccounts" || (route.resource == "secrets" && route.subresource != "")) {
		return false
	}
	if route.resource == "namespaces" && method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if route.resource == "resourcequotas" && method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if route.group == "apis.kcp.io" || (route.group == "core.kcp.io" && route.resource == "logicalclusters") {
		return false
	}
	if route.group == "apiextensions.k8s.io" && route.resource == "customresourcedefinitions" {
		return false
	}
	if route.group == "tenancy.kcp.io" {
		return false
	}
	if route.subresource == "impersonate" || route.subresource == "token" || route.resource == "tokenreviews" || route.resource == "subjectaccessreviews" {
		return false
	}
	return true
}

func stripOrgBackendIdentityHeaders(header http.Header) {
	for key := range header {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-railgrid-") || strings.HasPrefix(lower, "x-remote-") || strings.HasPrefix(lower, "impersonate-") {
			header.Del(key)
		}
	}
	header.Del("Authorization")
	header.Del("Cookie")
}
