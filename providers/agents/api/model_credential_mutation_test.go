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

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-agents/tenant/tenanttest"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestModelCredentialReadIsRawAndRuntimeResolutionRequiresUse(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{true: "use allowed", false: "use denied"}[allowed], func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := agentsv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			credential := &agentsv1alpha1.ModelCredential{
				TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential"},
				ObjectMeta: metav1.ObjectMeta{Name: "bound-model"},
				Spec:       agentsv1alpha1.ModelCredentialSpec{ConnectionRef: "account", Model: "gpt-6-luna"},
			}
			connection := &agentsv1alpha1.ModelProviderConnection{
				TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection"},
				ObjectMeta: metav1.ObjectMeta{Name: "account"},
				Spec: agentsv1alpha1.ModelProviderConnectionSpec{
					Provider:  agentsv1alpha1.ModelProviderOpenAICompatible,
					BaseURL:   llm.DefaultBaseURL,
					SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: "shared-key"},
					SecretKey: "token",
				},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "shared-key", Namespace: llm.SecretNamespace},
				Data:       map[string][]byte{"token": []byte("shared-secret")},
			}
			d := dynamicfake.NewSimpleDynamicClient(scheme, credential, connection, secret)
			var checkedUse bool
			d.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				object := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
				var review authorizationv1.SelfSubjectAccessReview
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &review); err != nil {
					t.Fatal(err)
				}
				attrs := review.Spec.ResourceAttributes
				if attrs == nil || attrs.Verb != "use" || attrs.Resource != "modelproviderconnections" || attrs.Name != "account" {
					t.Fatalf("wrong provider connection permission check: %+v", attrs)
				}
				checkedUse = true
				return true, &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "authorization.k8s.io/v1",
					"kind":       "SelfSubjectAccessReview",
					"status":     map[string]any{"allowed": allowed},
				}}, nil
			})
			c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d))

			stored, err := c.GetModelCredential(t.Context(), "bound-model")
			if err != nil {
				t.Fatal(err)
			}
			if stored.Spec.ConnectionRef != "account" || stored.Spec.Provider != "" || stored.Spec.SecretRef.Name != "" {
				t.Fatalf("stored read hydrated connection data: %+v", stored.Spec)
			}
			for _, action := range d.Actions() {
				if action.GetResource().Resource == "selfsubjectaccessreviews" || action.GetResource().Resource == "modelproviderconnections" || action.GetResource().Resource == "secrets" {
					t.Fatalf("stored read touched connection authorization or credentials: %s %s", action.GetVerb(), action.GetResource())
				}
			}

			profile, err := llm.LoadCredential(t.Context(), c, "bound-model")
			if allowed {
				if err != nil {
					t.Fatal(err)
				}
				if !checkedUse || profile.Provider != agentsv1alpha1.ModelProviderOpenAICompatible || profile.BaseURL != llm.DefaultBaseURL || profile.Model != "gpt-6-luna" || profile.APIKey != "shared-secret" {
					t.Fatalf("runtime profile was not resolved through the authorized connection: checkedUse=%v profile=%+v", checkedUse, profile)
				}

				s := &Server{store: store.NewMemoryStore()}
				scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
				if err := s.store.PutSessionModelSettings(t.Context(), scope, "chat", store.SessionModelSettings{
					ModelCredential:        "bound-model",
					ReasoningEffortByModel: map[string]string{"bound-model": "low"},
				}); err != nil {
					t.Fatal(err)
				}
				secretReads := countCredentialActions(d.Actions(), "get", "secrets")
				run := taskRun{Creds: c, HubToken: "caller-token", Scope: scope, Agent: &agentsv1alpha1.Agent{}, SessionID: "chat", Trigger: agentsv1alpha1.RunTriggerChat}
				if err := s.resolveChatModelSettings(t.Context(), &run); err != nil {
					t.Fatalf("resolve session reasoning for a connection binding: %v", err)
				}
				if run.ModelSettings == nil || run.ModelSettings.ReasoningEffort != "low" || run.ModelSettings.Transport != llm.TransportResponses {
					t.Fatalf("session did not pin connection model reasoning/transport: %+v", run.ModelSettings)
				}
				if got := countCredentialActions(d.Actions(), "get", "secrets"); got != secretReads {
					t.Fatalf("session metadata read a Secret: reads before=%d after=%d", secretReads, got)
				}
			} else {
				if !apierrors.IsForbidden(err) {
					t.Fatalf("runtime resolution without connection use returned %v, want forbidden", err)
				}
				for _, action := range d.Actions() {
					if action.GetVerb() == "get" && (action.GetResource().Resource == "modelproviderconnections" || action.GetResource().Resource == "secrets") {
						t.Fatalf("denied runtime resolution read protected data: %s %s", action.GetVerb(), action.GetResource())
					}
				}
			}
		})
	}
}

