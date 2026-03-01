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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// KedgeClient wraps the kedge CLI binary for use in e2e tests.
type KedgeClient struct {
	bin        string
	workDir    string
	kubeconfig string
	hubURL     string
}

// NewKedgeClient creates a new KedgeClient.
func NewKedgeClient(workDir, kubeconfig, hubURL string) *KedgeClient {
	return &KedgeClient{
		bin:        filepath.Join(workDir, KedgeBin),
		workDir:    workDir,
		kubeconfig: kubeconfig,
		hubURL:     hubURL,
	}
}

// run executes a kedge command and returns stdout+stderr.
func (k *KedgeClient) run(ctx context.Context, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, k.bin, args...)
	cmd.Dir = k.workDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// Ensure the bin/ directory is in PATH so exec credential plugins (e.g.
	// `kedge get-token`) can be found when the OIDC kubeconfig is used.
	binDir := filepath.Dir(k.bin)
	env := os.Environ()
	pathUpdated := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + binDir + string(filepath.ListSeparator) + e[5:]
			pathUpdated = true
			break
		}
	}
	if !pathUpdated {
		env = append(env, "PATH="+binDir)
	}

	if k.kubeconfig != "" {
		env = append(env, "KUBECONFIG="+k.kubeconfig)
	}
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("kedge %s failed: %w\noutput: %s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}

// Run executes an arbitrary kedge command and returns stdout+stderr combined.
// This is the public variant of the internal run() helper.
func (k *KedgeClient) Run(ctx context.Context, args ...string) (string, error) {
	return k.run(ctx, args...)
}

// Login authenticates to the hub using a static token.
func (k *KedgeClient) Login(ctx context.Context, token string) error {
	_, err := k.run(ctx,
		"login",
		"--hub-url", k.hubURL,
		"--insecure-skip-tls-verify",
		"--token", token,
	)
	return err
}

// RunCmd runs an arbitrary command and returns its combined output (package-level helper).
func RunCmd(ctx context.Context, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("%s %s: %w\noutput: %s", name, strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}

// KubectlWithConfig runs kubectl with an explicit kubeconfig path (package-level helper).
func KubectlWithConfig(ctx context.Context, kubeconfig string, args ...string) (string, error) {
	var buf bytes.Buffer
	allArgs := append([]string{"--kubeconfig", kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("kubectl %s failed: %w\noutput: %s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}

func (k *KedgeClient) Kubectl(ctx context.Context, args ...string) (string, error) {
	var buf bytes.Buffer
	allArgs := append([]string{"--kubeconfig", k.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	cmd.Dir = k.workDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("kubectl %s failed: %w\noutput: %s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}

// ApplyFile applies a YAML file via kubectl against the hub kubeconfig.
func (k *KedgeClient) ApplyFile(ctx context.Context, path string) error {
	_, err := k.Kubectl(ctx, "apply", "-f", path)
	return err
}

// ApplyManifest writes yaml to a temp file and applies it via kubectl.
func (k *KedgeClient) ApplyManifest(ctx context.Context, yaml string) error {
	f, err := os.CreateTemp("", "kedge-e2e-manifest-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck
	if _, err := f.WriteString(yaml); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing manifest file: %w", err)
	}
	_, err = k.Kubectl(ctx, "apply", "--insecure-skip-tls-verify", "-f", f.Name())
	return err
}

// WaitForPlacement polls until a Placement targeting edgeName exists for the
// given VirtualWorkload or the timeout expires.
func (k *KedgeClient) WaitForPlacement(ctx context.Context, vwName, namespace, edgeName string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "placements",
			"-n", namespace,
			"--insecure-skip-tls-verify",
			"-l", "kedge.faros.sh/virtualworkload="+vwName,
			"-o", "custom-columns=EDGE:.spec.edgeName",
			"--no-headers",
		)
		if err != nil {
			return false, nil
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == edgeName {
				return true, nil
			}
		}
		return false, nil
	})
}

// WaitForNoPlacement polls until no Placement targeting edgeName exists for the
// given VirtualWorkload — i.e. the scheduler has not routed to that edge.
// Returns nil when the condition is confirmed within timeout; returns an error
// if a matching placement still exists at deadline.
func (k *KedgeClient) WaitForNoPlacement(ctx context.Context, vwName, namespace, edgeName string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "placements",
			"-n", namespace,
			"--insecure-skip-tls-verify",
			"-l", "kedge.faros.sh/virtualworkload="+vwName,
			"-o", "custom-columns=EDGE:.spec.edgeName",
			"--no-headers",
		)
		if err != nil {
			// If placements don't exist yet, no match — confirmed.
			return true, nil
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == edgeName {
				return false, nil // still present, keep polling
			}
		}
		return true, nil
	})
}

// DeleteVirtualWorkload deletes a VirtualWorkload by name and namespace.
func (k *KedgeClient) DeleteVirtualWorkload(ctx context.Context, name, namespace string) error {
	_, err := k.Kubectl(ctx,
		"delete", "virtualworkload", name,
		"-n", namespace,
		"--insecure-skip-tls-verify",
		"--ignore-not-found",
	)
	return err
}

// ─── Edge resource helpers (Phase 5 — replaces Site / Server helpers) ────────

// EdgeCreate creates an Edge resource via kubectl with the given name, type,
// and optional comma-separated labels.
// type must be "kubernetes" or "server".
func (k *KedgeClient) EdgeCreate(ctx context.Context, name, edgeType string, labels ...string) error {
	labelStr := strings.Join(labels, ",")
	labelsYAML := ""
	if labelStr != "" {
		labelsYAML = "\n  labels:"
		for _, kv := range strings.Split(labelStr, ",") {
			parts := strings.SplitN(strings.TrimSpace(kv), "=", 2)
			if len(parts) == 2 {
				labelsYAML += "\n    " + parts[0] + ": " + parts[1]
			}
		}
	}

	manifest := fmt.Sprintf(`apiVersion: kedge.faros.sh/v1alpha1
kind: Edge
metadata:
  name: %s%s
spec:
  type: %s
`, name, labelsYAML, edgeType)

	return k.ApplyManifest(ctx, manifest)
}

// EdgeList returns raw kubectl output for listing all edges.
func (k *KedgeClient) EdgeList(ctx context.Context) (string, error) {
	return k.Kubectl(ctx,
		"get", "edges",
		"-o", "custom-columns=NAME:.metadata.name,PHASE:.status.phase,CONNECTED:.status.connected",
		"--no-headers",
		"--insecure-skip-tls-verify",
	)
}

// EdgeDelete deletes an Edge resource by name.
func (k *KedgeClient) EdgeDelete(ctx context.Context, name string) error {
	_, err := k.Kubectl(ctx,
		"delete", "edge", name,
		"--ignore-not-found",
		"--insecure-skip-tls-verify",
	)
	return err
}

// WaitForEdgeReady polls until the given Edge resource has phase "Ready".
func (k *KedgeClient) WaitForEdgeReady(ctx context.Context, edgeName string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "edge", edgeName,
			"-o", "jsonpath={.status.phase}",
			"--insecure-skip-tls-verify",
		)
		if err != nil {
			return false, nil
		}
		return strings.TrimSpace(out) == "Ready", nil
	})
}

// WaitForEdgePhase polls until the given Edge resource has the expected phase.
func (k *KedgeClient) WaitForEdgePhase(ctx context.Context, edgeName, phase string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "edge", edgeName,
			"-o", "jsonpath={.status.phase}",
			"--insecure-skip-tls-verify",
		)
		if err != nil {
			return false, nil
		}
		return strings.TrimSpace(out) == phase, nil
	})
}

