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

package quota

import (
	"context"
	"errors"
	"testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func TestCheckQuotas(t *testing.T) {
	cases := []struct {
		name    string
		cap     int32
		count   int32
		wantErr bool
	}{
		{"under cap", 3, 2, false},
		{"at cap", 3, 3, true},
		{"over cap", 3, 7, true},
		{"zero cap denies even the first", 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter := CounterFunc(func(context.Context) (int32, error) { return tc.count, nil })
			for kind, check := range map[string]func(context.Context, string, int32, Counter) error{
				KindOrganizations:     CheckOrgQuota,
				KindFreeOrganizations: CheckFreeOrgQuota,
			} {
				err := check(context.Background(), "alice", tc.cap, counter)
				if !tc.wantErr {
					if err != nil {
						t.Fatalf("%s: err = %v, want nil", kind, err)
					}
					continue
				}
				var qe *QuotaExceededError
				if !errors.As(err, &qe) {
					t.Fatalf("%s: err = %v, want *QuotaExceededError", kind, err)
				}
				if qe.Kind != kind || qe.Owner != "alice" || qe.Count != tc.count || qe.Cap != tc.cap {
					t.Fatalf("%s: got %+v", kind, qe)
				}
			}
		})
	}
}

func TestCheckOrgQuota_CounterErrorAndNilCounter(t *testing.T) {
	boom := errors.New("boom")
	err := CheckOrgQuota(context.Background(), "alice", 3, CounterFunc(func(context.Context) (int32, error) { return 0, boom }))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
	if err := CheckOrgQuota(context.Background(), "alice", 3, nil); err == nil {
		t.Fatal("expected error for a nil counter")
	}
}

func TestCapsForUser(t *testing.T) {
	base := Caps{Organizations: 10, FreeOrganizations: 1}
	if got := base.ForUser(nil); got != base {
		t.Fatalf("nil user: %+v", got)
	}
	if got := base.ForUser(&tenancyv1alpha1.User{}); got != base {
		t.Fatalf("no overrides: %+v", got)
	}
	zero, three := int32(0), int32(3)
	u := &tenancyv1alpha1.User{}
	u.Spec.Quota = &tenancyv1alpha1.UserQuota{Organizations: &three}
	if got := base.ForUser(u); got != (Caps{Organizations: 3, FreeOrganizations: 1}) {
		t.Fatalf("organizations override: %+v", got)
	}
	// A set 0 is a real cap of zero, not "use the default".
	u.Spec.Quota = &tenancyv1alpha1.UserQuota{FreeOrganizations: &zero}
	if got := base.ForUser(u); got != (Caps{Organizations: 10, FreeOrganizations: 0}) {
		t.Fatalf("free organizations override: %+v", got)
	}
}

func TestIsFree(t *testing.T) {
	o := &tenancyv1alpha1.Organization{}
	if !IsFree(o, "free") {
		t.Fatal("empty spec.plan is the default plan")
	}
	o.Spec.Plan = "free"
	if !IsFree(o, "free") {
		t.Fatal("naming the default plan is free too")
	}
	o.Spec.Plan = "team"
	if IsFree(o, "free") {
		t.Fatal("another plan is not free")
	}
}
