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
)

func TestCheckOrgQuota(t *testing.T) {
	cases := []struct {
		name    string
		cap     int32
		count   int32
		wantErr bool
		wantCap int32
	}{
		{"under cap", 3, 2, false, 3},
		{"at cap", 3, 3, true, 3},
		{"over cap", 3, 7, true, 3},
		{"zero cap denies even the first", 0, 0, true, 0},
		{"negative cap falls back to the default", -1, DefaultOrgsPerUser - 1, false, DefaultOrgsPerUser},
		{"negative cap default reached", -1, DefaultOrgsPerUser, true, DefaultOrgsPerUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckOrgQuota(context.Background(), "alice", tc.cap, CounterFunc(func(context.Context) (int32, error) { return tc.count, nil }))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			var qe *QuotaExceededError
			if !errors.As(err, &qe) {
				t.Fatalf("err = %v, want *QuotaExceededError", err)
			}
			if qe.Kind != "Organization" || qe.Owner != "alice" || qe.Count != tc.count || qe.Cap != tc.wantCap {
				t.Fatalf("got %+v", qe)
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
