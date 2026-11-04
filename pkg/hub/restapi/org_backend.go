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
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

var (
	errOrgBackendIdentityUnavailable = errors.New("organization provider identity is unavailable")
	errOrgBackendStateUnavailable    = errors.New("organization provider authorization state is unavailable")
)

func orgBackendLookupError(err error) error {
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %w", errOrgBackendIdentityUnavailable, err)
	}
	return fmt.Errorf("%w: %w", errOrgBackendStateUnavailable, err)
}

// IssueOrgBackendToken mints a short-lived opaque identity token for one
// organization-owned provider and one selected consumer workspace. It carries
// no KCP authority; the corresponding handler resolves it against live state
// on every request.
func (m *Manager) IssueOrgBackendToken(ctx context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, provider serviceaccounts.DelegatedProvider) (string, time.Time, error) {
	if m == nil || m.client == nil || m.bootstrapper == nil || m.providers == nil || m.orgProviders == nil || m.orgBackendKeys == nil || m.orgTrustStore == nil {
		return "", time.Time{}, errOrgBackendIdentityUnavailable
	}
	if orgUUID == "" || wsUUID == "" || user.User == "" || provider.Name == "" || provider.OrgUUID != orgUUID {
		return "", time.Time{}, errOrgBackendIdentityUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, orgProviderAuthorizationTimeout)
	defer cancel()
	claims, err := m.currentOrgBackendClaims(ctx, orgUUID, wsUUID, user.User, provider.Name)
	if err != nil {
		return "", time.Time{}, err
	}
	approval, approved, err := m.readOrgTrust(ctx, orgUUID, provider.Name, claims.ProviderCluster)
	if err != nil || !approved {
		return "", time.Time{}, errOrgBackendIdentityUnavailable
	}
	claims.TrustRevision = approval.Revision
	return serviceaccounts.MintOrgBackendToken(ctx, m.orgBackendKeys, claims, time.Now())
}

// VerifyOrgBackendCaller verifies the opaque token and its live user, tenant,
// provider, hosting workspace, and APIBinding coordinates. It intentionally
// does not authorize a particular resource; OrgBackendHandler applies the
// organization gateway policy before dispatch.
func (m *Manager) VerifyOrgBackendCaller(r *http.Request) (serviceaccounts.OrgBackendClaims, error) {
	claims, err := m.verifyOrgBackendCredential(r)
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), orgProviderAuthorizationTimeout)
	defer cancel()
	if err := m.ValidateOrgTrustClaims(ctx, claims); err != nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	return claims, nil
}

