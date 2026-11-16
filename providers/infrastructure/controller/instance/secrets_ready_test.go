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

package instance

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// A pull Secret that could not be bridged holds Ready at False even while the
// runtime still reports Ready: the pods cannot pull their private images, and
// a tenant must not see production as serving.
func TestMirrorStatusSecretsNotBridgedIsNotReady(t *testing.T) {
	instance := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"generation": int64(2)},
		"spec":     map[string]any{"values": map[string]any{}},
	}}
	runtimeObj := runtimeForNetwork(2, 2, infrav1alpha1.RailgridNetworkPhaseRuntime, "True")
	runtimeObj.Object["status"].(map[string]any)["state"] = "ACTIVE"
	tmpl := &infrav1alpha1.Template{Spec: infrav1alpha1.TemplateSpec{Backend: infrav1alpha1.BackendKRO}}
	bridged := secretsBridgedCondition(metav1.ConditionFalse, infrav1alpha1.ReasonSecretRefNotFound,
		"spec.imagePullSecretRef names Secret default/app-image-pull, which does not exist")

	ready, err := (&Controller{}).mirrorStatus(context.Background(), mirroredStatusClient{}, instance, tmpl, runtimeObj,
		validCondition(metav1.ConditionTrue, infrav1alpha1.ReasonReady, ""), bridged)
	if err != nil {
		t.Fatalf("mirrorStatus: %v", err)
	}
	if ready {
		t.Fatal("mirrorStatus reported Ready with an unbridged pull Secret")
	}
	conds, _, _ := unstructured.NestedSlice(instance.Object, "status", "conditions")
	var readyCond map[string]any
	for _, raw := range conds {
		if m, ok := raw.(map[string]any); ok && m["type"] == "Ready" {
			readyCond = m
		}
	}
	if readyCond == nil || readyCond["status"] != "False" || readyCond["reason"] != infrav1alpha1.ReasonSecretRefNotFound {
		t.Fatalf("Ready condition = %#v, want False/%s", readyCond, infrav1alpha1.ReasonSecretRefNotFound)
	}
	if phase, _, _ := unstructured.NestedString(instance.Object, "status", "phase"); phase == "Ready" {
		t.Fatalf("phase = %q, want not Ready", phase)
	}

	// Bridged again: the runtime's own Ready stands.
	ready, err = (&Controller{}).mirrorStatus(context.Background(), mirroredStatusClient{}, instance, tmpl, runtimeObj,
		validCondition(metav1.ConditionTrue, infrav1alpha1.ReasonReady, ""),
		secretsBridgedCondition(metav1.ConditionTrue, infrav1alpha1.ReasonSecretsBridged, ""))
	if err != nil {
		t.Fatalf("mirrorStatus: %v", err)
	}
	if !ready {
		t.Fatal("mirrorStatus stayed not-Ready after the Secret was bridged")
	}
}

// App Studio re-hands a pull credential over only when the missing Secret is
// the pull one, and tells it from a missing OIDC bridge Secret (same reason)
// by this message prefix. Changing either message breaks that.
func TestMissingSecretMessagesNameTheirReference(t *testing.T) {
	c := &Controller{cfg: Config{CredentialsNamespace: "default"}}
	empty := clientfake.NewClientBuilder().Build()
	for name, bridge := range map[string]struct {
		call   func() (*conditionSpec, error)
		prefix string
	}{
		"pull": {func() (*conditionSpec, error) {
			return c.bridgeRegistryPullSecret(context.Background(), empty, "tenant", "default", "app", "app-image-pull")
		}, "spec.imagePullSecretRef "},
		"oidc": {func() (*conditionSpec, error) {
			return c.bridgeBYOSecret(context.Background(), empty, "tenant", "default", "app", "app-oidc")
		}, "spec.oidcBridgeSecretRef "},
	} {
		cond, err := bridge.call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cond == nil || cond.reason != infrav1alpha1.ReasonSecretRefNotFound || !strings.HasPrefix(cond.message, bridge.prefix) {
			t.Fatalf("%s: condition = %+v, want %s with a message starting %q", name, cond, infrav1alpha1.ReasonSecretRefNotFound, bridge.prefix)
		}
	}
}
