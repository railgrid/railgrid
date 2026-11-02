/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

// memoryRecords is an in-memory ScopedIdentity store. The real one is a
// dynamic client over root:railgrid:system:tenants; nothing in the service
// depends on which, which is the point of the RecordStore seam.
type memoryRecords struct {
	items map[string]*tenancyv1alpha1.ScopedIdentity
	// creates counts Create calls so idempotence is observable, not inferred.
	creates, updates int
	resourceVersion  int64
}

func newMemoryRecords() *memoryRecords {
	return &memoryRecords{items: map[string]*tenancyv1alpha1.ScopedIdentity{}}
}

func (m *memoryRecords) Get(_ context.Context, name string, _ metav1.GetOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	item, ok := m.items[name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "scopedidentities"}, name)
	}
	return item.DeepCopy(), nil
}

func (m *memoryRecords) List(_ context.Context, opts metav1.ListOptions) (*tenancyv1alpha1.ScopedIdentityList, error) {
	list := &tenancyv1alpha1.ScopedIdentityList{}
	names := make([]string, 0, len(m.items))
	for name := range m.items {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		item := m.items[name]
		if !matchesSelector(item.Labels, opts.LabelSelector) {
			continue
		}
		list.Items = append(list.Items, *item.DeepCopy())
	}
	return list, nil
}

func matchesSelector(labels map[string]string, selector string) bool {
	if strings.TrimSpace(selector) == "" {
		return true
	}
	for _, term := range strings.Split(selector, ",") {
		if strings.HasPrefix(term, "!") {
			if _, exists := labels[strings.TrimPrefix(term, "!")]; exists {
				return false
			}
			continue
		}
		parts := strings.SplitN(term, "=", 2)
		if len(parts) != 2 || labels[parts[0]] != parts[1] {
			return false
		}
	}
	return true
}

func TestOrgIdentityTuplesCannotCollideWithPlatformIdentityTuples(t *testing.T) {
	platform := Owner{Provider: "factory", Group: "factory.example", Kind: "Run", Name: "run-a", UID: "uid-1"}
	organization := platform
	organization.OrgUUID = "org-a"
	if ServiceAccountName(platform) == ServiceAccountName(organization) {
		t.Fatal("same-name platform and org providers received the same identity account")
	}
	if OwnerHash("team-a", platform) == OwnerHash("team-a", organization) {
		t.Fatal("same-name platform and org providers received the same record owner hash")
	}
}

func (m *memoryRecords) Create(_ context.Context, obj *tenancyv1alpha1.ScopedIdentity, _ metav1.CreateOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	if _, ok := m.items[obj.Name]; ok {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "scopedidentities"}, obj.Name)
	}
	m.creates++
	copied := obj.DeepCopy()
	copied.UID = types.UID("record-" + obj.Name)
	copied.Generation = 1
	copied.ResourceVersion = m.nextResourceVersion()
	m.items[obj.Name] = copied
	return copied.DeepCopy(), nil
}

func (m *memoryRecords) Update(_ context.Context, obj *tenancyv1alpha1.ScopedIdentity, _ metav1.UpdateOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	existing, ok := m.items[obj.Name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "scopedidentities"}, obj.Name)
	}
	if obj.ResourceVersion != existing.ResourceVersion {
		return nil, apierrors.NewConflict(schema.GroupResource{Resource: "scopedidentities"}, obj.Name, errors.New("resourceVersion changed"))
	}
	m.updates++
	copied := obj.DeepCopy()
	if !reflect.DeepEqual(existing.Spec, copied.Spec) {
		copied.Generation = existing.Generation + 1
	}
	copied.ResourceVersion = m.nextResourceVersion()
	m.items[obj.Name] = copied
	return copied.DeepCopy(), nil
}