// verifyOrgBackendCredential validates only the opaque credential's signature,
// expiry, and required trust revision. OrgBackendHandler passes the result to
// DispatchOrgTrust, which performs the single fresh live authorization for the
// request. VerifyOrgBackendCaller remains the full live check used by other
// hub middleware.
func (m *Manager) verifyOrgBackendCredential(r *http.Request) (serviceaccounts.OrgBackendClaims, error) {
	if m == nil || m.orgBackendKeys == nil || r == nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	value := r.Header.Values("Authorization")
	if len(value) != 1 || !strings.HasPrefix(value[0], "Bearer ") {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	claims, err := serviceaccounts.VerifyOrgBackendToken(r.Context(), m.orgBackendKeys, strings.TrimPrefix(value[0], "Bearer "), time.Now())
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	// Approval is part of backend authority too. Tokens minted before the
	// org-gateway trust model must be reissued, never silently treated as broad.
	if claims.TrustRevision == "" {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	return claims, nil
}

// currentOrgBackendClaims verifies that the identity tuple still belongs to a
// live user membership, enabled self-hosted provider, exact APIExport, and
// current APIBinding. No authorization is inferred from the signed snapshot.
func (m *Manager) currentOrgBackendClaims(ctx context.Context, orgUUID, wsUUID, userName, providerName string) (serviceaccounts.OrgBackendClaims, error) {
	if orgUUID == "" || wsUUID == "" || userName == "" || providerName == "" {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	if m == nil || m.client == nil || m.bootstrapper == nil || m.providers == nil || m.orgProviders == nil {
		return serviceaccounts.OrgBackendClaims{}, fmt.Errorf("%w: live organization provider dependencies are not configured", errOrgBackendStateUnavailable)
	}
	user, err := m.client.Users().Get(ctx, userName, metav1.GetOptions{})
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, orgBackendLookupError(err)
	}
	if user == nil || user.UID == "" || user.DeletionTimestamp != nil || user.Status.DeletionRequestedAt != nil {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	index, err := m.client.UserMembershipIndices().Get(ctx, userName, metav1.GetOptions{})
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, orgBackendLookupError(err)
	}
	if !membershipIndexCovers(index, orgUUID, wsUUID) {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	clusterID, err := m.bootstrapper.GetChildWorkspaceClusterName(ctx, orgUUID, wsUUID)
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, orgBackendLookupError(err)
	}
	if clusterID == "" {
		return serviceaccounts.OrgBackendClaims{}, fmt.Errorf("%w: selected workspace has no ready cluster", errOrgBackendStateUnavailable)
	}
	provider, found := m.providers.GetForOrg(orgUUID, providerName)
	if !found || provider.Name != providerName || provider.OrgUUID != orgUUID || provider.APIExportPath == "" || provider.APIExportName == "" || len(provider.APIGroups) == 0 {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	providerWorkspace, err := m.orgProviders.GetOrgProviderWorkspace(ctx, orgUUID, providerName)
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, orgBackendLookupError(err)
	}
	if providerWorkspace == nil || providerWorkspace.Name != providerName || providerWorkspace.Cluster == "" || providerWorkspace.Phase != "Ready" || providerWorkspace.Terminating ||
		providerWorkspace.BootstrapPolicy != providers.OrgProviderBootstrapPolicy ||
		provider.CatalogEntryCluster == "" || provider.WorkspaceCluster == "" || provider.CatalogEntryCluster != providerWorkspace.Cluster || provider.WorkspaceCluster != providerWorkspace.Cluster {
		return serviceaccounts.OrgBackendClaims{}, errOrgBackendIdentityUnavailable
	}
	bindings, err := m.bootstrapper.ListProviderAPIBindings(ctx, orgUUID, wsUUID)
	if err != nil {
		return serviceaccounts.OrgBackendClaims{}, orgBackendLookupError(err)
	}
	binding, ok := bindings[providerName]
	if !ok || binding.Name != providerName || binding.UID == "" || binding.Terminating || !binding.SelfHosted ||
		binding.ExportPath != provider.APIExportPath || binding.ExportName != provider.APIExportName ||
		binding.ExportCluster == "" || binding.ExportCluster != providerWorkspace.Cluster ||
		!bindingHasOwnProviderResource(provider, binding.BoundResources) {
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

// OrgBackendHandler mediates backend KCP-shaped requests through the same
// organization trust policy as the browser gateway. The injected dispatcher must perform the KCP call
// as the live human named by the verified token, never as the provider.
func (m *Manager) OrgBackendHandler() http.Handler { return &orgBackendHandler{mgr: m} }

type orgBackendHandler struct{ mgr *Manager }

func (h *orgBackendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mgr == nil || h.mgr.orgBackendKeys == nil || h.mgr.orgBackendDispatcher == nil || h.mgr.orgTrustStore == nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider authorization is unavailable")
		return
	}
	claims, err := h.mgr.verifyOrgBackendCredential(r)
	if err != nil {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "organization provider identity is invalid, expired or no longer approved")
		return
	}
	requestCtx, err := h.mgr.authorizeOrgTrustRequest(r.Context(), claims, false)
	if err != nil {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "organization provider identity is invalid, expired or no longer approved")
		return
	}
	h.mgr.DispatchOrgTrust(w, r.WithContext(requestCtx), claims)
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
