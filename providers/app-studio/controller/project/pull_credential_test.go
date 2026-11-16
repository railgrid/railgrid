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

package project

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	"github.com/railgrid/provider-app-studio/internal/pullcredential"
	"github.com/railgrid/provider-sdk/claimscope"
	"github.com/railgrid/provider-sdk/dataplane"
)

const (
	testCluster      = "tenantclusterid1"
	testInstance     = "demo-prod"
	testSourceSecret = "demo-prod-pull-credential"
	testDockerConfig = `{"auths":{"ghcr.io":{"auth":"dTpw","password":"p","username":"u"}}}`
)

// fakeProviders stands in for the Code and infrastructure data planes as App
// Studio reaches them: through its own export virtual workspace, as itself.
type fakeProviders struct {
	t      *testing.T
	server *httptest.Server

	mu         sync.Mutex
	handOvers  []map[string]any
	mints      int
	failHandOv bool
	mintExpiry time.Time
}

func newFakeProviders(t *testing.T) *fakeProviders {
	f := &fakeProviders{t: t, mintExpiry: time.Now().Add(time.Hour)}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeProviders) serve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input map[string]any `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode %s: %v", r.URL.Path, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/connections/github/"+pullcredential.MintVerb):
		f.mints++
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"registry": "ghcr.io", "username": "u", "token": "rotated",
			"expiresAt": f.mintExpiry.UTC().Format(time.RFC3339), "scoped": true,
		}})
	case strings.HasSuffix(r.URL.Path, "/instances/"+testInstance+"/"+pullcredential.HandOverVerb):
		if !strings.Contains(r.URL.Path, "/apis/infrastructure.railgrid.ai/v1alpha1/") || !strings.Contains(r.URL.Path, "/clusters/"+testCluster+"/") {
			f.t.Errorf("hand-over addressed %s", r.URL.Path)
		}
		f.handOvers = append(f.handOvers, body.Input)
		if f.failHandOv {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "secret_write_failed", "message": "could not store the pull credential", "retryable": true}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"secretRef": map[string]any{"name": testInstance + "-image-pull"}}})
	default:
		f.t.Errorf("unexpected call %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeProviders) ExportVerbURL(_ context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error) {
	return dataplane.SubresourceURL(f.server.URL, gvr.Group, gvr.Version, r)
}

func (f *fakeProviders) ProviderHTTPClient() (*http.Client, error) { return f.server.Client(), nil }

func (f *fakeProviders) counts() (mints, handOvers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints, len(f.handOvers)
}

func pullFixture(expiresAt string) (*aiv1alpha1.Project, aiv1alpha1.ProjectProviderBindingSpec, client.Client) {
	p := &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: aiv1alpha1.ProjectSpec{
			Template:   &aiv1alpha1.ProjectTemplateSpec{Name: "application"},
			Repository: &aiv1alpha1.ProjectRepositoryBinding{ConnectionRef: "github", RepositoryRef: "demo"},
		},
	}
	b := binding(projectProductionBindingNameForTest)
	b.ImagePullSecretRef = &aiv1alpha1.LocalSecretReference{Name: testSourceSecret}

	source := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": testSourceSecret, "namespace": pullcredential.Namespace,
			"labels": map[string]any{claimscope.OwnerLabel: "app-studio"},
		},
		"type": "kubernetes.io/dockerconfigjson",
		"data": map[string]any{pullcredential.DockerConfigKey: base64.StdEncoding.EncodeToString([]byte(testDockerConfig))},
	}}
	// Promote recorded the Connection it authorized, and the Project owns it.
	annotations := map[string]string{
		pullcredential.ConnectionAnnotation:    "github",
		pullcredential.ConnectionUIDAnnotation: "conn-uid",
	}
	if expiresAt != "" {
		annotations[pullcredential.ExpiresAtAnnotation] = expiresAt
	}
	source.SetAnnotations(annotations)
	source.SetOwnerReferences([]metav1.OwnerReference{testProjectOwner})
	conn := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "code.railgrid.ai/v1alpha1", "kind": "Connection",
		"metadata": map[string]any{"name": "github", "uid": "conn-uid"},
	}}
	inst := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": b.ResourceRef.APIVersion, "kind": b.ResourceRef.Kind,
		"metadata": map[string]any{"name": testInstance, "uid": "inst-uid"},
		"spec":     map[string]any{"template": "application", "values": map[string]any{}},
	}}
	objects := []client.Object{source, conn, inst}
	return p, b, fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithObjects(objects...).Build()
}

const projectProductionBindingNameForTest = "prod"

var testProjectOwner = metav1.OwnerReference{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project", Name: "demo", UID: "project-uid"}

func getSource(t *testing.T, c client.Client) *unstructured.Unstructured {
	t.Helper()
	source := &unstructured.Unstructured{}
	source.SetAPIVersion("v1")
	source.SetKind("Secret")
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: pullcredential.Namespace, Name: testSourceSecret}, source); err != nil {
		t.Fatal(err)
	}
	return source
}

// missingSecret is SecretsBridged=False as infrastructure's bridge reports
// a missing Secret, for the reference named in message.
func missingSecret(inst *unstructured.Unstructured, message string) {
	_ = unstructured.SetNestedSlice(inst.Object, []any{map[string]any{
		"type": "SecretsBridged", "status": "False", "reason": "SecretRefNotFound", "message": message,
	}}, "status", "conditions")
}

func getInstance(t *testing.T, c client.Client, b aiv1alpha1.ProjectProviderBindingSpec) *unstructured.Unstructured {
	t.Helper()
	inst := &unstructured.Unstructured{}
	inst.SetAPIVersion(b.ResourceRef.APIVersion)
	inst.SetKind(b.ResourceRef.Kind)
	if err := c.Get(context.Background(), client.ObjectKey{Name: testInstance}, inst); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	return inst
}

// The regression: the credential reaches the Instance as a Secret
// infrastructure can read, named by the reference infrastructure returned,
// and an unchanged credential is not handed over twice.
func TestEnsurePullCredentialHandsOverOnce(t *testing.T) {
	providers := newFakeProviders(t)
	p, b, c := pullFixture("")
	r := &Reconciler{Callers: providers}

	got := r.ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b))
	if got.problem != "" || got.retry || got.refreshIn != 0 {
		t.Fatalf("first pass = %+v, want clean, no refresh", got)
	}
	inst := getInstance(t, c, b)
	if ref, _, _ := unstructured.NestedString(inst.Object, "spec", "imagePullSecretRef", "name"); ref != testInstance+"-image-pull" {
		t.Fatalf("spec.imagePullSecretRef = %q, want the name infrastructure returned", ref)
	}
	if inst.GetAnnotations()[pullcredential.DigestAnnotation] != pullcredential.Digest(testDockerConfig) {
		t.Fatalf("digest annotation = %q", inst.GetAnnotations()[pullcredential.DigestAnnotation])
	}
	if strings.Contains(inst.GetAnnotations()[pullcredential.DigestAnnotation], "dTpw") {
		t.Fatal("the Instance carries the credential")
	}
	providers.mu.Lock()
	handed := providers.handOvers[0]["dockerConfigJSON"]
	providers.mu.Unlock()
	if handed != testDockerConfig {
		t.Fatalf("handed over %v, want the source Secret's dockerconfigjson", handed)
	}

	// Converged: nothing to do.
	if got := r.ensurePullCredential(context.Background(), c, c, testCluster, p, b, inst); got.problem != "" || got.retry {
		t.Fatalf("second pass = %+v", got)
	}
	if _, n := providers.counts(); n != 1 {
		t.Fatalf("hand-overs = %d, want 1", n)
	}

	// A missing BYO OIDC Secret carries the same reason, but no hand-over
	// fixes it, so nothing is handed over.
	missingSecret(inst, "spec.oidcBridgeSecretRef names Secret default/demo-oidc, which does not exist")
	if got := r.ensurePullCredential(context.Background(), c, c, testCluster, p, b, inst); got.problem != "" {
		t.Fatalf("OIDC-missing pass = %+v", got)
	}
	if _, n := providers.counts(); n != 1 {
		t.Fatalf("hand-overs = %d, want 1: a missing OIDC Secret is not the pull Secret", n)
	}

	// Infrastructure reports the pull Secret gone: hand it over again.
	missingSecret(inst, "spec.imagePullSecretRef names Secret default/"+testInstance+"-image-pull, which does not exist")
	if got := r.ensurePullCredential(context.Background(), c, c, testCluster, p, b, inst); got.problem != "" {
		t.Fatalf("re-hand-over pass = %+v", got)
	}
	if _, n := providers.counts(); n != 2 {
		t.Fatalf("hand-overs = %d, want 2 after the Secret went missing", n)
	}
}

// Renewal mints from the Connection promote authorized, never from the
// Project's current connectionRef: the reconciler mints as the provider, so
// following an edited connectionRef would mint from a Connection the editor
// may not be allowed to read.
func TestEnsurePullCredentialRenewsFromThePromotedConnection(t *testing.T) {
	providers := newFakeProviders(t)
	p, b, c := pullFixture(time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339))
	p.Spec.Repository.ConnectionRef = "someone-elses" // edited after promote
	r := &Reconciler{Callers: providers}

	if got := r.ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b)); got.problem != "" || got.retry {
		t.Fatalf("renewal pass = %+v", got)
	}
	// The fake serves only connections/github; any other mint fails the test.
	if mints, _ := providers.counts(); mints != 1 {
		t.Fatalf("mints = %d, want 1 from the promoted connection", mints)
	}
	source := getSource(t, c)
	if name, uid := pullcredential.SourceConnection(source); name != "github" || uid != "conn-uid" {
		t.Fatalf("renewed Secret records connection %q/%q, want the promoted one", name, uid)
	}
	if owners := source.GetOwnerReferences(); len(owners) != 1 || owners[0].UID != testProjectOwner.UID {
		t.Fatalf("renewal dropped the Project owner: %+v", owners)
	}
}

// A Connection deleted and recreated under the promoted name is a different
// Connection: renewal stops and asks for a promotion, which re-authorizes.
func TestEnsurePullCredentialRefusesAReplacedConnection(t *testing.T) {
	providers := newFakeProviders(t)
	p, b, c := pullFixture(time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339))
	conn := &unstructured.Unstructured{}
	conn.SetAPIVersion("code.railgrid.ai/v1alpha1")
	conn.SetKind("Connection")
	if err := c.Get(context.Background(), client.ObjectKey{Name: "github"}, conn); err != nil {
		t.Fatal(err)
	}
	conn.SetUID("recreated-uid")
	if err := c.Update(context.Background(), conn); err != nil {
		t.Fatal(err)
	}

	got := (&Reconciler{Callers: providers}).ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b))
	if !strings.Contains(got.problem, "was replaced") || !strings.Contains(got.problem, "promote again") {
		t.Fatalf("result = %+v, want a replaced-connection problem", got)
	}
	if mints, _ := providers.counts(); mints != 0 {
		t.Fatalf("mints = %d, want none from a replaced connection", mints)
	}
}

// A credential close to expiry is re-minted from the Connection, recorded,
// and handed over, and the reconcile is paced to the next refresh.
func TestEnsurePullCredentialRenewsBeforeExpiry(t *testing.T) {
	providers := newFakeProviders(t)
	p, b, c := pullFixture(time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339))
	r := &Reconciler{Callers: providers}

	got := r.ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b))
	if got.problem != "" || got.retry {
		t.Fatalf("renewal pass = %+v", got)
	}
	if mints, handOvers := providers.counts(); mints != 1 || handOvers != 1 {
		t.Fatalf("mints=%d handOvers=%d, want 1/1", mints, handOvers)
	}
	if got.refreshIn < 40*time.Minute || got.refreshIn > 46*time.Minute {
		t.Fatalf("refreshIn = %s, want about an hour less the refresh margin", got.refreshIn)
	}
	source := getSource(t, c)
	doc, err := pullcredential.SourceDockerConfig(source)
	if err != nil || !strings.Contains(doc, `"password":"rotated"`) {
		t.Fatalf("source Secret was not rewritten with the fresh token: %q (%v)", doc, err)
	}
	if source.GetLabels()[claimscope.OwnerLabel] != "app-studio" {
		t.Fatalf("source Secret lost its owner label: %v", source.GetLabels())
	}
	providers.mu.Lock()
	handed := providers.handOvers[0]
	providers.mu.Unlock()
	if !strings.Contains(handed["dockerConfigJSON"].(string), "rotated") || handed["expiresAt"] == "" {
		t.Fatalf("handed over %v, want the fresh token and its expiry", handed)
	}
}

func TestEnsurePullCredentialReportsProblems(t *testing.T) {
	t.Run("hand-over refused", func(t *testing.T) {
		providers := newFakeProviders(t)
		providers.failHandOv = true
		p, b, c := pullFixture("")
		got := (&Reconciler{Callers: providers}).ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b))
		if !got.retry || !strings.Contains(got.problem, "secret_write_failed") {
			t.Fatalf("result = %+v, want a retried, reported failure", got)
		}
		if ref, _, _ := unstructured.NestedString(getInstance(t, c, b).Object, "spec", "imagePullSecretRef", "name"); ref != "" {
			t.Fatalf("instance names %q after a failed hand-over", ref)
		}
	})
	t.Run("source Secret missing", func(t *testing.T) {
		providers := newFakeProviders(t)
		p, b, c := pullFixture("")
		b.ImagePullSecretRef.Name = "gone"
		got := (&Reconciler{Callers: providers}).ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b))
		if got.retry || !strings.Contains(got.problem, "promote again") {
			t.Fatalf("result = %+v, want a reported, non-retried problem", got)
		}
		if _, n := providers.counts(); n != 0 {
			t.Fatal("handed over with no source")
		}
	})
	t.Run("no binding ref", func(t *testing.T) {
		providers := newFakeProviders(t)
		p, b, c := pullFixture("")
		b.ImagePullSecretRef = nil
		if got := (&Reconciler{Callers: providers}).ensurePullCredential(context.Background(), c, c, testCluster, p, b, getInstance(t, c, b)); got != (pullCredentialResult{}) {
			t.Fatalf("result = %+v, want nothing to do", got)
		}
	})
}

// ensureInstance keeps the reference the hand-over wrote while the binding
// asks for a credential, and clears it once the binding stops asking.
func TestEnsureInstanceKeepsHandedOverPullSecretRef(t *testing.T) {
	p, b, c := pullFixture("")
	b.ResourceRef.Name = testInstance
	b.Values = runtime.RawExtension{Raw: []byte(`{"name":"` + testInstance + `"}`)}
	inst := getInstance(t, c, b)
	_ = unstructured.SetNestedField(inst.Object, testInstance+"-image-pull", "spec", "imagePullSecretRef", "name")
	if err := c.Update(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	inst.SetLabels(map[string]string{bindings.ProjectLabel: p.Name})

	if _, err := (&Reconciler{}).ensureInstance(context.Background(), c, p, b); err != nil {
		t.Fatalf("ensureInstance: %v", err)
	}
	if ref, _, _ := unstructured.NestedString(getInstance(t, c, b).Object, "spec", "imagePullSecretRef", "name"); ref != testInstance+"-image-pull" {
		t.Fatalf("ref = %q, want the handed-over name kept", ref)
	}

	b.ImagePullSecretRef = nil
	if _, err := (&Reconciler{}).ensureInstance(context.Background(), c, p, b); err != nil {
		t.Fatalf("ensureInstance: %v", err)
	}
	if _, found, _ := unstructured.NestedMap(getInstance(t, c, b).Object, "spec", "imagePullSecretRef"); found {
		t.Fatal("ref kept after the binding stopped asking for a credential")
	}
}
