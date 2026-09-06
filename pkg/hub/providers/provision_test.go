/*
Copyright 2026 The Faros Authors.

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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// newRBACDynamicFake is a dynamic client that serves the two RBAC kinds
// ensureProviderRoleBinding writes, so the binding logic — including the
// delete-and-recreate a RoleRef change forces — runs for real.
func newRBACDynamicFake() dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			clusterRoleGVR:        "ClusterRoleList",
			clusterRoleBindingGVR: "ClusterRoleBindingList",
		},
	)
}

// ===== the generated faros:provider role =====

// rulesFor collects every rule covering (group, resource). More than one is
// normal: apiexports carries an ordinary CRUD rule and a separate `bind` rule.
func rulesFor(group, resource string) []map[string]any {
	var out []map[string]any
	for _, raw := range providerClusterRoleRules() {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		groups, _ := rule["apiGroups"].([]any)
		resources, _ := rule["resources"].([]any)
		if containsAny(groups, group) && containsAny(resources, resource) {
			out = append(out, rule)
		}
	}
	return out
}

// grants reports whether the role permits verb on (group, resource) through
// any of its rules, and returns the verbs it does permit for the failure
// message.
func grants(group, resource, verb string) (bool, []string) {
	var seen []string
	for _, rule := range rulesFor(group, resource) {
		seen = append(seen, ruleVerbs(rule)...)
		if hasVerb(rule, verb) {
			return true, seen
		}
	}
	return false, seen
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if s, _ := v.(string); s == want {
			return true
		}
	}
	return false
}

func ruleVerbs(rule map[string]any) []string {
	raw, _ := rule["verbs"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasVerb(rule map[string]any, verb string) bool {
	for _, v := range ruleVerbs(rule) {
		if v == verb || v == "*" {
			return true
		}
	}
	return false
}

// Everything a provider's own `init` and runtime do inside its workspace has to
// be covered, or narrowing the role turns into an outage the first time a chart
// is installed. Each row names the caller so a future reader can check it
// against the code rather than against this list.
func TestProviderClusterRoleCoversWhatProvidersActuallyDo(t *testing.T) {
	for _, tc := range []struct {
		group, resource, verb, why string
	}{
		{"apis.kcp.io", "apiresourceschemas", "create", "install.ApplySchemasFromDir"},
		{"apis.kcp.io", "apiexports", "update", "install.ApplyAPIExport on upgrade"},
		{"apis.kcp.io", "apiexportendpointslices", "delete", "install.EnsureAPIExportEndpointSlice recreates on a path change"},
		{"apis.kcp.io", "apibindings", "list", "the apiexport multicluster provider enumerates bound clusters"},
		{"apis.kcp.io", "apiexports/content", "get", "the APIExport VW authorizer SARs the in-flight verb"},
		{"apis.kcp.io", "apiexports", "bind", "install.ApplyBindGrant writes the tenant bind ClusterRole"},
		{"cache.kcp.io", "cachedresources", "create", "the infrastructure provider's virtual storage"},
		{"core.kcp.io", "logicalclusters", "get", "install.workspacePathOf, the org-owned check"},
		{"providers.faros.sh", "catalogentries", "update", "install.ApplyCatalogEntry self-registration"},
		{"providers.faros.sh", "catalogentries/status", "patch", "the provider reports its own status"},
		{"rbac.authorization.k8s.io", "clusterrolebindings", "delete", "install.removeBindGrant for org-owned workspaces"},
		{"apiextensions.k8s.io", "customresourcedefinitions", "create", "per-template CRDs"},
		{"", "secrets", "create", "runtime identity minting"},
		{"", "serviceaccounts", "create", "runtime identity minting"},
		{"coordination.k8s.io", "leases", "update", "provider-sdk leader election"},
		{"authentication.k8s.io", "tokenreviews", "create", "providers that authenticate their own callers"},
		{"authorization.k8s.io", "subjectaccessreviews", "create", "providers that authorize their own callers"},
	} {
		if ok, verbs := grants(tc.group, tc.resource, tc.verb); !ok {
			t.Errorf("%s/%s lacks verb %q (needed by %s); grants %v", tc.group, tc.resource, tc.verb, tc.why, verbs)
		}
	}

	// The APIExport VW passes the request's own verb into its SAR, discovery
	// included, so this one rule cannot be an enumerated verb list.
	content := rulesFor("apis.kcp.io", "apiexports/content")
	if len(content) != 1 {
		t.Fatalf("apiexports/content rules = %d, want exactly 1", len(content))
	}
	if got := ruleVerbs(content[0]); len(got) != 1 || got[0] != "*" {
		t.Errorf("apiexports/content verbs = %v, want [*]", got)
	}

	// Discovery: every client-go built from the provider kubeconfig does it
	// before it can construct a single informer.
	var discovery bool
	for _, raw := range providerClusterRoleRules() {
		rule, _ := raw.(map[string]any)
		if urls, ok := rule["nonResourceURLs"].([]any); ok && containsAny(urls, "/apis") {
			discovery = true
		}
	}
	if !discovery {
		t.Error("no nonResourceURLs rule granting discovery")
	}
}

// The point of the narrower role is that a provider cannot climb back out of
// it. escalate/impersonate/bind-on-clusterroles would each let it do exactly
// that, and tenancy writes would let it delete the workspace it lives in.
func TestProviderClusterRoleWithholdsEscalationPaths(t *testing.T) {
	for _, raw := range providerClusterRoleRules() {
		rule, _ := raw.(map[string]any)
		groups, _ := rule["apiGroups"].([]any)
		resources, _ := rule["resources"].([]any)
		if containsAny(groups, "*") || containsAny(resources, "*") {
			t.Errorf("wildcard rule defeats the narrowing: %v", rule)
		}
		if containsAny(groups, "tenancy.kcp.io") {
			t.Errorf("provider role must not reach tenancy.kcp.io: %v", rule)
		}
		if containsAny(groups, "rbac.authorization.k8s.io") {
			for _, verb := range ruleVerbs(rule) {
				switch verb {
				case "escalate", "bind", "impersonate", "*":
					t.Errorf("provider role grants %q on RBAC, which re-opens cluster-admin: %v", verb, rule)
				}
			}
		}
		for _, verb := range ruleVerbs(rule) {
			if verb == "impersonate" {
				t.Errorf("provider role grants impersonate: %v", rule)
			}
		}
	}
}

// ===== the --provider-workspace-cluster-admin flag =====

// The flag decides one thing: which ClusterRole the provider SA is bound to in
// its own workspace. Default keeps cluster-admin so this release changes
// nothing for an operator who does not opt in.
func TestEnsureProviderRoleBindingHonoursTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clusterAdmin bool
		wantRole     string
		wantRole1    bool
	}{
		{"default keeps cluster-admin", true, "cluster-admin", false},
		{"opting in binds the generated role", false, ProviderClusterRoleName, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := newRBACDynamicFake()
			p := NewProvisioner(nil, WithWorkspaceClusterAdmin(tc.clusterAdmin))
			if err := p.ensureProviderRoleBinding(context.Background(), cl); err != nil {
				t.Fatalf("ensureProviderRoleBinding: %v", err)
			}
			crb, err := cl.Resource(clusterRoleBindingGVR).Get(context.Background(), providerSABindingName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting binding: %v", err)
			}
			if got, _, _ := unstructured.NestedString(crb.Object, "roleRef", "name"); got != tc.wantRole {
				t.Fatalf("roleRef = %q, want %q", got, tc.wantRole)
			}
			_, err = cl.Resource(clusterRoleGVR).Get(context.Background(), ProviderClusterRoleName, metav1.GetOptions{})
			if got := err == nil; got != tc.wantRole1 {
				t.Fatalf("ClusterRole %s present = %v, want %v", ProviderClusterRoleName, got, tc.wantRole1)
			}
		})
	}
}

// RoleRef is immutable, so narrowing an EXISTING provider means the hub has to
// delete the cluster-admin binding and recreate it. A hub that only tried to
// update would leave every already-onboarded provider on cluster-admin — the
// exact set the change is for.
func TestEnsureProviderRoleBindingReplacesAnExistingClusterAdminBinding(t *testing.T) {
	ctx := context.Background()
	cl := newRBACDynamicFake()

	wide := NewProvisioner(nil, WithWorkspaceClusterAdmin(true))
	if err := wide.ensureProviderRoleBinding(ctx, cl); err != nil {
		t.Fatalf("initial binding: %v", err)
	}

	narrow := NewProvisioner(nil, WithWorkspaceClusterAdmin(false))
	if err := narrow.ensureProviderRoleBinding(ctx, cl); err != nil {
		t.Fatalf("narrowing: %v", err)
	}
	crb, err := cl.Resource(clusterRoleBindingGVR).Get(ctx, providerSABindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting binding: %v", err)
	}
	if got, _, _ := unstructured.NestedString(crb.Object, "roleRef", "name"); got != ProviderClusterRoleName {
		t.Fatalf("roleRef after narrowing = %q, want %q", got, ProviderClusterRoleName)
	}
	// One binding, under the historical name: a second one under a new name
	// would leave the cluster-admin grant in force.
	list, err := cl.Resource(clusterRoleBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing bindings: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("bindings = %d, want exactly 1", len(list.Items))
	}

	// And back again, for the operator who staged the change and rolled it
	// back.
	if err := wide.ensureProviderRoleBinding(ctx, cl); err != nil {
		t.Fatalf("rolling back: %v", err)
	}
	crb, err = cl.Resource(clusterRoleBindingGVR).Get(ctx, providerSABindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting binding: %v", err)
	}
	if got, _, _ := unstructured.NestedString(crb.Object, "roleRef", "name"); got != "cluster-admin" {
		t.Fatalf("roleRef after rollback = %q, want cluster-admin", got)
	}
}

// ===== rotation =====

// providerSA is the ServiceAccount every provider workspace holds.
func providerSA(annotations map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: ProviderSAName, Namespace: ProviderSANamespace, Annotations: annotations,
	}}
}

func tokenSecret(name string, annotations map[string]string, token string) *corev1.Secret {
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[corev1.ServiceAccountNameKey] = ProviderSAName
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ProviderSANamespace, Annotations: annotations},
		Type:       corev1.SecretTypeServiceAccountToken,
		Data:       map[string][]byte{corev1.ServiceAccountTokenKey: []byte(token)},
	}
}

// tokenPopulatingClientset stands in for kcp's token controller: it fills in
// the token of any service-account-token Secret as it is created, which is what
// ensureLegacySAToken polls for.
func tokenPopulatingClientset(objects ...runtime.Object) *fake.Clientset {
	cs := fake.NewClientset(objects...)
	cs.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		secret, ok := action.(k8stesting.CreateAction).GetObject().(*corev1.Secret)
		if !ok || secret.Type != corev1.SecretTypeServiceAccountToken {
			return false, nil, nil
		}
		out := secret.DeepCopy()
		out.Data = map[string][]byte{corev1.ServiceAccountTokenKey: []byte("token-for-" + secret.Name)}
		return false, nil, cs.Tracker().Add(out)
	})
	return cs
}

// A rotation must produce a NEW token, point the hub at it, and give the old
// one a deadline rather than deleting it — the provider is still using it.
func TestRotateMintsANewTokenAndRetiresThePreviousOne(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	cs := tokenPopulatingClientset(
		providerSA(nil),
		tokenSecret(ProviderTokenSecretName, nil, "original-token"),
	)
	p := &Provisioner{clock: func() time.Time { return now }}

	previous, err := activeTokenSecretName(ctx, cs)
	if err != nil {
		t.Fatalf("active secret: %v", err)
	}
	if previous != ProviderTokenSecretName {
		t.Fatalf("active secret before rotation = %q, want %q", previous, ProviderTokenSecretName)
	}

	rotated, token, err := p.rotateToken(ctx, cs)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	next := rotated.SecretName
	if next == ProviderTokenSecretName {
		t.Fatal("rotation reused the original Secret name")
	}
	if token == "original-token" || token == "" {
		t.Fatalf("rotated token = %q, want a new one", token)
	}
	if rotated.PreviousSecretName != ProviderTokenSecretName {
		t.Fatalf("PreviousSecretName = %q, want %q", rotated.PreviousSecretName, ProviderTokenSecretName)
	}
	if want := now.Add(DefaultCredentialGracePeriod); !rotated.PreviousValidUntil.Equal(want) {
		t.Fatalf("PreviousValidUntil = %s, want %s", rotated.PreviousValidUntil, want)
	}

	// The hub now hands out the new one.
	active, err := activeTokenSecretName(ctx, cs)
	if err != nil {
		t.Fatalf("active secret: %v", err)
	}
	if active != next {
		t.Fatalf("active secret after rotation = %q, want %q", active, next)
	}

	// The old one is still there, still valid, with a deadline.
	old, err := cs.CoreV1().Secrets(ProviderSANamespace).Get(ctx, ProviderTokenSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("previous Secret was deleted immediately: %v", err)
	}
	if string(old.Data[corev1.ServiceAccountTokenKey]) != "original-token" {
		t.Fatal("previous token was altered; providers still holding it would break")
	}
	expiry, parseErr := time.Parse(time.RFC3339, old.Annotations[AnnotationTokenSecretExpiry])
	if parseErr != nil {
		t.Fatalf("previous Secret has no parseable expiry: %v", parseErr)
	}
	if want := now.Add(DefaultCredentialGracePeriod); !expiry.Equal(want) {
		t.Fatalf("expiry = %s, want %s", expiry, want)
	}
}

// The grace period is the whole point: both Secrets are tokens for the SAME
// ServiceAccount, so anything that verifies the provider's identity — the
// heartbeat's TokenReview (PR #627) above all — resolves both to the same
// username and keeps accepting the old one while the chart is rolled forward.
// The username never depends on which Secret the token came from.
func TestBothCredentialsBelongToTheSameServiceAccountDuringTheGracePeriod(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	cs := tokenPopulatingClientset(
		providerSA(nil),
		tokenSecret(ProviderTokenSecretName, nil, "original-token"),
	)
	p := &Provisioner{clock: func() time.Time { return now }}
	rotated, _, err := p.rotateToken(ctx, cs)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	next := rotated.SecretName

	// The identity a TokenReview reports is derived from the Secret's
	// service-account-name annotation, not from the Secret's name, so both
	// tokens authenticate as system:serviceaccount:default:provider.
	const wantUsername = "system:serviceaccount:" + ProviderSANamespace + ":" + ProviderSAName
	for _, name := range []string{ProviderTokenSecretName, next} {
		secret, err := cs.CoreV1().Secrets(ProviderSANamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting %s: %v", name, err)
		}
		sa := secret.Annotations[corev1.ServiceAccountNameKey]
		if sa != ProviderSAName {
			t.Fatalf("Secret %s is bound to service account %q, want %q — its token would authenticate as a different identity", name, sa, ProviderSAName)
		}
		if got := "system:serviceaccount:" + secret.Namespace + ":" + sa; got != wantUsername {
			t.Fatalf("Secret %s resolves to username %q, want %q", name, got, wantUsername)
		}
		if len(secret.Data[corev1.ServiceAccountTokenKey]) == 0 {
			t.Fatalf("Secret %s carries no token; it would stop authenticating", name)
		}
	}
}

// Sweeping is what ends the old credential. Before the deadline it must leave
// everything alone — deleting early is an outage for a provider mid-rollout.
func TestSweepDeletesOnlyExpiredCredentials(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute).Format(time.RFC3339)
	pending := now.Add(2 * time.Hour).Format(time.RFC3339)

	cs := fake.NewClientset(
		providerSA(map[string]string{AnnotationActiveTokenSecret: "provider-token-live"}),
		tokenSecret("provider-token-live", nil, "live"),
		tokenSecret("provider-token-old", map[string]string{AnnotationTokenSecretExpiry: expired}, "old"),
		tokenSecret("provider-token-recent", map[string]string{AnnotationTokenSecretExpiry: pending}, "recent"),
		// Never annotated: the credential a workspace that has not rotated
		// still holds, or one whose retirement failed to stamp. Not ours to
		// delete on a guess.
		tokenSecret("provider-token", nil, "unannotated"),
		// Someone else's Secret in the same namespace.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "unrelated", Namespace: ProviderSANamespace,
			Annotations: map[string]string{AnnotationTokenSecretExpiry: expired},
		}, Type: corev1.SecretTypeOpaque},
	)
	p := &Provisioner{clock: func() time.Time { return now }}

	deleted, next, err := p.sweepExpiredTokens(ctx, cs)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (only the lapsed credential)", deleted)
	}
	if want, _ := time.Parse(time.RFC3339, pending); !next.Equal(want) {
		t.Fatalf("next expiry = %s, want %s", next, want)
	}
	for _, name := range []string{"provider-token-live", "provider-token-recent", "provider-token", "unrelated"} {
		if _, err := cs.CoreV1().Secrets(ProviderSANamespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Errorf("sweep deleted %s, which was not expired: %v", name, err)
		}
	}
	if _, err := cs.CoreV1().Secrets(ProviderSANamespace).Get(ctx, "provider-token-old", metav1.GetOptions{}); err == nil {
		t.Error("expired credential survived the sweep")
	}
}

// A workspace that never rotated reports the original Secret, so minting keeps
// returning the same credential it always did.
func TestActiveTokenSecretNameDefaultsToTheOriginal(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(providerSA(nil))
	got, err := activeTokenSecretName(ctx, cs)
	if err != nil {
		t.Fatalf("activeTokenSecretName: %v", err)
	}
	if got != ProviderTokenSecretName {
		t.Fatalf("active secret = %q, want %q", got, ProviderTokenSecretName)
	}
	if !strings.HasSuffix(ProviderTokenSecretName, ProviderTokenSecretSuffix) {
		t.Fatalf("ProviderTokenSecretName %q no longer matches the name the token controller populates", ProviderTokenSecretName)
	}
}
