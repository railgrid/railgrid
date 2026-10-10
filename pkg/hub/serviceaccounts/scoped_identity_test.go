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

package serviceaccounts

import (
	"context"
	"errors"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

// TestClampScopedIdentityTTLNeverAdmitsAnUnmintableLifetime pins the floor to
// what kcp's TokenRequest admission will actually mint. A below-floor TTL used
// to pass straight through to the API server, which refused it with "may not
// specify a duration less than 10 minutes" — a 502 identity_issue_failed that
// named neither the caller's TTL nor the limit.
func TestClampScopedIdentityTTLNeverAdmitsAnUnmintableLifetime(t *testing.T) {
	if ScopedIdentityMinTokenTTL < 10*time.Minute {
		t.Fatalf("ScopedIdentityMinTokenTTL = %s; kcp refuses a TokenRequest under 10m, so the hub floor must not be lower", ScopedIdentityMinTokenTTL)
	}

	for _, tc := range []struct {
		name       string
		requested  time.Duration
		defaultTTL time.Duration
		want       time.Duration
	}{
		{name: "zero takes the default", requested: 0, defaultTTL: time.Hour, want: time.Hour},
		{name: "negative takes the default", requested: -time.Minute, defaultTTL: time.Hour, want: time.Hour},
		{name: "a default under the floor is raised too", requested: 0, defaultTTL: time.Minute, want: ScopedIdentityMinTokenTTL},
		{name: "one minute is raised to the floor", requested: time.Minute, defaultTTL: time.Hour, want: ScopedIdentityMinTokenTTL},
		{name: "five minutes is raised to the floor", requested: 5 * time.Minute, defaultTTL: time.Hour, want: ScopedIdentityMinTokenTTL},
		{name: "the floor itself passes through", requested: ScopedIdentityMinTokenTTL, defaultTTL: time.Hour, want: ScopedIdentityMinTokenTTL},
		{name: "a mintable value passes through", requested: 42 * time.Minute, defaultTTL: time.Hour, want: 42 * time.Minute},
		{name: "above the ceiling is capped", requested: 48 * time.Hour, defaultTTL: time.Hour, want: ScopedIdentityMaxTokenTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampScopedIdentityTTL(tc.requested, tc.defaultTTL); got != tc.want {
				t.Fatalf("ClampScopedIdentityTTL(%s, %s) = %s, want %s", tc.requested, tc.defaultTTL, got, tc.want)
			}
		})
	}
}

func TestScopedIdentityClusterRoleRejectsOlderRecordGeneration(t *testing.T) {
	_, cs := managerFor(t)
	defer resetTestClientset()
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		request := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenRequest)
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token: "scoped-token", ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Duration(*request.Spec.ExpirationSeconds) * time.Second)),
		}}, nil
	})

	ctx := context.Background()
	base := ScopedIdentityShape{
		ServiceAccount:       "railgrid-si-fenced",
		Rules:                []rbacv1.PolicyRule{{APIGroups: []string{"infrastructure.railgrid.ai"}, Resources: []string{"instances"}, Verbs: []string{"get"}, ResourceNames: []string{"instance-1"}}},
		MaterializationFence: &ScopedIdentityMaterializationFence{RecordUID: "record-uid", RecordGeneration: 4},
	}
	if _, err := EnsureScopedIdentity(ctx, cs, base); err != nil {
		t.Fatalf("materialize generation 4: %v", err)
	}

	stale := base
	stale.Rules = nil
	stale.MaterializationFence = &ScopedIdentityMaterializationFence{RecordUID: "record-uid", RecordGeneration: 3}
	if _, err := EnsureScopedIdentity(ctx, cs, stale); err == nil {
		t.Fatal("older record generation changed the ClusterRole")
	} else {
		var conflict MaterializationFenceConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("error = %v, want MaterializationFenceConflict", err)
		}
	}

	roleName := WorkloadIdentityRoleName(base.ServiceAccount)
	role, err := cs.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	if len(role.Rules) != 1 || role.Rules[0].ResourceNames[0] != "instance-1" {
		t.Fatalf("stale materialization changed rules: %#v", role.Rules)
	}
	if role.Annotations[annotationMaterializationRecordUID] != "record-uid" || role.Annotations[annotationMaterializationRecordGeneration] != "4" {
		t.Fatalf("materialization fence = %#v, want record UID and generation 4", role.Annotations)
	}

	legacy := base
	legacy.Rules = nil
	legacy.MaterializationFence = nil
	if _, err := EnsureScopedIdentity(ctx, cs, legacy); err == nil {
		t.Fatal("unfenced materialization changed a fenced ClusterRole")
	} else {
		var conflict MaterializationFenceConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("unfenced error = %v, want MaterializationFenceConflict", err)
		}
	}
}

