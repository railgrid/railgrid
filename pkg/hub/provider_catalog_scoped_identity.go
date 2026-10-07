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

package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const (
	projectCatalogIdentityPrefix = "railgrid-si-"
	projectCatalogOwnerProvider  = "app-studio"
	projectCatalogOwnerKind      = "Project"
	projectCatalogOwnerGroup     = "ai.railgrid.ai"
	projectCatalogOwnerVersion   = "v1alpha1"
	projectCatalogOwnerResource  = "projects"
)

type scopedIdentityRecordGetter interface {
	Get(context.Context, string, metav1.GetOptions) (*tenancyv1alpha1.ScopedIdentity, error)
}

type providerCatalogTokenReviewer func(context.Context, *rest.Config, string, string) (authnv1.TokenReviewStatus, error)

// providerCatalogScopedIdentityResolver admits a Project-scoped identity to
// the read-only provider catalog. It never resolves or assigns a human role.
// The bearer is checked online in the selected child workspace, its name must
// point at a central hub-owned ScopedIdentity record, and the record's Project
// owner is checked live with the hub's owner probe. Tenant headers address the
// selected workspace only after the authoritative path-to-cluster mapping and
// the TokenReview agree.
type providerCatalogScopedIdentityResolver struct {
	workspaces serviceaccounts.WorkspaceConfigBuilder
	records    scopedIdentityRecordGetter
	owners     identity.OwnerProbe
	clusterID  func(context.Context, string) (string, error)
	review     providerCatalogTokenReviewer
}

// providerCatalogWorkloadVerifier keeps the existing online workload and
// delegated-token contract first. Project-scoped identities get a separate,
// narrower verification path because their account is recorded centrally but
// intentionally has none of the workload Project/environment annotations.
func providerCatalogWorkloadVerifier(workload func(*http.Request) (string, string, error), project *providerCatalogScopedIdentityResolver) func(*http.Request) (string, string, error) {
	return func(req *http.Request) (string, string, error) {
		if workload != nil {
			user, path, err := workload(req)
			if err == nil {
				return user, path, nil
			}
			if project == nil {
				return "", "", err
			}
			projectUser, projectPath, projectErr := project.resolve(req)
			if projectErr == nil {
				return projectUser, projectPath, nil
			}
			return "", "", projectErr
		}
		if project == nil {
			return "", "", errors.New("provider catalog workload verifier is unavailable")
		}
		return project.resolve(req)
	}
}

func (v *providerCatalogScopedIdentityResolver) resolve(req *http.Request) (string, string, error) {
	if v == nil || req == nil || v.workspaces == nil || v.records == nil || v.owners == nil || v.clusterID == nil {
		return "", "", errors.New("project-scoped catalog identity verification is unavailable")
	}
	if req.Method != http.MethodGet || req.URL == nil || req.URL.Path != providers.PathListProviders {
		return "", "", errors.New("project-scoped identities are limited to the provider catalog GET")
	}

	orgUUID, err := oneScopedCatalogHeader(req, headerRailgridOrg)
	if err != nil {
		return "", "", err
	}
	workspaceUUID, err := oneScopedCatalogHeader(req, headerRailgridWorkspace)
	if err != nil {
		return "", "", err
	}
	tenantHeader, err := oneScopedCatalogHeader(req, dataplane.HeaderTenant)
	if err != nil {
		return "", "", err
	}
	clusterHeader, err := oneScopedCatalogHeader(req, dataplane.HeaderCluster)
	if err != nil {
		return "", "", err
	}
	if !validScopedCatalogTenantID(orgUUID) || !validScopedCatalogTenantID(workspaceUUID) || !dataplane.IsClusterID(tenantHeader) || !dataplane.IsClusterID(clusterHeader) {
		return "", "", errors.New("project-scoped catalog identity requires concrete tenant headers")
	}

	tenantPath := workspacePathRoot + ":" + orgUUID + ":" + workspaceUUID
	clusterID, err := v.clusterID(req.Context(), tenantPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving project identity tenant: %w", err)
	}
	if !dataplane.IsClusterID(clusterID) || tenantHeader != clusterID || clusterHeader != clusterID {
		return "", "", errors.New("project-scoped catalog identity tenant headers do not match the selected workspace")
	}

	token, err := oneBearerToken(req)
	if err != nil {
		return "", "", err
	}
	review := v.review
	if review == nil {
		review = reviewProviderCatalogToken
	}
	status, err := review(req.Context(), v.workspaces.ChildWorkspaceConfig(orgUUID, workspaceUUID), token, serviceaccounts.WorkloadIdentityTokenAudience)
	if err != nil {
		return "", "", fmt.Errorf("reviewing project-scoped catalog identity: %w", err)
	}
	if !status.Authenticated || !hasAudience(status.Audiences, serviceaccounts.WorkloadIdentityTokenAudience) {
		return "", "", errors.New("project-scoped catalog token is not authenticated for the workload audience")
	}
	username, serviceAccountName, ok := parseProjectCatalogServiceAccount(status.User.Username)
	if !ok {
		return "", "", errors.New("project-scoped catalog token subject is not a Project identity")
	}

	recordName := identity.RecordName(clusterID, serviceAccountName)
	record, err := v.records.Get(req.Context(), recordName, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("reading project-scoped identity record: %w", err)
	}
	if !validProjectCatalogIdentityRecord(record, recordName, clusterID, serviceAccountName) {
		return "", "", errors.New("project-scoped catalog token has no current App Studio Project identity record")
	}

	owner := identity.Owner{
		Provider:  projectCatalogOwnerProvider,
		Kind:      projectCatalogOwnerKind,
		Group:     projectCatalogOwnerGroup,
		Version:   projectCatalogOwnerVersion,
		Resource:  projectCatalogOwnerResource,
		Name:      record.Spec.Owner.Name,
		UID:       record.Spec.Owner.UID,
		ClusterID: clusterID,
	}
	found, uid, err := v.owners.Exists(req.Context(), clusterID, owner)
	if err != nil {
		return "", "", fmt.Errorf("verifying project-scoped catalog owner: %w", err)
	}
	if !found || uid == "" || uid != owner.UID {
		return "", "", errors.New("project-scoped catalog identity owner is no longer current")
	}
	return username, tenantPath, nil
}