func (m *memoryRecords) UpdateStatus(_ context.Context, obj *tenancyv1alpha1.ScopedIdentity, _ metav1.UpdateOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	existing, ok := m.items[obj.Name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "scopedidentities"}, obj.Name)
	}
	if obj.ResourceVersion != existing.ResourceVersion {
		return nil, apierrors.NewConflict(schema.GroupResource{Resource: "scopedidentities"}, obj.Name, errors.New("resourceVersion changed"))
	}
	existing.Status = *obj.Status.DeepCopy()
	existing.ResourceVersion = m.nextResourceVersion()
	return existing.DeepCopy(), nil
}

func (m *memoryRecords) nextResourceVersion() string {
	m.resourceVersion++
	return strconv.FormatInt(m.resourceVersion, 10)
}

func (m *memoryRecords) Delete(_ context.Context, name string, _ metav1.DeleteOptions) error {
	if _, ok := m.items[name]; !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "scopedidentities"}, name)
	}
	delete(m.items, name)
	return nil
}

// fakeOwners is a settable owner probe.
type fakeOwners struct {
	present         map[string]string // "<kind>/<name>" -> uid
	generation      int64
	resourceVersion string
	err             error
}

type fakeRevisionOwners struct {
	uid             string
	generation      int64
	resourceVersion string
	deleting        bool
	err             error
}

func (f *fakeRevisionOwners) Exists(ctx context.Context, clusterID string, owner Owner) (bool, string, error) {
	found, observation, err := f.Observe(ctx, clusterID, owner)
	return found, observation.UID, err
}

func (f *fakeRevisionOwners) Observe(_ context.Context, _ string, owner Owner) (bool, OwnerObservation, error) {
	if f.err != nil {
		return false, OwnerObservation{}, f.err
	}
	observation := OwnerObservation{UID: f.uid, Generation: f.generation, ResourceVersion: f.resourceVersion}
	if f.deleting || owner.UID != "" && owner.UID != f.uid {
		return false, observation, nil
	}
	return true, observation, nil
}

func (f *fakeOwners) Exists(_ context.Context, _ string, owner Owner) (bool, string, error) {
	if f.err != nil {
		return false, "", f.err
	}
	uid, ok := f.present[owner.Kind+"/"+owner.Name]
	if !ok {
		return false, "", nil
	}
	if owner.UID != "" && owner.UID != uid {
		return false, uid, nil
	}
	return true, uid, nil
}

func (f *fakeOwners) Observe(_ context.Context, _ string, owner Owner) (bool, OwnerObservation, error) {
	if f.err != nil {
		return false, OwnerObservation{}, f.err
	}
	uid, ok := f.present[owner.Kind+"/"+owner.Name]
	if !ok {
		return false, OwnerObservation{}, nil
	}
	observation := OwnerObservation{UID: uid, Generation: f.generation, ResourceVersion: f.resourceVersion}
	if owner.UID != "" && owner.UID != uid {
		return false, observation, nil
	}
	return true, observation, nil
}

func testService(t *testing.T) (*Service, *memoryRecords, *fake.Clientset, *fakeOwners) {
	t.Helper()
	records := newMemoryRecords()
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		request := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenRequest)
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "minted",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Duration(*request.Spec.ExpirationSeconds) * time.Second)),
		}}, nil
	})
	owners := &fakeOwners{
		present:    map[string]string{"Agent/scheduler": "agent-uid-1"},
		generation: 1, resourceVersion: "owner-rv-1",
	}
	service := New(Options{
		Records: records,
		Clients: func(string) (kubernetes.Interface, error) { return cs, nil },
		Policy:  testPolicy(),
		Owners:  owners,
	})
	return service, records, cs, owners
}

type staticOrgPolicyResolver struct{ policy *Policy }

func (r staticOrgPolicyResolver) Resolve(context.Context, string, string, string) (*Policy, error) {
	return r.policy, nil
}

type failingOrgPolicyResolver struct{ err error }