func TestScopedIdentityClusterRoleFenceRechecksAfterResourceVersionConflict(t *testing.T) {
	for _, tc := range []struct {
		name          string
		candidateGen  int64
		concurrentGen int64
		wantConflict  bool
	}{
		{name: "older writer is rejected after newer writer wins", candidateGen: 1, concurrentGen: 2, wantConflict: true},
		{name: "newer writer retries after unrelated conflict", candidateGen: 2, concurrentGen: 1, wantConflict: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roleName := "railgrid-si-fence-cas"
			uid := "record-uid"
			oldRules := []rbacv1.PolicyRule{{APIGroups: []string{"infrastructure.railgrid.ai"}, Resources: []string{"instances"}, Verbs: []string{"get"}, ResourceNames: []string{"old-instance"}}}
			candidateRules := []rbacv1.PolicyRule{{APIGroups: []string{"infrastructure.railgrid.ai"}, Resources: []string{"instances"}, Verbs: []string{"get"}, ResourceNames: []string{"new-instance"}}}
			initial := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
				Name: roleName,
				Annotations: map[string]string{
					annotationMaterializationRecordUID:        uid,
					annotationMaterializationRecordGeneration: "1",
				},
			}, Rules: oldRules}
			_, cs := managerFor(t, initial)
			defer resetTestClientset()

			groupResource := schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterroles"}
			gvr := schema.GroupVersionResource{Group: rbacv1.GroupName, Version: "v1", Resource: "clusterroles"}
			concurrentUpdates := 0
			cs.PrependReactor("update", "clusterroles", func(clientAction clienttesting.Action) (bool, runtime.Object, error) {
				concurrentUpdates++
				if concurrentUpdates > 1 {
					return false, nil, nil
				}
				stored, err := cs.Tracker().Get(gvr, "", roleName)
				if err != nil {
					return true, nil, err
				}
				current := stored.(*rbacv1.ClusterRole).DeepCopy()
				if tc.concurrentGen > tc.candidateGen {
					current.Rules = []rbacv1.PolicyRule{{APIGroups: []string{"infrastructure.railgrid.ai"}, Resources: []string{"instances"}, Verbs: []string{"get"}, ResourceNames: []string{"newer-writer-instance"}}}
					current.Annotations[annotationMaterializationRecordGeneration] = "2"
				} else {
					current.Annotations["example.test/concurrent-metadata"] = "updated"
				}
				if err := cs.Tracker().Update(gvr, current, ""); err != nil {
					return true, nil, err
				}
				return true, nil, apierrors.NewConflict(groupResource, roleName, errors.New("simulated concurrent write"))
			})

			shape := ScopedIdentityShape{
				Rules: candidateRules,
				MaterializationFence: &ScopedIdentityMaterializationFence{
					RecordUID: uid, RecordGeneration: tc.candidateGen,
				},
			}
			err := ensureScopedClusterRole(context.Background(), cs, roleName, shape)
			if tc.wantConflict {
				var conflict MaterializationFenceConflict
				if !errors.As(err, &conflict) {
					t.Fatalf("ensure error = %v, want materialization fence conflict", err)
				}
			} else if err != nil {
				t.Fatalf("newer writer did not recover from unrelated conflict: %v", err)
			}

			role, err := cs.RbacV1().ClusterRoles().Get(context.Background(), roleName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get final ClusterRole: %v", err)
			}
			if tc.wantConflict {
				if role.Annotations[annotationMaterializationRecordGeneration] != "2" || len(role.Rules) != 1 || role.Rules[0].ResourceNames[0] != "newer-writer-instance" {
					t.Fatalf("newer concurrent role was overwritten: annotations=%#v rules=%#v", role.Annotations, role.Rules)
				}
			} else if role.Annotations[annotationMaterializationRecordGeneration] != "2" || len(role.Rules) != 1 || role.Rules[0].ResourceNames[0] != "new-instance" {
				t.Fatalf("newer retry did not apply candidate role: annotations=%#v rules=%#v", role.Annotations, role.Rules)
			}
		})
	}
}

// TestEnsureScopedIdentityRaisesABelowFloorTTLOnTheWire is the same guarantee
// one level down: whatever a caller puts in the shape, the TokenRequest that
// leaves the hub asks for something kcp will mint.
func TestEnsureScopedIdentityRaisesABelowFloorTTLOnTheWire(t *testing.T) {
	_, cs := managerFor(t)
	defer resetTestClientset()

	var gotTTL int64
	cs.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		request := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenRequest)
		gotTTL = *request.Spec.ExpirationSeconds
		return true, &authnv1.TokenRequest{Status: authnv1.TokenRequestStatus{
			Token:               "scoped-token",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Duration(gotTTL) * time.Second)),
		}}, nil
	})

	token, err := EnsureScopedIdentity(context.Background(), cs, ScopedIdentityShape{
		ServiceAccount: "railgrid-si-floor",
		TokenTTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("EnsureScopedIdentity: %v", err)
	}
	if want := int64(ScopedIdentityMinTokenTTL / time.Second); gotTTL != want {
		t.Fatalf("TokenRequest expirationSeconds = %d, want %d (a 5m request must be raised to the floor, not sent as-is)", gotTTL, want)
	}
	// The longer-than-requested token must still be accepted: the returned
	// expiry is the clamped lifetime, and the "exceeds requested lifetime"
	// guard is checked against it, not against the caller's original ask.
	if until := time.Until(token.ExpiresAt); until <= 5*time.Minute {
		t.Fatalf("token expires in %s; the clamped lifetime should be ~%s", until, ScopedIdentityMinTokenTTL)
	}
}
