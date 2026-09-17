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

package addonctrl

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
)

const (
	addonName = "code"
	edgeName  = "build-01"
)

func newTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := edgesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add edges scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&edgesv1alpha1.Addon{}, &edgesv1alpha1.Service{}).
		Build()
}

func testAddon(mutate ...func(*edgesv1alpha1.Addon)) *edgesv1alpha1.Addon {
	addon := &edgesv1alpha1.Addon{
		ObjectMeta: metav1.ObjectMeta{Name: addonName, UID: types.UID("addon-uid-1")},
		Spec: edgesv1alpha1.AddonSpec{
			EdgeRef: edgesv1alpha1.AddonEdgeRef{Kind: "LinuxServer", Name: edgeName},
			Type:    edgesv1alpha1.AddonTypeRunner,
			Runner:  &edgesv1alpha1.AddonRunnerSpec{Port: 8787, MaximumCapacity: 1},
		},
	}
	for _, m := range mutate {
		m(addon)
	}
	return addon
}

func allowed(addon *edgesv1alpha1.Addon) {
	apimeta.SetStatusCondition(&addon.Status.Conditions, metav1.Condition{
		Type:    edgesv1alpha1.AddonConditionAllowed,
		Status:  metav1.ConditionTrue,
		Reason:  edgesv1alpha1.AddonReasonAllowedOnEdge,
		Message: "allowed by --allow-addon on this edge",
	})
}

func tokenSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: addonName + tokenSecretSuffix, Namespace: tokenSecretNamespace},
		Data:       map[string][]byte{"token": []byte("deadbeef")},
	}
}

func linuxEdge() *edgesv1alpha1.LinuxServer {
	return &edgesv1alpha1.LinuxServer{ObjectMeta: metav1.ObjectMeta{Name: edgeName}}
}

// reconcile drives the reconciler directly against c, bypassing the
// multicluster manager (whose only job here is to hand back that client).
func reconcile(t *testing.T, c client.Client) {
	t.Helper()
	r := &Reconciler{}
	ctx := context.Background()
	addon := &edgesv1alpha1.Addon{}
	if err := c.Get(ctx, client.ObjectKey{Name: addonName}, addon); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileAddon(ctx, c, addon); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getService(t *testing.T, c client.Client) (*edgesv1alpha1.Service, bool) {
	t.Helper()
	svc := &edgesv1alpha1.Service{}
	err := c.Get(context.Background(), client.ObjectKey{Name: addonName}, svc)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return svc, true
}

func getAddon(t *testing.T, c client.Client) *edgesv1alpha1.Addon {
	t.Helper()
	addon := &edgesv1alpha1.Addon{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: addonName}, addon); err != nil {
		t.Fatal(err)
	}
	return addon
}

// TestNoServiceBeforeTheAgentAllowsIt is the load-bearing property of this
// controller. A tenant with permission to create an Addon must not be able to
// conjure a reachable endpoint on a machine whose owner never opted in: until
// an AGENT reports Allowed=True, and until it has published the add-on's token
// Secret, no Service exists.
func TestNoServiceBeforeTheAgentAllowsIt(t *testing.T) {
	// Hub-side intent only: the Addon exists, the edge exists, the agent has
	// said nothing.
	c := newTestClient(t, testAddon(), linuxEdge(), tokenSecret())
	reconcile(t, c)
	if svc, found := getService(t, c); found {
		t.Fatalf("a Service was published before the agent reported Allowed=True: %+v", svc.Spec)
	}
	addon := getAddon(t, c)
	published := apimeta.FindStatusCondition(addon.Status.Conditions, edgesv1alpha1.AddonConditionPublished)
	if published == nil || published.Status != metav1.ConditionFalse {
		t.Fatalf("Published = %+v, want False", published)
	}
	if published.Reason != edgesv1alpha1.AddonReasonNotAllowedYet {
		t.Errorf("Published reason = %q, want %q", published.Reason, edgesv1alpha1.AddonReasonNotAllowedYet)
	}
	if addon.Status.ServiceRef != nil {
		t.Errorf("serviceRef = %+v, want nil", addon.Status.ServiceRef)
	}

	// Allowed, but the agent has not published its token yet.
	c = newTestClient(t, testAddon(allowed), linuxEdge())
	reconcile(t, c)
	if _, found := getService(t, c); found {
		t.Fatal("a Service was published before the add-on's token Secret existed")
	}
	if published := apimeta.FindStatusCondition(getAddon(t, c).Status.Conditions, edgesv1alpha1.AddonConditionPublished); published == nil ||
		published.Reason != edgesv1alpha1.AddonReasonTokenSecretMissing {
		t.Errorf("Published = %+v, want reason %q", published, edgesv1alpha1.AddonReasonTokenSecretMissing)
	}
}

