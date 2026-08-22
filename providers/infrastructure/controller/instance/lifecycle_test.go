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

package instance

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func TestLifecycleDueHardExpiryAndIdleActivity(t *testing.T) {
	created := metav1.NewTime(time.Unix(100, 0))
	dev := &infrav1alpha1.TemplateDevelopment{MaxLifetimeSeconds: 12, IdleTimeoutSeconds: 3}
	if reason, due := lifecycleDue(created.Add(12*time.Second), created, dev, nil); reason != "SandboxExpired" || !due {
		t.Fatalf("hard expiry = %q/%v", reason, due)
	}
	runtimeObj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"annotations": map[string]any{
		infrav1alpha1.FarosLastActivityAnnotation: created.Add(2 * time.Second).Format(time.RFC3339Nano),
	}}}}
	if reason, due := lifecycleDue(created.Add(6*time.Second), created, dev, runtimeObj); reason != "SandboxIdle" || !due {
		t.Fatalf("idle expiry = %q/%v", reason, due)
	}
	if reason, due := lifecycleDue(created.Add(3*time.Second), created, dev, runtimeObj); due || reason != "" {
		t.Fatalf("recent activity should keep sandbox alive: %q/%v", reason, due)
	}
}