func (r failingOrgPolicyResolver) Resolve(context.Context, string, string, string) (*Policy, error) {
	return nil, r.err
}

func agentRequest() Request {
	return Request{
		Owner: Owner{
			Provider: "agents", Kind: "Agent", Group: "agents.railgrid.ai",
			Version: "v1alpha1", Resource: "agents", Name: "scheduler", UID: "agent-uid-1",
		},
		ClusterID: "cluster-1",
		Rules: []rbacv1.PolicyRule{
			rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"get"}, []string{"search-1"}),
		},
		TTLSeconds: 900,
	}
}

func TestEnsureIsIdempotentOnTheOwnerTuple(t *testing.T) {
	service, records, cs, _ := testService(t)
	ctx := context.Background()

	first, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "system:serviceaccount:default:provider")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	second, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "system:serviceaccount:default:provider")
	if err != nil {
		t.Fatalf("Ensure (repeat): %v", err)
	}
	if first.ServiceAccount != second.ServiceAccount {
		t.Fatalf("account is not deterministic: %q vs %q", first.ServiceAccount, second.ServiceAccount)
	}
	if first.Name != second.Name {
		t.Fatalf("record name is not deterministic: %q vs %q", first.Name, second.Name)
	}
	if records.creates != 1 {
		t.Fatalf("records created = %d, want 1", records.creates)
	}
	if records.updates != 0 {
		t.Fatalf("an unchanged repeat rewrote the record %d times", records.updates)
	}
	// Both calls still minted a token: refreshing is the whole point.
	if first.Token == "" || second.Token == "" || first.ExpiresAt.IsZero() {
		t.Fatalf("Ensure did not mint a token: %#v / %#v", first, second)
	}
	accounts, err := cs.CoreV1().ServiceAccounts("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing accounts: %v", err)
	}
	if len(accounts.Items) != 1 {
		t.Fatalf("accounts created = %d, want 1", len(accounts.Items))
	}
}

func TestOrgAndPlatformIdentityRecordsStaySeparatelyScoped(t *testing.T) {
	service, records, _, _ := testService(t)
	service.orgPolicies = staticOrgPolicyResolver{policy: testPolicy()}
	platformRequest := agentRequest()
	platformToken, err := service.Ensure(context.Background(), platformRequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("platform Ensure: %v", err)
	}
	orgRequest := agentRequest()
	orgRequest.OrgUUID = "org-a"
	orgToken, err := service.Ensure(context.Background(), orgRequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("org Ensure: %v", err)
	}
	if platformToken.Name == orgToken.Name || platformToken.ServiceAccount == orgToken.ServiceAccount {
		t.Fatalf("platform and org identities collided: platform=%#v org=%#v", platformToken, orgToken)
	}
	platformItems, err := service.List(context.Background(), "agents", "cluster-1", nil)
	if err != nil {
		t.Fatalf("platform List: %v", err)
	}
	if len(platformItems) != 1 || platformItems[0].Name != platformToken.Name {
		t.Fatalf("platform list included an org identity: %v", recordNames(platformItems))
	}
	orgItems, err := service.ListForOrg(context.Background(), "org-a", "agents", "cluster-1", nil)
	if err != nil {
		t.Fatalf("org List: %v", err)
	}
	if len(orgItems) != 1 || orgItems[0].Name != orgToken.Name {
		t.Fatalf("org list included a platform identity: %v", recordNames(orgItems))
	}
	if records.items[orgToken.Name].Labels[labelScopedIdentityOrg] != "org-a" {
		t.Fatalf("org identity record label = %q", records.items[orgToken.Name].Labels[labelScopedIdentityOrg])
	}
	if err := service.ReleaseByName(context.Background(), "agents", orgToken.Name); err != nil {
		t.Fatalf("platform ReleaseByName: %v", err)
	}
	if _, ok := records.items[orgToken.Name]; !ok {
		t.Fatal("platform attestation released an org-owned identity")
	}
}