// GetEdgeURL polls until edge.status.URL is populated and returns it.
// It returns an error if the URL is not set within 2 minutes.
func (k *KedgeClient) GetEdgeURL(ctx context.Context, name string) (string, error) {
	var edgeURL string
	err := Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "edge", name,
			"-o", "jsonpath={.status.URL}",
			"--insecure-skip-tls-verify",
		)
		if err != nil {
			return false, nil
		}
		u := strings.TrimSpace(out)
		if u == "" {
			return false, nil
		}
		edgeURL = u
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("edge %q status.URL not populated within timeout: %w", name, err)
	}
	return edgeURL, nil
}

// KubectlWithURL runs kubectl against a specific server URL using credentials
// from the hub kubeconfig. The hub bearer token is passed transparently to the
// edge proxy endpoint on the hub.
func (k *KedgeClient) KubectlWithURL(ctx context.Context, serverURL string, args ...string) (string, error) {
	var buf bytes.Buffer
	allArgs := append([]string{
		"--kubeconfig", k.kubeconfig,
		"--server", serverURL,
		"--insecure-skip-tls-verify",
	}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	cmd.Dir = k.workDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("kubectl --server %s %s failed: %w\noutput: %s",
			serverURL, strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}

// ExtractEdgeKubeconfig waits for the edge kubeconfig secret to appear in the
// hub cluster and writes the base64-decoded content to destPath.
// Secret name format: edge-<edgeName>-kubeconfig in namespace kedge-system.
func (k *KedgeClient) ExtractEdgeKubeconfig(ctx context.Context, edgeName, destPath string) error {
	secretName := "edge-" + edgeName + "-kubeconfig"

	return Poll(ctx, 5*time.Second, 5*time.Minute, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "secret", secretName,
			"-n", "kedge-system",
			"-o", "json",
		)
		if err != nil || out == "" {
			return false, nil
		}

		var secret struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &secret); err != nil {
			return false, nil
		}
		encoded, ok := secret.Data["kubeconfig"]
		if !ok || encoded == "" {
			return false, nil
		}

		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return false, nil
		}

		if err := os.WriteFile(destPath, decoded, 0600); err != nil {
			return false, err
		}
		return true, nil
	})
}
