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

package serviceaccounts

import (
	"context"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
)

func TestEnsureWorkloadIdentityIsDeterministicScopedAndShortLived(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()

	scope := WorkloadIdentityScope{
		TenantPath:  "root:kedge:tenants:org:workspace",
		Project:     "project",
		ProjectUID:  "project-uid",
		Environment: "development",
		Instance:    "project-dev",
		ProviderResources: []ProviderResourceScope{{
			APIVersion: "example.kedge.faros.sh/v1alpha1",
			Kind:       "Example",
			Resource:   "examples",
			Name:       "example",
		}},
	}
	var gotAudience []string
	var gotTTL int64
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		tokenAction := action.(clienttesting.CreateAction)
		request := tokenAction.GetObject().(*authnv1.TokenRequest)
		gotAudience = append([]string(nil), request.Spec.Audiences...)
		gotTTL = *request.Spec.ExpirationSeconds
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "runtime-token",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(5 * time.Minute)),
		}}, nil
	})

	first, err := m.EnsureWorkloadIdentity(context.Background(), "org", "workspace", scope)
	if err != nil {
		t.Fatalf("EnsureWorkloadIdentity: %v", err)
	}
	second, err := m.EnsureWorkloadIdentity(context.Background(), "org", "workspace", scope)
	if err != nil {
		t.Fatalf("EnsureWorkloadIdentity (repeat): %v", err)
	}
	if first.ServiceAccountName != second.ServiceAccountName || first.ServiceAccountName != WorkloadServiceAccountName(scope) {
		t.Fatalf("service account name not deterministic: first=%q second=%q", first.ServiceAccountName, second.ServiceAccountName)
	}
	if gotAudience == nil || len(gotAudience) != 1 || gotAudience[0] != WorkloadIdentityTokenAudience {
		t.Fatalf("TokenRequest audiences = %v, want [%q]", gotAudience, WorkloadIdentityTokenAudience)
	}
	if gotTTL != int64(WorkloadIdentityTokenTTL/time.Second) {
		t.Fatalf("TokenRequest TTL = %d, want %d", gotTTL, int64(WorkloadIdentityTokenTTL/time.Second))
	}

	role, err := cs.RbacV1().ClusterRoles().Get(context.Background(), WorkloadIdentityRoleName(first.ServiceAccountName), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get workload ClusterRole: %v", err)
	}
	if len(role.Rules) != 2 {
		t.Fatalf("workload ClusterRole rules = %d, want 2", len(role.Rules))
	}
	for _, rule := range role.Rules {
		if len(rule.Verbs) != 1 || rule.Verbs[0] != "get" || len(rule.ResourceNames) != 1 {
			t.Fatalf("workload ClusterRole rule is broader than GET one-name: %#v", rule)
		}
		for _, resource := range rule.Resources {
			if resource == "*" {
				t.Fatal("workload ClusterRole must not contain wildcard resources")
			}
		}
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(context.Background(), WorkloadIdentityRoleName(first.ServiceAccountName), metav1.GetOptions{}); err != nil {
		t.Fatalf("get workload ClusterRoleBinding: %v", err)
	}
	created, err := cs.CoreV1().ServiceAccounts(Namespace).Get(context.Background(), first.ServiceAccountName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get workload ServiceAccount: %v", err)
	}
	wantAnnotations := map[string]string{
		AnnotationWorkloadIdentityTenantPath:  scope.TenantPath,
		AnnotationWorkloadIdentityProject:     scope.Project,
		AnnotationWorkloadIdentityProjectUID:  scope.ProjectUID,
		AnnotationWorkloadIdentityEnvironment: scope.Environment,
		AnnotationWorkloadIdentityInstance:    scope.Instance,
		AnnotationWorkloadIdentityScope:       WorkloadIdentityScopeMarker(scope),
	}
	for key, want := range wantAnnotations {
		if got := created.Annotations[key]; got != want {
			t.Errorf("workload annotation %q = %q, want %q", key, got, want)
		}
	}
}

func TestWorkloadServiceAccountNameChangesWhenProjectUIDChanges(t *testing.T) {
	scope := WorkloadIdentityScope{TenantPath: "root:kedge:tenants:o:w", Project: "p", ProjectUID: "uid-a", Environment: "development", Instance: "p-dev"}
	other := scope
	other.ProjectUID = "uid-b"
	if WorkloadServiceAccountName(scope) == WorkloadServiceAccountName(other) {
		t.Fatal("project UID must participate in workload identity name")
	}
}

func TestEnsureWorkloadIdentityRejectsTokenExpiryBeyondPolicy(t *testing.T) {
	m, cs := managerFor(t)
	defer resetTestClientset()
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "runtime-token",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(WorkloadIdentityTokenTTL + 2*time.Second)),
		}}, nil
	})
	_, err := m.EnsureWorkloadIdentity(context.Background(), "org", "workspace", WorkloadIdentityScope{
		TenantPath:  "root:kedge:tenants:org:workspace",
		Project:     "project",
		ProjectUID:  "project-uid",
		Environment: "development",
		Instance:    "project-dev",
	})
	if err == nil {
		t.Fatal("EnsureWorkloadIdentity accepted a token beyond the maximum lifetime")
	}
}