func TestDeleteModelBindingPreservesSharedConnectionSecret(t *testing.T) {
	ws, c, requests := modelCredentialWorkspace(t,
		modelCredentialBindingObject("bound-model", "account", "gpt-6-luna"),
		modelProviderConnectionObject("account", "shared-key"),
		modelCredentialSecretObject("shared-key", map[string]string{"token": "key bytes", "other": "retained"}),
	)
	secretBefore := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, "shared-key")
	connectionBefore := ws.Get(agentsclient.ModelProviderConnectionGVR, "", "account")

	if err := deleteCredential(t.Context(), c, "bound-model"); err != nil {
		t.Fatal(err)
	}
	if got := ws.Get(agentsclient.ModelCredentialGVR, "", "bound-model"); got != nil {
		t.Fatalf("binding remains after delete: %#v", got.Object)
	}
	secretAfter := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, "shared-key")
	connectionAfter := ws.Get(agentsclient.ModelProviderConnectionGVR, "", "account")
	if !sameUnstructuredJSON(secretBefore, secretAfter) {
		t.Fatalf("shared Secret changed after deleting one binding:\nbefore=%#v\nafter=%#v", secretBefore.Object, secretAfter.Object)
	}
	if !reflect.DeepEqual(connectionBefore.Object["spec"], connectionAfter.Object["spec"]) {
		t.Fatalf("parent connection spec changed: before=%#v after=%#v", connectionBefore.Object["spec"], connectionAfter.Object["spec"])
	}
	assertNoCredentialSecretRequests(t, *requests)
}

func TestDeleteMissingModelCredentialLeavesOrphanKeyAlone(t *testing.T) {
	ws, c, requests := modelCredentialWorkspace(t,
		modelCredentialSecretObject(CredentialSecretName("old-model"), map[string]string{"apiKey": "orphan-but-not-owned"}),
	)
	secretBefore := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, CredentialSecretName("old-model"))
	if err := deleteCredential(t.Context(), c, "old-model"); err != nil {
		t.Fatal(err)
	}
	secretAfter := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, CredentialSecretName("old-model"))
	if !sameUnstructuredJSON(secretBefore, secretAfter) {
		t.Fatalf("idempotent delete removed an orphan Secret: before=%#v after=%#v", secretBefore.Object, secretAfter.Object)
	}
	assertNoCredentialSecretRequests(t, *requests)
	assertNoModelCredentialDelete(t, *requests)
}

