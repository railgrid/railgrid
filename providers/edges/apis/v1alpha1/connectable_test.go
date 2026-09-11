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

package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestMacOSServerGVRAndSchemeRegistration(t *testing.T) {
	if got, want := MacOSServerGVR.Resource, MacOSServerResource; got != want {
		t.Fatalf("MacOSServerGVR resource = %q, want %q", got, want)
	}
	if got, want := MacOSServerGVR.GroupVersion(), SchemeGroupVersion; got != want {
		t.Fatalf("MacOSServerGVR group/version = %q, want %q", got, want)
	}

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	obj, err := scheme.New(SchemeGroupVersion.WithKind("MacOSServer"))
	if err != nil {
		t.Fatalf("scheme.New(MacOSServer): %v", err)
	}
	if _, ok := obj.(*MacOSServer); !ok {
		t.Fatalf("scheme.New(MacOSServer) = %T, want *MacOSServer", obj)
	}
	if got := NewMacOSServer(); got == nil {
		t.Fatal("NewMacOSServer returned nil")
	}
}
