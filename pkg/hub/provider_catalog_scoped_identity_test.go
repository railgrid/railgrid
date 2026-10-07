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
	"net/http"
	"net/http/httptest"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const (
	testCatalogProjectCluster = "cluster123"
	testCatalogProjectName    = "project-1"
	testCatalogProjectUID     = "project-uid-1"
	testCatalogOrg            = "org-1"
	testCatalogWorkspace      = "workspace-1"
	testCatalogToken          = "project-scoped-token"
)

type catalogWorkspaceConfig struct {
	org, workspace string
}

func (c *catalogWorkspaceConfig) ChildWorkspaceConfig(org, workspace string) *rest.Config {
	c.org, c.workspace = org, workspace
	return &rest.Config{Host: "https://kcp.test"}
}

type catalogScopedRecordReader struct {
	record *tenancyv1alpha1.ScopedIdentity
	err    error
	name   string
}

func (r *catalogScopedRecordReader) Get(_ context.Context, name string, _ metav1.GetOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	r.name = name
	if r.err != nil {
		return nil, r.err
	}
	if r.record == nil || r.record.Name != name {
		return nil, errors.New("record not found")
	}
	return r.record.DeepCopy(), nil
}

type catalogOwnerProbe struct {
	found   bool
	uid     string
	err     error
	cluster string
	owner   identity.Owner
	calls   int
}

func (p *catalogOwnerProbe) Exists(_ context.Context, cluster string, owner identity.Owner) (bool, string, error) {
	p.calls++
	p.cluster, p.owner = cluster, owner
	return p.found, p.uid, p.err
}

func validCatalogProjectRecord() (*tenancyv1alpha1.ScopedIdentity, string) {
	owner := identity.Owner{
		Provider: projectCatalogOwnerProvider,
		Kind:     projectCatalogOwnerKind,
		Group:    projectCatalogOwnerGroup,
		Version:  projectCatalogOwnerVersion,
		Resource: projectCatalogOwnerResource,
		Name:     testCatalogProjectName,
		UID:      testCatalogProjectUID,
	}
	serviceAccountName := identity.ServiceAccountName(owner)
	recordName := identity.RecordName(testCatalogProjectCluster, serviceAccountName)
	return &tenancyv1alpha1.ScopedIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: recordName, Generation: 3},
		Spec: tenancyv1alpha1.ScopedIdentitySpec{
			Owner: tenancyv1alpha1.ScopedIdentityOwner{
				Provider: owner.Provider, Kind: owner.Kind, Group: owner.Group,
				Version: owner.Version, Resource: owner.Resource, Name: owner.Name,
				UID: owner.UID, ClusterID: testCatalogProjectCluster,
			},
			ClusterID: testCatalogProjectCluster, ServiceAccountName: serviceAccountName,
			Attestation: tenancyv1alpha1.ScopedIdentityAttestation{
				Mode:    tenancyv1alpha1.ScopedIdentityAttestationProvider,
				Subject: providers.ProviderSAUsername,
			},
		},
		Status: tenancyv1alpha1.ScopedIdentityStatus{
			Phase: tenancyv1alpha1.ScopedIdentityReady, ServiceAccount: serviceAccountName,
			ObservedGeneration: 3,
		},
	}, serviceAccountName
}

func validCatalogProjectRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, providers.PathListProviders, nil)
	req.Header.Set("Authorization", "Bearer "+testCatalogToken)
	req.Header.Set(headerRailgridOrg, testCatalogOrg)
	req.Header.Set(headerRailgridWorkspace, testCatalogWorkspace)
	req.Header.Set(dataplane.HeaderTenant, testCatalogProjectCluster)
	req.Header.Set(dataplane.HeaderCluster, testCatalogProjectCluster)
	return req
}

func newCatalogProjectResolver(record *tenancyv1alpha1.ScopedIdentity, account string, ownerFound bool) (*providerCatalogScopedIdentityResolver, *catalogWorkspaceConfig, *catalogScopedRecordReader, *catalogOwnerProbe, *int) {
	workspaces := &catalogWorkspaceConfig{}
	records := &catalogScopedRecordReader{record: record}
	owners := &catalogOwnerProbe{found: ownerFound, uid: testCatalogProjectUID}
	reviews := 0
	resolver := &providerCatalogScopedIdentityResolver{
		workspaces: workspaces,
		records:    records,
		owners:     owners,
		clusterID: func(_ context.Context, path string) (string, error) {
			if path != workspacePathRoot+":"+testCatalogOrg+":"+testCatalogWorkspace {
				return "", errors.New("unexpected tenant path")
			}
			return testCatalogProjectCluster, nil
		},
		review: func(_ context.Context, cfg *rest.Config, token, audience string) (authnv1.TokenReviewStatus, error) {
			reviews++
			if cfg == nil || cfg.Host != "https://kcp.test" || token != testCatalogToken || audience != serviceaccounts.WorkloadIdentityTokenAudience {
				return authnv1.TokenReviewStatus{}, errors.New("unexpected TokenReview inputs")
			}
			return authnv1.TokenReviewStatus{
				Authenticated: true,
				Audiences:     []string{serviceaccounts.WorkloadIdentityTokenAudience},
				User: authnv1.UserInfo{
					Username: "system:serviceaccount:" + serviceaccounts.Namespace + ":" + account,
					UID:      "service-account-uid",
				},
			}, nil
		},
	}
	return resolver, workspaces, records, owners, &reviews
}