func TestDeleteModelBindingUsesUIDAndResourceVersionPreconditions(t *testing.T) {
	for _, tt := range []struct {
		name            string
		uid             string
		resourceVersion string
		wantError       bool
	}{
		{name: "complete identity", uid: "binding-uid", resourceVersion: "17"},
		{name: "missing UID", resourceVersion: "17", wantError: true},
		{name: "missing resourceVersion", uid: "binding-uid", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := agentsv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			binding := &agentsv1alpha1.ModelCredential{
				TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential"},
				ObjectMeta: metav1.ObjectMeta{Name: "bound-model", UID: types.UID(tt.uid), ResourceVersion: tt.resourceVersion},
				Spec:       agentsv1alpha1.ModelCredentialSpec{ConnectionRef: "account", Model: "gpt-6-luna"},
			}
			d := dynamicfake.NewSimpleDynamicClient(scheme, binding)
			var deleteCalled bool
			d.PrependReactor("delete", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
				deleteCalled = true
				if tt.wantError {
					t.Fatal("binding without a complete identity was deleted")
				}
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != types.UID(tt.uid) || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != tt.resourceVersion {
					t.Fatalf("binding delete preconditions = %+v", options.Preconditions)
				}
				return false, nil, nil
			})
			c := agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d))
			err := deleteCredential(t.Context(), c, "bound-model")
			if tt.wantError {
				if err == nil {
					t.Fatal("binding delete without UID/resourceVersion succeeded")
				}
				if deleteCalled {
					t.Fatal("binding delete without UID/resourceVersion issued DELETE")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !deleteCalled {
				t.Fatal("binding delete was not issued")
			}
		})
	}
}

func TestApplyCredentialUpsertModelBindingChangesOnlyModel(t *testing.T) {
	ws, c, requests := modelCredentialWorkspace(t,
		modelCredentialBindingObject("bound-model", "account", "gpt-6-luna"),
		modelProviderConnectionObject("account", "shared-key"),
		modelCredentialSecretObject("shared-key", map[string]string{"token": "key bytes", "other": "retained"}),
	)
	before := ws.Get(agentsclient.ModelCredentialGVR, "", "bound-model")
	secretBefore := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, "shared-key")

	result, err := applyCredentialUpsert(t.Context(), c, &modelCredential{Name: "bound-model", Model: "gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "bound-model" || result.Model != "gpt-6-astra" {
		t.Fatalf("updated binding summary = %+v", result)
	}
	after := ws.Get(agentsclient.ModelCredentialGVR, "", "bound-model")
	var stored agentsv1alpha1.ModelCredential
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(after.Object, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.ConnectionRef != "account" || stored.Spec.Model != "gpt-6-astra" {
		t.Fatalf("binding was not updated model-only: %+v", stored.Spec)
	}
	if stored.UID != before.GetUID() || stored.ResourceVersion == before.GetResourceVersion() {
		t.Fatalf("binding identity/resourceVersion was not retained/advanced: before=%s/%s after=%s/%s", before.GetUID(), before.GetResourceVersion(), stored.UID, stored.ResourceVersion)
	}
	secretAfter := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, "shared-key")
	if !sameUnstructuredJSON(secretBefore, secretAfter) {
		t.Fatalf("model-only binding update changed the shared Secret: before=%#v after=%#v", secretBefore.Object, secretAfter.Object)
	}
	assertNoCredentialSecretRequests(t, *requests)
}

func TestApplyCredentialUpsertRejectsConnectionEditsBeforeSecretIO(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*modelCredential)
	}{
		{name: "provider", change: func(req *modelCredential) { req.Provider = "openai" }},
		{name: "base URL", change: func(req *modelCredential) { req.BaseURL = "https://other.example/v1" }},
		{name: "API key", change: func(req *modelCredential) { req.APIKey = "new-key" }},
		{name: "workspace ID", change: func(req *modelCredential) { req.AnthropicWorkspaceID = "wrkspc_team" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ws, c, requests := modelCredentialWorkspace(t,
				modelCredentialBindingObject("bound-model", "account", "gpt-6-luna"),
				modelProviderConnectionObject("account", "shared-key"),
				modelCredentialSecretObject("shared-key", map[string]string{"token": "key bytes"}),
			)
			before := ws.Get(agentsclient.ModelCredentialGVR, "", "bound-model")
			req := &modelCredential{Name: "bound-model", Model: "gpt-6-astra"}
			tt.change(req)
			if _, err := applyCredentialUpsert(t.Context(), c, req); err == nil {
				t.Fatal("connection-owned edit was accepted")
			}
			after := ws.Get(agentsclient.ModelCredentialGVR, "", "bound-model")
			if !sameUnstructuredJSON(before, after) {
				t.Fatalf("rejected edit changed binding: before=%#v after=%#v", before.Object, after.Object)
			}
			assertNoCredentialSecretRequests(t, *requests)
		})
	}
}