func TestOrgIdentityRequiresOwnerUID(t *testing.T) {
	service, _, _, _ := testService(t)
	service.orgPolicies = staticOrgPolicyResolver{policy: testPolicy()}
	request := agentRequest()
	request.OrgUUID = "org-a"
	request.Owner.UID = ""
	_, err := service.Ensure(context.Background(), request, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want invalid-request refusal", err)
	}
}

func TestReleaseByNameForOrgIsScopedToTheAuthenticatedOrganization(t *testing.T) {
	service, records, cs, _ := testService(t)
	service.orgPolicies = staticOrgPolicyResolver{policy: testPolicy()}
	ctx := context.Background()
	platform, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("platform Ensure: %v", err)
	}
	orgRequest := agentRequest()
	orgRequest.OrgUUID = "org-a"
	orgToken, err := service.Ensure(ctx, orgRequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("org Ensure: %v", err)
	}
	if err := service.ReleaseByNameForOrg(ctx, "org-a", "agents", platform.Name); err != nil {
		t.Fatalf("cross-owner release: %v", err)
	}
	if _, ok := records.items[platform.Name]; !ok {
		t.Fatal("an organization released a platform identity")
	}
	if err := service.ReleaseByNameForOrg(ctx, "org-b", "agents", orgToken.Name); err != nil {
		t.Fatalf("cross-org release: %v", err)
	}
	if _, ok := records.items[orgToken.Name]; !ok {
		t.Fatal("another organization released the identity")
	}
	if err := service.ReleaseByNameForOrg(ctx, "org-a", "edges", orgToken.Name); err != nil {
		t.Fatalf("wrong-provider release: %v", err)
	}
	if _, ok := records.items[orgToken.Name]; !ok {
		t.Fatal("another provider released the identity")
	}
	if err := service.ReleaseByNameForOrg(ctx, "org-a", "agents", orgToken.Name); err != nil {
		t.Fatalf("scoped release: %v", err)
	}
	if _, ok := records.items[orgToken.Name]; ok {
		t.Fatal("the organization identity record survived release")
	}
	assertIdentityGone(t, ctx, cs, orgToken.ServiceAccount)
}

func TestReleaseForOrgIsScopedToTheAuthenticatedOrganizationAndOwner(t *testing.T) {
	service, records, cs, _ := testService(t)
	service.orgPolicies = staticOrgPolicyResolver{policy: testPolicy()}
	ctx := context.Background()
	owner := agentRequest().Owner

	platform, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("platform Ensure: %v", err)
	}
	orgARequest := agentRequest()
	orgARequest.OrgUUID = "org-a"
	orgA, err := service.Ensure(ctx, orgARequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("org-a Ensure: %v", err)
	}
	orgBRequest := agentRequest()
	orgBRequest.OrgUUID = "org-b"
	orgB, err := service.Ensure(ctx, orgBRequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("org-b Ensure: %v", err)
	}

	orgOwner := owner
	orgOwner.OrgUUID = "org-a"
	err = service.Release(ctx, "cluster-1", orgOwner)
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeInvalidRequest {
		t.Fatalf("platform Release accepted an org owner: %v", err)
	}
	if len(records.items) != 3 {
		t.Fatalf("refused platform Release changed records: %d", len(records.items))
	}

	if err := service.ReleaseForOrg(ctx, "org-a", "cluster-1", owner); err != nil {
		t.Fatalf("ReleaseForOrg: %v", err)
	}
	if _, ok := records.items[platform.Name]; !ok {
		t.Fatal("organization release removed the platform identity")
	}
	if _, ok := records.items[orgB.Name]; !ok {
		t.Fatal("organization release removed another organization's identity")
	}
	if _, ok := records.items[orgA.Name]; ok {
		t.Fatal("selected organization identity survived release")
	}
	assertIdentityGone(t, ctx, cs, orgA.ServiceAccount)
}