func TestProviderCatalogScopedIdentityRequiresLiveCentralProjectOwner(t *testing.T) {
	record, account := validCatalogProjectRecord()
	resolver, workspaces, records, owners, reviews := newCatalogProjectResolver(record, account, true)
	user, path, err := resolver.resolve(validCatalogProjectRequest())
	if err != nil {
		t.Fatalf("resolve returned error: %v", err)
	}
	if user != "system:serviceaccount:default:"+account || path != workspacePathRoot+":"+testCatalogOrg+":"+testCatalogWorkspace {
		t.Fatalf("resolve = (%q, %q), want Project service account and selected tenant path", user, path)
	}
	if workspaces.org != testCatalogOrg || workspaces.workspace != testCatalogWorkspace {
		t.Fatalf("TokenReview workspace = %q/%q, want selected tenant", workspaces.org, workspaces.workspace)
	}
	if records.name != record.Name || *reviews != 1 || owners.calls != 1 || owners.cluster != testCatalogProjectCluster {
		t.Fatalf("verification calls: record=%q reviews=%d owners=%d cluster=%q", records.name, *reviews, owners.calls, owners.cluster)
	}
	if owners.owner.Provider != projectCatalogOwnerProvider || owners.owner.Group != projectCatalogOwnerGroup ||
		owners.owner.Resource != projectCatalogOwnerResource || owners.owner.Name != testCatalogProjectName || owners.owner.UID != testCatalogProjectUID {
		t.Fatalf("owner probe got %#v", owners.owner)
	}
	if len(record.Spec.Annotations) != 0 {
		t.Fatalf("test requires record annotations to be absent, got %v", record.Spec.Annotations)
	}
}

func TestProviderCatalogScopedIdentityRejectsMismatchedOrAmbiguousTenantHeaders(t *testing.T) {
	record, account := validCatalogProjectRecord()
	cases := map[string]func(*http.Request){
		"missing workspace": func(req *http.Request) { req.Header.Del(headerRailgridWorkspace) },
		"duplicate org":     func(req *http.Request) { req.Header.Add(headerRailgridOrg, testCatalogOrg) },
		"comma org":         func(req *http.Request) { req.Header.Set(headerRailgridOrg, testCatalogOrg+",other") },
		"tenant mismatch":   func(req *http.Request) { req.Header.Set(dataplane.HeaderTenant, "othercluster") },
		"cluster mismatch":  func(req *http.Request) { req.Header.Set(dataplane.HeaderCluster, "othercluster") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			resolver, _, _, owners, reviews := newCatalogProjectResolver(record, account, true)
			req := validCatalogProjectRequest()
			mutate(req)
			if _, _, err := resolver.resolve(req); err == nil {
				t.Fatal("resolve accepted ambiguous or mismatched tenant headers")
			}
			if *reviews != 0 || owners.calls != 0 {
				t.Fatalf("unvalidated tenant reached TokenReview/owner probe: reviews=%d owners=%d", *reviews, owners.calls)
			}
		})
	}
}

