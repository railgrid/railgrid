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

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/agent/tunnel"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

// The agent's durable credential used to be a kubeconfig holding a permanent
// ServiceAccount token: written to ~/.railgrid/agent-<edge>.kubeconfig, and in
// Kubernetes mode into a Secret in the agent's own cluster as well. Two copies
// of something that never expired and that nothing could revoke.
//
// What is stored now is the enrolment bundle: a TTL'd scoped identity plus the
// addressing it was issued for. The token in it stops working, the agent
// re-mints it through the provider before it does, and the hub collects the
// identity when the edge is deleted.

// AgentCredentialPath returns where the enrolment bundle is stored.
func AgentCredentialPath(edgeName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return AgentCredentialPathForHome(home, edgeName), nil
}

// AgentCredentialPathForHome is AgentCredentialPath under an explicit home, for
// installers that run as one user and write for another.
func AgentCredentialPathForHome(home, edgeName string) string {
	return filepath.Join(home, ".railgrid", "agent-"+edgeName+".credential.json")
}

// SaveAgentCredential persists the bundle with the same restrictive rewrite the
// kubeconfig used (writeCredentialFile), because it holds a live bearer.
func SaveAgentCredential(edgeName string, credential tunnel.Credential) error {
	path, err := AgentCredentialPath(edgeName)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encoding the agent credential: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := writeCredentialFile(path, encoded); err != nil {
		return fmt.Errorf("writing the agent credential to %s: %w", path, err)
	}
	return nil
}

// LoadAgentCredential reads a previously saved bundle. A missing file is not an
// error the caller should treat as fatal: it simply means this agent has not
// enrolled yet and must present its join token.
func LoadAgentCredential(edgeName string) (tunnel.Credential, bool, error) {
	path, err := AgentCredentialPath(edgeName)
	if err != nil {
		return tunnel.Credential{}, false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return tunnel.Credential{}, false, nil
	}
	if err != nil {
		return tunnel.Credential{}, false, fmt.Errorf("reading %s: %w", path, err)
	}
	var credential tunnel.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return tunnel.Credential{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	if credential.Token == "" {
		return tunnel.Credential{}, false, nil
	}
	return credential, true, nil
}

// DeleteAgentCredential removes a saved bundle.
func DeleteAgentCredential(edgeName string) error {
	path, err := AgentCredentialPath(edgeName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

// hubConfigFromCredential builds the kcp client config the agent's reporters
// and reconcilers use, from the bundle rather than from a kubeconfig file.
//
// Everything it needs is in the bundle by design: the hub URL, the CA to trust
// and the logical cluster to address. That is the point of shipping an
// addressing tuple instead of a static token — the agent does not have to be
// told separately where it lives.
func hubConfigFromCredential(credential tunnel.Credential, insecure bool) *rest.Config {
	cfg := &rest.Config{
		Host:        apiurl.HubServerURL(credential.HubURL, credential.ClusterID),
		BearerToken: credential.Token,
	}
	switch {
	case insecure:
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	case len(credential.CACertData) > 0:
		cfg.TLSClientConfig = rest.TLSClientConfig{CAData: credential.CACertData}
	}
	applyHubClientDefaults(cfg)
	return cfg
}
