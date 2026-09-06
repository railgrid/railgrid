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

// Package hubclient holds the pieces a provider needs to talk to the faros
// hub itself (as opposed to kcp): today, the credential for the heartbeat.
package hubclient

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

const (
	// EnvHubToken is an explicit bearer token for hub calls. When set it wins
	// over the kubeconfig-derived token.
	EnvHubToken = "FAROS_HUB_TOKEN"
	// EnvProviderKubeconfig is the workspace-scoped kubeconfig the hub minted
	// for the provider's own service account. Its bearer token is what the
	// hub verifies heartbeats against, so it doubles as the hub credential.
	EnvProviderKubeconfig = "FAROS_PROVIDER_KUBECONFIG"
)

// ResolveHubToken returns the bearer token a provider should present to the
// hub, e.g. on POST /api/providers/{name}/heartbeat. The hub authenticates
// that call as the provider's own service account, so the token is, in order:
//
//  1. FAROS_HUB_TOKEN, if set (explicit override; charts that still wire
//     hub.tokenSecretRef keep working unchanged);
//  2. the bearer token inside the kubeconfig at FAROS_PROVIDER_KUBECONFIG,
//     which every provider already mounts.
//
// An empty string with a nil error means neither is configured; callers keep
// sending unauthenticated beats, which the hub logs (warn) or rejects
// (enforce). A kubeconfig that is set but cannot be read or carries no bearer
// token is an error so the misconfiguration surfaces in the provider's logs.
func ResolveHubToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv(EnvHubToken)); t != "" {
		return t, nil
	}
	path := strings.TrimSpace(os.Getenv(EnvProviderKubeconfig))
	if path == "" {
		return "", nil
	}
	return TokenFromKubeconfig(path)
}

// TokenFromKubeconfig loads the kubeconfig at path and returns the bearer
// token its current context resolves to (inline or via a token file).
func TokenFromKubeconfig(path string) (string, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return "", fmt.Errorf("loading %s=%s: %w", EnvProviderKubeconfig, path, err)
	}
	rc, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return "", fmt.Errorf("resolving %s=%s: %w", EnvProviderKubeconfig, path, err)
	}
	// clientcmd reads a tokenFile into BearerToken verbatim, so trim the
	// trailing newline a hand-written token file usually carries.
	if t := strings.TrimSpace(rc.BearerToken); t != "" {
		return t, nil
	}
	if rc.BearerTokenFile != "" {
		b, err := os.ReadFile(rc.BearerTokenFile)
		if err != nil {
			return "", fmt.Errorf("reading token file from %s=%s: %w", EnvProviderKubeconfig, path, err)
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	return "", fmt.Errorf("%s=%s carries no bearer token", EnvProviderKubeconfig, path)
}
