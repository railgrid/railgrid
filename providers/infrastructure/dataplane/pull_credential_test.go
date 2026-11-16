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

package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/claimscope"
	sdk "github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

const testDockerConfig = `{"auths":{"ghcr.io":{"username":"u","password":"p","auth":"dTpw"}}}`

// foreignProvider is App Studio's ServiceAccount as kcp stamps it when it
// calls the verb through its own export virtual workspace: a ServiceAccount
// whose home cluster is not the addressed one.
var foreignProvider = sdk.ProxiedIdentity{
	User:   "system:serviceaccount:default:provider",
	Groups: []string{"system:serviceaccounts"},
	Extra:  map[string][]string{sdk.ClusterNameExtra: {"appstudioproviderws"}},
}

func pullCredentialRequest(t *testing.T, identity sdk.ProxiedIdentity, input any) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, dataplaneURL(infrav1alpha1.ImagePullCredentialVerb), strings.NewReader(string(body)))
	ctx := sdk.WithProxiedIdentity(r.Context(), identity)
	route, err := sdk.ParseSubresourceRequest(r)
	if err != nil {
		t.Fatalf("parse route: %v", err)
	}
	return r.WithContext(sdk.WithRoute(ctx, route))
}

func pullCredentialCallers(allowUpdate bool) *conformance.FakeCallers {
	callers := runnerCallers()
	callers.Allow = func(a conformance.Attributes) bool {
		if a.Group != instancesGVR.Group || a.Resource != instancesGVR.Resource || a.Subresource != "" {
			return false
		}
		return a.Verb == "get" || (allowUpdate && a.Verb == "update")
	}
	return callers
}

func storedPullSecret(t *testing.T, callers *conformance.FakeCallers) *unstructured.Unstructured {
	t.Helper()
	provider, err := callers.AsProvider(testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := provider.Resource(tenantSecretsGVR).Namespace(infrav1alpha1.CredentialsNamespace).
		Get(context.Background(), infrav1alpha1.ImagePullCredentialSecretName(testNamespace), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pull Secret not written: %v", err)
	}
	return secret
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (PullCredentialResult, string) {
	t.Helper()
	var env struct {
		Result PullCredentialResult `json:"result"`
		Error  *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an envelope: %v: %s", err, rec.Body.String())
	}
	if env.Error != nil {
		return env.Result, env.Error.Code
	}
	return env.Result, ""
}

// A foreign provider's call is authorized by its accepted claim. The Secret
// is written under THIS provider's owner label, which is the whole point: the
// caller could not have written it.
func TestPullCredentialForeignProviderWritesOwnedSecret(t *testing.T) {
	callers := pullCredentialCallers(false)
	h := newTestHandler(t, callers, &fakeRuntime{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pullCredentialRequest(t, foreignProvider, map[string]any{
		"dockerConfigJSON": testDockerConfig,
		"expiresAt":        "2026-11-15T23:00:00Z",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	result, code := decodeEnvelope(t, rec)
	if code != "" {
		t.Fatalf("error code %q", code)
	}
	want := infrav1alpha1.ImagePullCredentialSecretName(testNamespace)
	if result.SecretRef.Name != want {
		t.Fatalf("secretRef = %q, want %q", result.SecretRef.Name, want)
	}

	secret := storedPullSecret(t, callers)
	if got := secret.GetLabels()[claimscope.OwnerLabel]; got != "infrastructure" {
		t.Errorf("owner label = %q, want infrastructure", got)
	}
	if typ, _, _ := unstructured.NestedString(secret.Object, "type"); typ != "kubernetes.io/dockerconfigjson" {
		t.Errorf("type = %q", typ)
	}
	if got, _, _ := unstructured.NestedString(secret.Object, "stringData", dockerConfigKey); got != testDockerConfig {
		t.Errorf("dockerconfigjson = %q", got)
	}
	owners := secret.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Instance" || owners[0].Name != testNamespace {
		t.Errorf("ownerReferences = %+v, want the Instance", owners)
	}
	if got := secret.GetAnnotations()[pullCredentialExpiresAnnotation]; got != "2026-11-15T23:00:00Z" {
		t.Errorf("expiry annotation = %q", got)
	}

	// A second hand-over (a rotated token) replaces the Secret in place.
	rotated := strings.Replace(testDockerConfig, `"p"`, `"p2"`, 1)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, pullCredentialRequest(t, foreignProvider, map[string]any{"dockerConfigJSON": rotated}))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotation status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got, _, _ := unstructured.NestedString(storedPullSecret(t, callers).Object, "stringData", dockerConfigKey); got != rotated {
		t.Errorf("rotated dockerconfigjson = %q", got)
	}
}

// A tenant caller must be able to update the Instance: storing the
// credential a workload runs with changes the Instance.
func TestPullCredentialTenantNeedsUpdate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allowUpdate bool
		wantStatus  int
	}{
		{"update granted", true, http.StatusOK},
		{"get only", false, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t, pullCredentialCallers(tc.allowUpdate), &fakeRuntime{})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, pullCredentialRequest(t, testCaller, map[string]any{"dockerConfigJSON": testDockerConfig}))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestPullCredentialRejectsBadInput(t *testing.T) {
	for name, input := range map[string]any{
		"empty":         map[string]any{"dockerConfigJSON": ""},
		"not json":      map[string]any{"dockerConfigJSON": "nope"},
		"no auths":      map[string]any{"dockerConfigJSON": `{"auths":{}}`},
		"bad expiry":    map[string]any{"dockerConfigJSON": testDockerConfig, "expiresAt": "tomorrow"},
		"unknown field": map[string]any{"dockerConfigJSON": testDockerConfig, "registry": "ghcr.io"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTestHandler(t, pullCredentialCallers(false), &fakeRuntime{})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, pullCredentialRequest(t, foreignProvider, input))
			if _, code := decodeEnvelope(t, rec); code != "invalid_input" {
				t.Fatalf("code = %q (status %d), want invalid_input", code, rec.Code)
			}
			if strings.Contains(rec.Body.String(), `"p"`) {
				t.Fatalf("error echoed the credential: %s", rec.Body.String())
			}
		})
	}
}

// A component address is not a pull-credential address.
func TestPullCredentialRefusesComponent(t *testing.T) {
	h := newTestHandler(t, pullCredentialCallers(true), &fakeRuntime{})
	r := httptest.NewRequest(http.MethodPost, componentURL(testWorkspace, testNamespace, "web", infrav1alpha1.ImagePullCredentialVerb), strings.NewReader(`{"input":{}}`))
	ctx := sdk.WithProxiedIdentity(r.Context(), testCaller)
	route, err := sdk.ParseSubresourceRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r.WithContext(sdk.WithRoute(ctx, route)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