// TestPublishedServiceShape pins the exact Service the add-on derives: a
// loopback, generic, http proxy target authenticated with the agent-published
// token, owned by the Addon so deletion cleans it up.
func TestPublishedServiceShape(t *testing.T) {
	c := newTestClient(t, testAddon(allowed), linuxEdge(), tokenSecret())
	reconcile(t, c)

	svc, found := getService(t, c)
	if !found {
		t.Fatal("no Service was published for an allowed add-on with a token Secret")
	}
	if svc.Spec.EdgeRef.Kind != "LinuxServer" || svc.Spec.EdgeRef.Name != edgeName {
		t.Errorf("edgeRef = %+v", svc.Spec.EdgeRef)
	}
	if svc.Spec.Host != "127.0.0.1" {
		t.Errorf("host = %q, want the edge host loopback", svc.Spec.Host)
	}
	if svc.Spec.Type != edgesv1alpha1.ServiceTypeGeneric {
		t.Errorf("type = %q, want generic", svc.Spec.Type)
	}
	if svc.Spec.Scheme != edgesv1alpha1.ServiceSchemeHTTP {
		t.Errorf("scheme = %q, want http", svc.Spec.Scheme)
	}
	if svc.Spec.Port != 8787 {
		t.Errorf("port = %d, want 8787", svc.Spec.Port)
	}
	if svc.Spec.Auth != edgesv1alpha1.ServiceAuthSecret {
		t.Errorf("auth = %q, want secret", svc.Spec.Auth)
	}
	if svc.Spec.AuthSecretRef == nil ||
		svc.Spec.AuthSecretRef.Name != addonName+tokenSecretSuffix ||
		svc.Spec.AuthSecretRef.Namespace != tokenSecretNamespace {
		t.Errorf("authSecretRef = %+v", svc.Spec.AuthSecretRef)
	}
	if len(svc.OwnerReferences) != 1 {
		t.Fatalf("owner references = %+v, want exactly the Addon", svc.OwnerReferences)
	}
	owner := svc.OwnerReferences[0]
	if owner.Kind != "Addon" || owner.Name != addonName || owner.UID != types.UID("addon-uid-1") {
		t.Errorf("owner = %+v", owner)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("the Addon is not the controlling owner of its Service")
	}

	addon := getAddon(t, c)
	if addon.Status.ServiceRef == nil || addon.Status.ServiceRef.Name != addonName {
		t.Errorf("serviceRef = %+v", addon.Status.ServiceRef)
	}
	if published := apimeta.FindStatusCondition(addon.Status.Conditions, edgesv1alpha1.AddonConditionPublished); published == nil ||
		published.Status != metav1.ConditionTrue {
		t.Errorf("Published = %+v, want True", published)
	}
	// The agent's own condition must survive the provider's status update.
	if apimeta.FindStatusCondition(addon.Status.Conditions, edgesv1alpha1.AddonConditionAllowed) == nil {
		t.Error("the provider dropped the agent's Allowed condition")
	}
}

// TestPortOverrideIsCarriedIntoTheService: the Service must dial the port the
// runner was told to listen on, or the proxy reaches nothing.
func TestPortOverrideIsCarriedIntoTheService(t *testing.T) {
	addon := testAddon(allowed)
	addon.Spec.Runner.Port = 9191
	c := newTestClient(t, addon, linuxEdge(), tokenSecret())
	reconcile(t, c)

	svc, found := getService(t, c)
	if !found {
		t.Fatal("no Service was published")
	}
	if svc.Spec.Port != 9191 {
		t.Errorf("port = %d, want 9191", svc.Spec.Port)
	}
}

// TestKubernetesClusterEdgeIsBlocked: a cluster edge has no host loopback to
// proxy to and no agent-side add-on plane, so it is refused with an explanation
// instead of producing a Service that could never work.
func TestKubernetesClusterEdgeIsBlocked(t *testing.T) {
	addon := testAddon(allowed)
	addon.Spec.EdgeRef.Kind = kubernetesClusterKind
	c := newTestClient(t, addon, tokenSecret())
	reconcile(t, c)

	if _, found := getService(t, c); found {
		t.Fatal("a Service was published for a KubernetesCluster edge")
	}
	got := getAddon(t, c)
	if got.Status.Phase != edgesv1alpha1.AddonPhaseBlocked {
		t.Errorf("phase = %q, want Blocked", got.Status.Phase)
	}
	published := apimeta.FindStatusCondition(got.Status.Conditions, edgesv1alpha1.AddonConditionPublished)
	if published == nil || published.Reason != edgesv1alpha1.AddonReasonUnsupportedEdgeKind {
		t.Errorf("Published = %+v, want reason %q", published, edgesv1alpha1.AddonReasonUnsupportedEdgeKind)
	}
}

// TestMissingEdgeIsBlocked: a Service pointing at an edge that does not exist
// would sit Unreachable forever with nothing said on the Addon the user created.
func TestMissingEdgeIsBlocked(t *testing.T) {
	c := newTestClient(t, testAddon(allowed), tokenSecret())
	reconcile(t, c)

	if _, found := getService(t, c); found {
		t.Fatal("a Service was published for a non-existent edge")
	}
	published := apimeta.FindStatusCondition(getAddon(t, c).Status.Conditions, edgesv1alpha1.AddonConditionPublished)
	if published == nil || published.Reason != edgesv1alpha1.AddonReasonEdgeNotFound {
		t.Errorf("Published = %+v, want reason %q", published, edgesv1alpha1.AddonReasonEdgeNotFound)
	}
}

// TestForeignServiceIsNotHijacked: a Service that a user (or discovery) already
// owns under the same name must not be silently rewritten into a
// code-execution endpoint.
func TestForeignServiceIsNotHijacked(t *testing.T) {
	foreign := &edgesv1alpha1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: addonName},
		Spec: edgesv1alpha1.ServiceSpec{
			EdgeRef: edgesv1alpha1.ServiceEdgeRef{Kind: "LinuxServer", Name: edgeName},
			Type:    edgesv1alpha1.ServiceTypeHomeAssistant,
			Port:    8123,
		},
	}
	c := newTestClient(t, testAddon(allowed), linuxEdge(), tokenSecret(), foreign)

	r := &Reconciler{}
	addon := getAddon(t, c)
	if _, err := r.reconcileAddon(context.Background(), c, addon); err == nil {
		t.Fatal("the reconciler adopted a Service it does not own")
	}
	svc, _ := getService(t, c)
	if svc.Spec.Type != edgesv1alpha1.ServiceTypeHomeAssistant || svc.Spec.Port != 8123 {
		t.Errorf("the foreign Service was modified: %+v", svc.Spec)
	}
}