func TestProviderCatalogScopedIdentityRejectsInvalidReviewAndStaleRecords(t *testing.T) {
	record, account := validCatalogProjectRecord()
	reviewStatus := authnv1.TokenReviewStatus{
		Authenticated: true,
		Audiences:     []string{serviceaccounts.WorkloadIdentityTokenAudience},
		User:          authnv1.UserInfo{Username: "system:serviceaccount:default:" + account},
	}
	validRecord := record.DeepCopy()
	cases := map[string]struct {
		mutateReview func(*authnv1.TokenReviewStatus)
		mutateRecord func(*tenancyv1alpha1.ScopedIdentity)
		ownerFound   bool
	}{
		"unauthenticated": {mutateReview: func(status *authnv1.TokenReviewStatus) { status.Authenticated = false }, ownerFound: true},
		"wrong audience":  {mutateReview: func(status *authnv1.TokenReviewStatus) { status.Audiences = []string{"other"} }, ownerFound: true},
		"wrong subject": {mutateReview: func(status *authnv1.TokenReviewStatus) {
			status.User.Username = "system:serviceaccount:default:railgrid-wi-fake"
		}, ownerFound: true},
		"wrong owner provider":   {mutateRecord: func(record *tenancyv1alpha1.ScopedIdentity) { record.Spec.Owner.Provider = "other" }, ownerFound: true},
		"wrong owner coordinate": {mutateRecord: func(record *tenancyv1alpha1.ScopedIdentity) { record.Spec.Owner.Resource = "secrets" }, ownerFound: true},
		"wrong cluster":          {mutateRecord: func(record *tenancyv1alpha1.ScopedIdentity) { record.Spec.ClusterID = "othercluster" }, ownerFound: true},
		"stale generation":       {mutateRecord: func(record *tenancyv1alpha1.ScopedIdentity) { record.Status.ObservedGeneration-- }, ownerFound: true},
		"not ready": {mutateRecord: func(record *tenancyv1alpha1.ScopedIdentity) {
			record.Status.Phase = tenancyv1alpha1.ScopedIdentityPending
		}, ownerFound: true},
		"owner missing": {ownerFound: false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			current := validRecord.DeepCopy()
			if test.mutateRecord != nil {
				test.mutateRecord(current)
			}
			resolver, _, _, _, _ := newCatalogProjectResolver(current, account, test.ownerFound)
			if test.mutateReview != nil {
				resolver.review = func(context.Context, *rest.Config, string, string) (authnv1.TokenReviewStatus, error) {
					status := reviewStatus
					test.mutateReview(&status)
					return status, nil
				}
			}
			if _, _, err := resolver.resolve(validCatalogProjectRequest()); err == nil {
				t.Fatal("resolve accepted an invalid review, stale record, or missing owner")
			}
		})
	}
}

func TestProviderCatalogScopedIdentityOnlyAddsAddressScope(t *testing.T) {
	record, account := validCatalogProjectRecord()
	project, _, _, _, _ := newCatalogProjectResolver(record, account, true)
	verify := providerCatalogWorkloadVerifier(func(*http.Request) (string, string, error) {
		return "", "", errors.New("existing workload verifier did not recognize Project identity")
	}, project)
	var got tenant.TenantContext
	called := false
	middleware := providerCatalogMiddleware(func(next http.Handler) http.Handler { return next }, verify)
	handler := middleware(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got, called = tenant.FromContext(req.Context())
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validCatalogProjectRequest())
	if response.Code != http.StatusOK || !called {
		t.Fatalf("catalog status/called = %d/%v, want 200/true", response.Code, called)
	}
	if got.User != "system:serviceaccount:default:"+account || got.OrgUUID != testCatalogOrg || got.WorkspaceUUID != testCatalogWorkspace || got.Role != "" || got.OrgRole != "" {
		t.Fatalf("Project identity received broader tenant context: %#v", got)
	}
}

func TestProviderCatalogActionProofBranchPrecedesProjectScopedFallback(t *testing.T) {
	projectResolverCalled := false
	verifyCalled := false
	verify := func(*http.Request) (string, string, error) {
		verifyCalled = true
		return "user", workspacePathRoot + ":" + testCatalogOrg + ":" + testCatalogWorkspace, nil
	}
	proof := func(context.Context, *http.Request) (actionproof.Claims, error) {
		return actionproof.Claims{}, errors.New("proof rejected")
	}
	handler := providerCatalogMiddleware(func(next http.Handler) http.Handler { return next }, verify, proof)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		projectResolverCalled = true
	}))
	req := validCatalogProjectRequest()
	req.Header.Set(actionproof.Header, "bad-proof")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized || verifyCalled || projectResolverCalled {
		t.Fatalf("proof branch did not fail closed first: status=%d verifier=%v next=%v", response.Code, verifyCalled, projectResolverCalled)
	}
}

func TestProviderCatalogScopedIdentityVerifierPreservesExistingWorkloadAdmission(t *testing.T) {
	project := &providerCatalogScopedIdentityResolver{}
	projectResolverCalled := false
	project.clusterID = func(context.Context, string) (string, error) {
		projectResolverCalled = true
		return "", errors.New("Project identity fallback must not run")
	}
	verify := providerCatalogWorkloadVerifier(func(*http.Request) (string, string, error) {
		return "existing-workload", workspacePathRoot + ":" + testCatalogOrg + ":" + testCatalogWorkspace, nil
	}, project)
	user, path, err := verify(validCatalogProjectRequest())
	if err != nil || user != "existing-workload" || path != workspacePathRoot+":"+testCatalogOrg+":"+testCatalogWorkspace || projectResolverCalled {
		t.Fatalf("existing workload path changed: user=%q path=%q err=%v", user, path, err)
	}
}
