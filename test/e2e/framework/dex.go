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

package framework

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	// DexServicePort is the host port where Dex is reachable from the test
	// runner via the kind port mapping (hostPort DexServicePort →
	// containerPort 31556 → Dex NodePort inside kind).
	DexServicePort = 5556

	// DexIssuerURL is the OIDC issuer URL used by both the hub pod (cluster
	// DNS) and the test runner (/etc/hosts alias to localhost).
	DexIssuerURL = "http://dex.kedge-system.svc.cluster.local:5556/dex"

	// DexExternalHost is added to the test runner's /etc/hosts as 127.0.0.1
	// so it can reach the in-cluster Dex via the kind port mapping.
	DexExternalHost = "dex.kedge-system.svc.cluster.local"

	// DexClientID / DexClientSecret are the OAuth2 credentials for the hub.
	DexClientID     = "kedge"
	DexClientSecret = "kedge-test-secret"

	// DexTestUserEmail / DexTestUserPassword are the static-password credentials
	// seeded in Dex for e2e OIDC tests.
	DexTestUserEmail    = "admin@test.kedge.local"
	DexTestUserPassword = "Password1!"
)

// DexEnv holds runtime OIDC provider info stored in the test context.
type DexEnv struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	UserEmail    string
	UserPassword string
}

// dexEnvKey is the context key for DexEnv.
type dexEnvKey struct{}

// WithDexEnv stores DexEnv in a context.
func WithDexEnv(ctx context.Context, d *DexEnv) context.Context {
	return context.WithValue(ctx, dexEnvKey{}, d)
}

// DexEnvFrom retrieves DexEnv from a context.
func DexEnvFrom(ctx context.Context) *DexEnv {
	v, _ := ctx.Value(dexEnvKey{}).(*DexEnv)
	return v
}

// DefaultDexEnv returns the DexEnv used in the e2e OIDC suite.
func DefaultDexEnv() *DexEnv {
	return &DexEnv{
		IssuerURL:    DexIssuerURL,
		ClientID:     DexClientID,
		ClientSecret: DexClientSecret,
		UserEmail:    DexTestUserEmail,
		UserPassword: DexTestUserPassword,
	}
}

// WaitForDexReady polls Dex's OIDC discovery endpoint on localhost until it
// returns 200 or the context deadline is exceeded.  The test runner reaches
// Dex via the kind port mapping on localhost:DexServicePort.
func WaitForDexReady(ctx context.Context) error {
	discoveryURL := fmt.Sprintf("http://localhost:%d/dex/.well-known/openid-configuration", DexServicePort)
	return Poll(ctx, 3*time.Second, 3*time.Minute, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
		if err != nil {
			return false, nil
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	})
}
