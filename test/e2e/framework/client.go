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

// SiteCreate creates a site with the given name and labels.
func (k *KedgeClient) SiteCreate(ctx context.Context, name string, labels ...string) error {
	args := []string{"site", "create", name}
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}
	_, err := k.run(ctx, args...)
	return err
}

// SiteList returns the raw output of `kedge site list`.
func (k *KedgeClient) SiteList(ctx context.Context) (string, error) {
	return k.run(ctx, "site", "list")
}

// SiteDelete deletes a site by name.
func (k *KedgeClient) SiteDelete(ctx context.Context, name string) error {
	_, err := k.run(ctx, "site", "delete", name)
	return err
}

// WaitForSiteReady polls until the given site appears with phase "Ready" in
// `kedge site list` or the timeout expires. It avoids the substring-match
// pitfall where "NotReady" would satisfy strings.Contains(…, "Ready").
func (k *KedgeClient) WaitForSiteReady(ctx context.Context, siteName string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.SiteList(ctx)
		if err != nil {
			return false, nil // not ready yet, retry
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			// Expected columns: NAME STATUS K8S_VERSION PROVIDER REGION AGE
			if len(fields) >= 2 && fields[0] == siteName && fields[1] == "Ready" {
				return true, nil
			}
		}
		return false, nil
	})
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

// WaitForSitePhase polls until the given site has the expected phase in
// `kedge site list` or the timeout expires.
func (k *KedgeClient) WaitForSitePhase(ctx context.Context, siteName, phase string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.SiteList(ctx)
		if err != nil {
			return false, nil // not ready yet, retry
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == siteName && fields[1] == phase {
				return true, nil
			}
		}
		return false, nil
	})
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

// WaitForPlacement polls until a Placement targeting siteName exists for the
// given VirtualWorkload or the timeout expires.
func (k *KedgeClient) WaitForPlacement(ctx context.Context, vwName, namespace, siteName string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "placements",
			"-n", namespace,
			"--insecure-skip-tls-verify",
			"-l", "kedge.faros.sh/virtualworkload="+vwName,
			"-o", "custom-columns=SITE:.spec.siteName",
			"--no-headers",
		)
		if err != nil {
			return false, nil
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == siteName {
				return true, nil
			}
		}
		return false, nil
	})
}

// WaitForNoPlacement polls until no Placement targeting siteName exists for the
// given VirtualWorkload — i.e. the scheduler has not routed to that site.
// Returns nil when the condition is confirmed within timeout; returns an error
// if a matching placement still exists at deadline.
func (k *KedgeClient) WaitForNoPlacement(ctx context.Context, vwName, namespace, siteName string, timeout time.Duration) error {
	return Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		out, err := k.Kubectl(ctx,
			"get", "placements",
			"-n", namespace,
			"--insecure-skip-tls-verify",
			"-l", "kedge.faros.sh/virtualworkload="+vwName,
			"-o", "custom-columns=SITE:.spec.siteName",
			"--no-headers",
		)
		if err != nil {
			// If placements don't exist yet, no match — confirmed.
			return true, nil
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == siteName {
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

// ExtractSiteKubeconfig waits for the site kubeconfig secret to appear in the
// hub cluster and writes the base64-decoded content to destPath.
// Secret name format: site-<siteName>-kubeconfig in namespace kedge-system.
func (k *KedgeClient) ExtractSiteKubeconfig(ctx context.Context, siteName, destPath string) error {
	secretName := "site-" + siteName + "-kubeconfig"

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
