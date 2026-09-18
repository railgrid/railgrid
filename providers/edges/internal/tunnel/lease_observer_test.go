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

package tunnel

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLeaseObserverDerivesLivenessFromTheRegistryLease(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	const key = "linuxservers/cl-1/edge-1"
	lease := func(renew time.Time, holder string) *coordinationv1.Lease {
		rt := metav1.NewMicroTime(renew)
		return &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name: TunnelLeaseName(key), Namespace: RegistryNamespace,
				Labels:      map[string]string{TunnelLeaseLabel: "true"},
				Annotations: map[string]string{TunnelLeaseKeyAnnotation: key},
			},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(holder), RenewTime: &rt},
		}
	}

	cases := map[string]struct {
		objs     []client.Object
		wantHeld bool
		wantAddr string
	}{
		"fresh":        {objs: []client.Object{lease(now.Add(-30*time.Second), "10.0.0.1:8090")}, wantHeld: true, wantAddr: "10.0.0.1:8090"},
		"expired":      {objs: []client.Object{lease(now.Add(-RegistryLeaseTTL-time.Second), "10.0.0.1:8090")}, wantHeld: false, wantAddr: "10.0.0.1:8090"},
		"no holder":    {objs: []client.Object{lease(now, "")}, wantHeld: false},
		"missing":      {wantHeld: false},
		"other tunnel": {objs: []client.Object{lease(now, "10.0.0.1:8090")}, wantHeld: true, wantAddr: "10.0.0.1:8090"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objs...).Build()
			o := NewLeaseObserver(c)
			o.now = func() time.Time { return now }

			obs, err := o.Observe(context.Background(), key)
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			if obs.Held != tc.wantHeld || obs.Holder != tc.wantAddr {
				t.Fatalf("observation = %+v, want held=%v holder=%q", obs, tc.wantHeld, tc.wantAddr)
			}
			if len(tc.objs) > 0 {
				if obs.RenewTime.IsZero() || !obs.ExpiresAt.Equal(obs.RenewTime.Add(RegistryLeaseTTL)) {
					t.Fatalf("renew/expiry = %v/%v", obs.RenewTime, obs.ExpiresAt)
				}
			} else if !obs.RenewTime.IsZero() || !obs.ExpiresAt.IsZero() {
				t.Fatalf("missing lease reported times %+v", obs)
			}

			// A different edge's key never resolves to this lease.
			if other, err := o.Observe(context.Background(), "linuxservers/cl-1/edge-2"); err != nil || other.Held {
				t.Fatalf("foreign key observation = %+v, %v", other, err)
			}
		})
	}
}

// The registry's own freshness rule and the observer's must agree, or the data
// plane and the edge status could disagree about the same lease.
func TestLeaseObserverAgreesWithRegistryFreshness(t *testing.T) {
	now := time.Now()
	for _, age := range []time.Duration{0, RegistryLeaseTTL, RegistryLeaseTTL + time.Millisecond, time.Hour} {
		rt := metav1.NewMicroTime(now.Add(-age))
		lease := &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{RenewTime: &rt}}
		r := &Registry{now: func() time.Time { return now }}
		if r.leaseFresh(lease) != leaseFreshAt(lease, now) {
			t.Fatalf("age %v: registry and observer disagree", age)
		}
	}
}

func TestParseEdgeConnKey(t *testing.T) {
	res, cl, name, ok := ParseEdgeConnKey(EdgeConnKey("linuxservers", "cl-1", "edge-1"))
	if !ok || res != "linuxservers" || cl != "cl-1" || name != "edge-1" {
		t.Fatalf("parse = %q/%q/%q/%v", res, cl, name, ok)
	}
	for _, bad := range []string{"", "a/b", "a/b/c/d", "/b/c", "a//c", "a/b/"} {
		if _, _, _, ok := ParseEdgeConnKey(bad); ok {
			t.Fatalf("%q parsed as a conn key", bad)
		}
	}
}
