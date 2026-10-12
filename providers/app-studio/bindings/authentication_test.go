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
package bindings

import (
	"reflect"
	"testing"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAuthenticationOverlayScopesAndDisablesWithoutChangingSharing(t *testing.T) {
	p := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "owned-project"}}
	b := aiv1alpha1.ProjectProviderBindingSpec{Provider: "app-studio", Kind: aiv1alpha1.ProjectBindingKindProviderResource, ResourceRef: &aiv1alpha1.ProjectProviderResourceReference{Name: "demo-dev"}, Values: runtime.RawExtension{Raw: []byte(`{"access":"private","railgridAuth":true,"railgridAuthAppID":"spoofed","railgridAuthEnvironment":"spoofed","railgridAuthInstanceUID":"forged-instance"}`)}}
	p.Spec.Authentication.Enabled = true
	for _, env := range []string{"development", "production"} {
		got, err := ApplyAuthentication(p, env, b)
		if err != nil {
			t.Fatal(err)
		}
		vals, err := Values(got)
		if err != nil {
			t.Fatal(err)
		}
		if _, forged := vals["railgridAuthInstanceUID"]; forged {
			t.Fatal("binding overrode provider instance identity")
		}
		if vals[AuthAppIDField] != "owned-project/"+env || vals[AuthEnvironmentField] != env || vals[AuthEnabledField] != true || vals["access"] != "private" {
			t.Fatalf("unexpected overlay: %#v", vals)
		}
	}
	p.Spec.Authentication.Enabled = false
	got, err := ApplyAuthentication(p, "development", b)
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := Values(got)
	if vals[AuthEnabledField] != false {
		t.Fatalf("disabled auth stayed enabled: %#v", vals)
	}
	legacy := b
	legacy.Values.Raw = []byte(`{"access":"public"}`)
	got, err = ApplyAuthentication(p, "development", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, legacy) {
		t.Fatal("disabled legacy binding was changed")
	}
}