func oneScopedCatalogHeader(req *http.Request, name string) (string, error) {
	values := req.Header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" || values[0] != strings.TrimSpace(values[0]) || strings.ContainsAny(values[0], "\r\n,") {
		return "", fmt.Errorf("project-scoped catalog identity requires one %s header", name)
	}
	return values[0], nil
}

func validScopedCatalogTenantID(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, ":/\\\x00 \t")
}

func oneBearerToken(req *http.Request) (string, error) {
	values := req.Header.Values("Authorization")
	if len(values) != 1 {
		return "", errors.New("project-scoped catalog identity requires one bearer token")
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || strings.ContainsAny(fields[1], "\r\n") {
		return "", errors.New("project-scoped catalog identity bearer is malformed")
	}
	return fields[1], nil
}

func reviewProviderCatalogToken(ctx context.Context, cfg *rest.Config, token, audience string) (authnv1.TokenReviewStatus, error) {
	if cfg == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(audience) == "" {
		return authnv1.TokenReviewStatus{}, errors.New("workspace config, token, and audience are required")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return authnv1.TokenReviewStatus{}, fmt.Errorf("building child-workspace TokenReview client: %w", err)
	}
	review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token, Audiences: []string{audience}},
	}, metav1.CreateOptions{})
	if err != nil {
		return authnv1.TokenReviewStatus{}, err
	}
	return review.Status, nil
}

func hasAudience(audiences []string, want string) bool {
	for _, audience := range audiences {
		if audience == want {
			return true
		}
	}
	return false
}

func parseProjectCatalogServiceAccount(username string) (string, string, bool) {
	parts := strings.Split(username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] != serviceaccounts.Namespace || !strings.HasPrefix(parts[3], projectCatalogIdentityPrefix) || len(parts[3]) != len(projectCatalogIdentityPrefix)+40 {
		return "", "", false
	}
	for _, char := range strings.TrimPrefix(parts[3], projectCatalogIdentityPrefix) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", "", false
		}
	}
	return username, parts[3], true
}

func validProjectCatalogIdentityRecord(record *tenancyv1alpha1.ScopedIdentity, expectedName, clusterID, serviceAccountName string) bool {
	if record == nil || record.DeletionTimestamp != nil || record.Name != expectedName || record.Generation <= 0 ||
		record.Spec.ClusterID != clusterID || record.Spec.Owner.ClusterID != clusterID ||
		record.Spec.ServiceAccountName != serviceAccountName ||
		record.Spec.Owner.Provider != projectCatalogOwnerProvider || record.Spec.Owner.Kind != projectCatalogOwnerKind ||
		record.Spec.Owner.Group != projectCatalogOwnerGroup || record.Spec.Owner.Version != projectCatalogOwnerVersion ||
		record.Spec.Owner.Resource != projectCatalogOwnerResource || record.Spec.Owner.Name == "" || record.Spec.Owner.UID == "" ||
		record.Spec.Attestation.Mode != tenancyv1alpha1.ScopedIdentityAttestationProvider ||
		record.Spec.Attestation.Subject != providers.ProviderSAUsername ||
		record.Status.Phase != tenancyv1alpha1.ScopedIdentityReady || record.Status.ServiceAccount != serviceAccountName ||
		record.Status.ObservedGeneration != record.Generation {
		return false
	}
	owner := identity.Owner{
		Provider: projectCatalogOwnerProvider,
		Kind:     projectCatalogOwnerKind,
		Group:    projectCatalogOwnerGroup,
		Version:  projectCatalogOwnerVersion,
		Resource: projectCatalogOwnerResource,
		Name:     record.Spec.Owner.Name,
		UID:      record.Spec.Owner.UID,
	}
	return identity.ServiceAccountName(owner) == serviceAccountName && identity.RecordName(clusterID, serviceAccountName) == expectedName
}
