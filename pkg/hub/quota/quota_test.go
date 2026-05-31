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

package quota

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

func TestEffectiveOrgsPerUser(t *testing.T) {
	cases := []struct {
		name string
		user *tenancyv1alpha1.User
		want int32
	}{
		{"nil user uses default", nil, DefaultOrgsPerUser},
		{"zero override uses default", &tenancyv1alpha1.User{Spec: tenancyv1alpha1.UserSpec{OrgQuota: 0}}, DefaultOrgsPerUser},
		{"override of 1 wins", &tenancyv1alpha1.User{Spec: tenancyv1alpha1.UserSpec{OrgQuota: 1}}, 1},
		{"override of 25 wins", &tenancyv1alpha1.User{Spec: tenancyv1alpha1.UserSpec{OrgQuota: 25}}, 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveOrgsPerUser(tc.user); got != tc.want {
				t.Errorf("EffectiveOrgsPerUser: got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEffectiveWorkspacesPerOrg(t *testing.T) {
	cases := []struct {
		name string
		org  *tenancyv1alpha1.Organization
		want int32
	}{
		{"nil org uses default", nil, DefaultWorkspacesPerOrg},
		{"zero override uses default", &tenancyv1alpha1.Organization{Spec: tenancyv1alpha1.OrganizationSpec{WorkspaceQuota: 0}}, DefaultWorkspacesPerOrg},
		{"override of 5 wins", &tenancyv1alpha1.Organization{Spec: tenancyv1alpha1.OrganizationSpec{WorkspaceQuota: 5}}, 5},
		{"override of 200 wins", &tenancyv1alpha1.Organization{Spec: tenancyv1alpha1.OrganizationSpec{WorkspaceQuota: 200}}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveWorkspacesPerOrg(tc.org); got != tc.want {
				t.Errorf("EffectiveWorkspacesPerOrg: got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCheckOrgQuota(t *testing.T) {
	user := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
	}
	overrideUser := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "bob"},
		Spec:       tenancyv1alpha1.UserSpec{OrgQuota: 3},
	}

	t.Run("under default cap permits create", func(t *testing.T) {
		err := CheckOrgQuota(context.Background(), user, CounterFunc(func(_ context.Context) (int32, error) { return 9, nil }))
		if err != nil {
			t.Errorf("under cap: got error %v, want nil", err)
		}
	})
	t.Run("at default cap rejects with quota-exceeded", func(t *testing.T) {
		err := CheckOrgQuota(context.Background(), user, CounterFunc(func(_ context.Context) (int32, error) { return DefaultOrgsPerUser, nil }))
		var qe *QuotaExceededError
		if !errors.As(err, &qe) {
			t.Fatalf("at cap: got error %v, want *QuotaExceededError", err)
		}
		if qe.Kind != "Organization" || qe.Owner != "alice" || qe.Count != DefaultOrgsPerUser || qe.Cap != DefaultOrgsPerUser {
			t.Errorf("error fields: %#v", qe)
		}
		if !strings.Contains(qe.Error(), "Organization") || !strings.Contains(qe.Error(), `"alice"`) {
			t.Errorf("error message: %q", qe.Error())
		}
	})
	t.Run("at admin override rejects", func(t *testing.T) {
		err := CheckOrgQuota(context.Background(), overrideUser, CounterFunc(func(_ context.Context) (int32, error) { return 3, nil }))
		var qe *QuotaExceededError
		if !errors.As(err, &qe) {
			t.Fatalf("override at cap: got error %v, want *QuotaExceededError", err)
		}
		if qe.Cap != 3 {
			t.Errorf("override cap: got %d, want 3", qe.Cap)
		}
	})
	t.Run("counter error propagates", func(t *testing.T) {
		boom := errors.New("kcp unreachable")
		err := CheckOrgQuota(context.Background(), user, CounterFunc(func(_ context.Context) (int32, error) { return 0, boom }))
		if !errors.Is(err, boom) {
			t.Errorf("counter error: got %v, want wrapping %v", err, boom)
		}
		var qe *QuotaExceededError
		if errors.As(err, &qe) {
			t.Errorf("counter error should not be classified as QuotaExceeded")
		}
	})
	t.Run("nil counter rejected", func(t *testing.T) {
		err := CheckOrgQuota(context.Background(), user, nil)
		if err == nil {
			t.Error("nil counter: expected error, got nil")
		}
	})
	t.Run("nil user uses default cap", func(t *testing.T) {
		err := CheckOrgQuota(context.Background(), nil, CounterFunc(func(_ context.Context) (int32, error) { return DefaultOrgsPerUser, nil }))
		var qe *QuotaExceededError
		if !errors.As(err, &qe) {
			t.Fatalf("nil user at cap: got %v, want *QuotaExceededError", err)
		}
		if qe.Owner != "" {
			t.Errorf("nil user owner: got %q, want empty", qe.Owner)
		}
	})
}

func TestCheckWorkspaceQuota(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "7f3a-acme"},
	}

	t.Run("under default cap permits create", func(t *testing.T) {
		err := CheckWorkspaceQuota(context.Background(), org, CounterFunc(func(_ context.Context) (int32, error) { return 0, nil }))
		if err != nil {
			t.Errorf("under cap: got %v, want nil", err)
		}
	})
	t.Run("at default cap rejects", func(t *testing.T) {
		err := CheckWorkspaceQuota(context.Background(), org, CounterFunc(func(_ context.Context) (int32, error) { return DefaultWorkspacesPerOrg, nil }))
		var qe *QuotaExceededError
		if !errors.As(err, &qe) {
			t.Fatalf("at cap: got %v, want *QuotaExceededError", err)
		}
		if qe.Kind != "Workspace" || qe.Owner != "7f3a-acme" {
			t.Errorf("error fields: %#v", qe)
		}
	})
	t.Run("counter error propagates", func(t *testing.T) {
		boom := errors.New("listing failed")
		err := CheckWorkspaceQuota(context.Background(), org, CounterFunc(func(_ context.Context) (int32, error) { return 0, boom }))
		if !errors.Is(err, boom) {
			t.Errorf("counter error: got %v, want wrapping %v", err, boom)
		}
	})
	t.Run("nil counter rejected", func(t *testing.T) {
		if err := CheckWorkspaceQuota(context.Background(), org, nil); err == nil {
			t.Error("expected nil-counter error")
		}
	})
}

func TestQuotaExceededError_AsTarget(t *testing.T) {
	// Demonstrates the intended usage pattern: handlers use errors.As
	// to switch on the structured fields. Guards against future
	// refactors that might change the error's pointer receiver.
	err := error(&QuotaExceededError{Kind: "Organization", Owner: "alice", Count: 10, Cap: 10})
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Fatal("errors.As failed on a literal QuotaExceededError")
	}
	if qe.Kind != "Organization" || qe.Cap != 10 {
		t.Errorf("As-target fields: %#v", qe)
	}
}