func modelCredentialWorkspace(t *testing.T, objects ...*unstructured.Unstructured) (*tenanttest.Server, *agentsclient.Client, *[]tenanttest.Request) {
	t.Helper()
	ws := tenanttest.New()
	requests := []tenanttest.Request{}
	ws.Intercept = func(req tenanttest.Request) *metav1.Status {
		requests = append(requests, req)
		return nil
	}
	ws.Add(objects...)
	return ws, workspaceClient(t, ws), &requests
}

func modelCredentialBindingObject(name, connectionRef, model string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(),
		"kind":       "ModelCredential",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"connectionRef": connectionRef, "model": model},
	}}
}

func modelProviderConnectionObject(name, secretName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(),
		"kind":       "ModelProviderConnection",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"provider": "openai-compatible", "baseURL": "https://provider.example/v1",
			"secretRef": map[string]any{"name": secretName}, "secretKey": "token",
		},
	}}
}

func modelCredentialSecretObject(name string, values map[string]string) *unstructured.Unstructured {
	data := map[string]any{}
	for key, value := range values {
		data[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": llm.SecretNamespace, "labels": map[string]any{"railgrid.ai/owner": agentsclient.ProviderName}},
		"type":     "Opaque", "stringData": data,
	}}
}

func assertNoCredentialSecretRequests(t *testing.T, requests []tenanttest.Request) {
	t.Helper()
	for _, req := range requests {
		if req.GVR == agentsclient.SecretGVR {
			t.Fatalf("operation unexpectedly accessed Secret data: %s %s/%s", req.Verb, req.Namespace, req.Name)
		}
	}
}

func assertNoModelCredentialDelete(t *testing.T, requests []tenanttest.Request) {
	t.Helper()
	for _, req := range requests {
		if req.Verb == "delete" && req.GVR == agentsclient.ModelCredentialGVR {
			t.Fatalf("unexpected ModelCredential DELETE after a NotFound read")
		}
	}
}

func countCredentialActions(actions []ktesting.Action, verb, resource string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func sameUnstructuredJSON(a, b *unstructured.Unstructured) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, err := json.Marshal(a.Object)
	if err != nil {
		panic(fmt.Sprintf("marshal expected test object: %v", err))
	}
	right, err := json.Marshal(b.Object)
	if err != nil {
		panic(fmt.Sprintf("marshal actual test object: %v", err))
	}
	return bytes.Equal(left, right)
}

func TestLegacyDeletePersistsProtectionBeforeRetry(t *testing.T) {
	object := modelProviderConnectionObject("legacy", CredentialSecretName("legacy"))
	object.SetKind("ModelCredential")
	ws, c, requests := modelCredentialWorkspace(t, object)
	original := ws.Intercept
	attempts := 0
	ws.Intercept = func(req tenanttest.Request) *metav1.Status {
		original(req)
		if req.GVR == agentsclient.ModelCredentialGVR && req.Verb == "delete" {
			attempts++
			current := ws.Get(agentsclient.ModelCredentialGVR, "", "legacy")
			protected := false
			for _, f := range current.GetFinalizers() {
				if f == agentsv1alpha1.ModelCredentialSecretProtectionFinalizer {
					protected = true
				}
			}
			if !protected {
				t.Fatal("DELETE before Secret cleanup protection persisted")
			}
			if attempts == 1 {
				return &metav1.Status{Status: "Failure", Reason: metav1.StatusReasonInternalError, Code: 500, Message: "transient delete failure"}
			}
		}
		return nil
	}
	if err := deleteCredential(t.Context(), c, "legacy"); err == nil {
		t.Fatal("expected first DELETE failure")
	}
	if err := deleteCredential(t.Context(), c, "legacy"); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	assertNoCredentialSecretRequests(t, *requests)
	if attempts != 2 {
		t.Fatalf("delete attempts=%d", attempts)
	}
}
