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
	"sort"
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		parts := strings.SplitN(term, "=", 2)
		if len(parts) != 2 || labels[parts[0]] != parts[1] {
			return false
		}
	}
	return true
}

func (m *memoryRecords) Create(_ context.Context, obj *tenancyv1alpha1.ScopedIdentity, _ metav1.CreateOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	if _, ok := m.items[obj.Name]; ok {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "scopedidentities"}, obj.Name)
	}
	m.creates++
	copied := obj.DeepCopy()
	copied.Generation = 1
	m.items[obj.Name] = copied
	return copied.DeepCopy(), nil
}

func (m *memoryRecords) Update(_ context.Context, obj *tenancyv1alpha1.ScopedIdentity, _ metav1.UpdateOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	m.updates++
	copied := obj.DeepCopy()
	copied.Generation++
	m.items[obj.Name] = copied
	return copied.DeepCopy(), nil
}

func (m *memoryRecords) UpdateStatus(_ context.Context, obj *tenancyv1alpha1.ScopedIdentity, _ metav1.UpdateOptions) (*tenancyv1alpha1.ScopedIdentity, error) {
	existing, ok := m.items[obj.Name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "scopedidentities"}, obj.Name)
	}
	existing.Status = *obj.Status.DeepCopy()
	return existing.DeepCopy(), nil
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
	present map[string]string // "<kind>/<name>" -> uid
	err     error
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
	owners := &fakeOwners{present: map[string]string{"Agent/scheduler": "agent-uid-1"}}
	service := New(Options{
		Records: records,
		Clients: func(string) (kubernetes.Interface, error) { return cs, nil },
		Policy:  testPolicy(),
		Owners:  owners,
	})
	return service, records, cs, owners
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