func recordNames(records []tenancyv1alpha1.ScopedIdentity) []string {
	names := make([]string, 0, len(records))
	for _, record := range records {
		names = append(names, record.Name)
	}
	return names
}

func TestEnsureMaterializesOnlyPolicyCheckedRules(t *testing.T) {
	service, _, cs, _ := testService(t)
	ctx := context.Background()

	req := agentRequest()
	// A rule the policy refuses must abort the whole request: a partially
	// applied identity would be a credential nobody asked for.
	req.Rules = append(req.Rules, rule("infrastructure.railgrid.ai", []string{"instances"}, []string{"delete"}, []string{"search-1"}))
	if _, err := service.Ensure(ctx, req, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject"); err == nil {
		t.Fatal("Ensure accepted a refused rule")
	}
	accounts, _ := cs.CoreV1().ServiceAccounts("default").List(ctx, metav1.ListOptions{})
	if len(accounts.Items) != 0 {
		t.Fatalf("a refused request still created %d accounts", len(accounts.Items))
	}

	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	role, err := cs.RbacV1().ClusterRoles().Get(ctx, serviceaccounts.WorkloadIdentityRoleName(token.ServiceAccount), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != "search-1" {
		t.Fatalf("ClusterRole rules = %#v, want one name-scoped get", role.Rules)
	}
	if len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "get" {
		t.Fatalf("ClusterRole verbs = %v, want [get]", role.Rules[0].Verbs)
	}
}

func TestEnsureRefusesAnOwnerThatDoesNotExist(t *testing.T) {
	service, records, _, owners := testService(t)
	owners.present = map[string]string{}
	_, err := service.Ensure(context.Background(), agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnknownOwner {
		t.Fatalf("want %q, got %v", CodeUnknownOwner, err)
	}
	if len(records.items) != 0 {
		t.Fatalf("a refused request recorded %d identities", len(records.items))
	}
}

func TestEnsureRefusesARecreatedOwnerUnderTheOldUID(t *testing.T) {
	service, _, _, owners := testService(t)
	// The Agent was deleted and recreated: same name, new UID. The caller's
	// stale UID must not resolve to the live object.
	owners.present = map[string]string{"Agent/scheduler": "agent-uid-2"}
	_, err := service.Ensure(context.Background(), agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnknownOwner {
		t.Fatalf("want %q for a recreated owner, got %v", CodeUnknownOwner, err)
	}
}

func TestEnsureRequiresAndAcknowledgesExactOwnerRevision(t *testing.T) {
	service, records, _, _ := testService(t)
	service.owners = &fakeRevisionOwners{uid: "agent-uid-1", generation: 7, resourceVersion: "opaque-rv"}
	req := agentRequest()
	req.ExpectedOwnerGeneration = 7
	req.ExpectedOwnerResourceVersion = "opaque-rv"
	token, err := service.Ensure(context.Background(), req, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure versioned request: %v", err)
	}
	if !token.OwnerRevisionVerified {
		t.Fatal("versioned token did not acknowledge owner revision enforcement")
	}
	if len(records.items) != 1 {
		t.Fatalf("record count = %d, want 1", len(records.items))
	}
	for _, record := range records.items {
		revision, versioned, err := recordOwnerRevision(record)
		if err != nil || !versioned || revision.Generation != 7 || revision.ResourceVersion != "opaque-rv" {
			t.Fatalf("persisted owner revision = (%#v, %v, %v)", revision, versioned, err)
		}
		if record.UID == "" || record.Generation < 1 {
			t.Fatalf("test record lacks materialization fence metadata: UID=%q generation=%d", record.UID, record.Generation)
		}
	}
}

func TestEnsureRejectsStaleAndDeletingVersionedOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		owners  *fakeRevisionOwners
		wantErr string
	}{
		{name: "resourceVersion changed", owners: &fakeRevisionOwners{uid: "agent-uid-1", generation: 7, resourceVersion: "new-rv"}, wantErr: CodeStaleOwner},
		{name: "generation changed", owners: &fakeRevisionOwners{uid: "agent-uid-1", generation: 8, resourceVersion: "opaque-rv"}, wantErr: CodeStaleOwner},
		{name: "owner is deleting", owners: &fakeRevisionOwners{uid: "agent-uid-1", generation: 7, resourceVersion: "opaque-rv", deleting: true}, wantErr: CodeStaleOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, records, _, _ := testService(t)
			service.owners = tc.owners
			req := agentRequest()
			req.ExpectedOwnerGeneration = 7
			req.ExpectedOwnerResourceVersion = "opaque-rv"
			_, err := service.Ensure(context.Background(), req, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
			var conflict RevisionConflict
			if !errors.As(err, &conflict) || conflict.Code != tc.wantErr {
				t.Fatalf("Ensure error = %v, want revision conflict %q", err, tc.wantErr)
			}
			if len(records.items) != 0 {
				t.Fatalf("stale owner wrote %d identity records", len(records.items))
			}
		})
	}
}

func TestStaleMaterializationCannotRestoreRevokedRules(t *testing.T) {
	service, records, cs, _ := testService(t)
	owners := &fakeRevisionOwners{uid: "agent-uid-1", generation: 7, resourceVersion: "owner-rv-7"}
	service.owners = owners

	oldRequest := agentRequest()
	oldRequest.ExpectedOwnerGeneration = 7
	oldRequest.ExpectedOwnerResourceVersion = "owner-rv-7"
	oldToken, err := service.Ensure(context.Background(), oldRequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("initial Ensure: %v", err)
	}
	oldRecord, err := records.Get(context.Background(), oldToken.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get old record snapshot: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	staleService := New(Options{
		Records: records,
		Clients: func(string) (kubernetes.Interface, error) {
			enteredOnce.Do(func() { close(entered) })
			<-release
			return cs, nil
		},
		Policy: testPolicy(), Owners: owners,
	})
	staleResult := make(chan error, 1)
	go func() {
		_, err := staleService.materialize(context.Background(), oldRecord)
		staleResult <- err
	}()
	<-entered // the old record passed its owner check and is paused before RBAC writes

	owners.generation = 8
	owners.resourceVersion = "owner-rv-8"
	newRequest := agentRequest()
	newRequest.Rules = nil
	newRequest.ExpectedOwnerGeneration = 8
	newRequest.ExpectedOwnerResourceVersion = "owner-rv-8"
	if _, err := service.Ensure(context.Background(), newRequest, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject"); err != nil {
		t.Fatalf("narrowed Ensure: %v", err)
	}
	roleName := serviceaccounts.WorkloadIdentityRoleName(oldToken.ServiceAccount)
	role, err := cs.RbacV1().ClusterRoles().Get(context.Background(), roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get narrowed ClusterRole: %v", err)
	}
	if len(role.Rules) != 0 {
		t.Fatalf("new owner revision did not narrow the ClusterRole: %#v", role.Rules)
	}
	if _, err := service.materializeForRevision(context.Background(), oldRecord, &OwnerRevision{Generation: 7, ResourceVersion: "owner-rv-7"}); err == nil {
		t.Fatal("old request was given a token from the superseding identity record")
	} else {
		var conflict RevisionConflict
		if !errors.As(err, &conflict) || conflict.Code != CodeStaleOwner {
			t.Fatalf("superseded request error = %v, want stale_owner", err)
		}
	}

	close(release)
	if err := <-staleResult; err == nil {
		t.Fatal("stale materialization unexpectedly succeeded")
	} else {
		var conflict RevisionConflict
		if !errors.As(err, &conflict) || conflict.Code != CodeVersionConflict {
			t.Fatalf("stale materialization error = %v, want version_conflict", err)
		}
	}
	role, err = cs.RbacV1().ClusterRoles().Get(context.Background(), roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get final ClusterRole: %v", err)
	}
	if len(role.Rules) != 0 {
		t.Fatalf("stale materialization restored revoked rules: %#v", role.Rules)
	}
}

func TestRequestOwnerRevisionRequiresBothFields(t *testing.T) {
	for _, req := range []Request{
		{ExpectedOwnerGeneration: 7},
		{ExpectedOwnerResourceVersion: "opaque-rv"},
		{ExpectedOwnerGeneration: -1, ExpectedOwnerResourceVersion: "opaque-rv"},
	} {
		if _, err := requestOwnerRevision(req); err == nil {
			t.Fatalf("request revision %#v was accepted without a valid pair", req)
		}
	}
}

func TestEnsureCapsTheTokenLifetime(t *testing.T) {
	service, records, _, _ := testService(t)
	req := agentRequest()
	req.TTLSeconds = int64((72 * time.Hour) / time.Second)
	token, err := service.Ensure(context.Background(), req, tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := time.Until(token.ExpiresAt); got > serviceaccounts.ScopedIdentityMaxTokenTTL+time.Minute {
		t.Fatalf("token lifetime %s exceeds the 24h ceiling", got)
	}
	record := records.items[token.Name]
	if record.Spec.TTLSeconds != int64(serviceaccounts.ScopedIdentityMaxTokenTTL/time.Second) {
		t.Fatalf("recorded TTL = %ds, want the clamped ceiling", record.Spec.TTLSeconds)
	}
}

func TestEnsureNeverPersistsTheToken(t *testing.T) {
	service, records, cs, _ := testService(t)
	ctx := context.Background()
	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	record := records.items[token.Name]
	for key, value := range record.Spec.Annotations {
		if strings.Contains(value, token.Token) {
			t.Fatalf("the token leaked into record annotation %q", key)
		}
	}
	secrets, err := cs.CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing secrets: %v", err)
	}
	if len(secrets.Items) != 0 {
		t.Fatalf("the minter wrote %d Secrets; tokens are TokenRequest-only", len(secrets.Items))
	}
}

func TestReleaseRemovesTheAccountAndTheRecord(t *testing.T) {
	service, records, cs, _ := testService(t)
	ctx := context.Background()
	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := service.ReleaseByName(ctx, "agents", token.Name); err != nil {
		t.Fatalf("ReleaseByName: %v", err)
	}
	if len(records.items) != 0 {
		t.Fatalf("record survived release: %v", records.items)
	}
	assertIdentityGone(t, ctx, cs, token.ServiceAccount)
	// Idempotent.
	if err := service.ReleaseByName(ctx, "agents", token.Name); err != nil {
		t.Fatalf("second ReleaseByName: %v", err)
	}
}

func TestReleaseByNameIgnoresAnotherProvidersRecord(t *testing.T) {
	service, records, _, _ := testService(t)
	ctx := context.Background()
	token, err := service.Ensure(ctx, agentRequest(), tenancyv1alpha1.ScopedIdentityAttestationProvider, "subject")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := service.ReleaseByName(ctx, "edges", token.Name); err != nil {
		t.Fatalf("ReleaseByName: %v", err)
	}
	if len(records.items) != 1 {
		t.Fatal("one provider deleted another provider's identity")
	}
}

func assertIdentityGone(t *testing.T, ctx context.Context, cs kubernetes.Interface, account string) {
	t.Helper()
	if _, err := cs.CoreV1().ServiceAccounts("default").Get(ctx, account, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ServiceAccount survived: %v", err)
	}
	role := serviceaccounts.WorkloadIdentityRoleName(account)
	if _, err := cs.RbacV1().ClusterRoles().Get(ctx, role, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ClusterRole survived: %v", err)
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, role, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ClusterRoleBinding survived: %v", err)
	}
}
