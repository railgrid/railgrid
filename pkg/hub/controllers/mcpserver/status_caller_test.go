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

package mcpserver

import (
	"context"
	"testing"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/faroshq/faros/pkg/hub/mcpaggregate"
)

// Status discovery enumerates as the server's own ServiceAccount in the
// server's tenant, so the Org's shadowing applies and org-owned providers
// (which need a human to delegate for) are not listed.
func TestStatusCaller(t *testing.T) {
	sa := mcpaggregate.ServiceAccountUsername("default")
	cases := []struct {
		path string
		want mcpaggregate.Caller
	}{
		{path: "root:faros:tenants:org1:ws1", want: mcpaggregate.Caller{OrgUUID: "org1", WorkspaceUUID: "ws1", ServiceAccount: sa}},
		{path: "root:faros:tenants:org1", want: mcpaggregate.Caller{OrgUUID: "org1", ServiceAccount: sa}},
		// Unknown path: platform catalog only, never "every org".
		{path: "", want: mcpaggregate.Caller{ServiceAccount: sa}},
		{path: "root:faros:providers:infra", want: mcpaggregate.Caller{ServiceAccount: sa}},
	}
	for _, tc := range cases {
		got := statusCaller(tc.path, "default")
		if got != tc.want {
			t.Errorf("statusCaller(%q) = %+v, want %+v", tc.path, got, tc.want)
		}
		if !got.IsServiceAccount() {
			t.Errorf("statusCaller(%q) is not a ServiceAccount caller", tc.path)
		}
	}
}

func TestDirectClusterPath(t *testing.T) {
	kcp := kcpfake.NewSimpleClientset(&corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{
		Name:        "cluster",
		Annotations: map[string]string{"kcp.io/path": "root:faros:tenants:org1:ws1"},
	}})
	if got := directClusterPath(context.Background(), kcp); got != "root:faros:tenants:org1:ws1" {
		t.Fatalf("directClusterPath = %q", got)
	}
	if got := directClusterPath(context.Background(), kcpfake.NewSimpleClientset()); got != "" {
		t.Fatalf("directClusterPath with no LogicalCluster = %q, want empty", got)
	}
}
