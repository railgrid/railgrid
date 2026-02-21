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
	if k.kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+k.kubeconfig)
	}
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("kedge %s failed: %w\noutput: %s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
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

// Kubectl runs a kubectl command against the hub cluster kubeconfig.
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
