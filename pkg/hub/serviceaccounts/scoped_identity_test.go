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
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
