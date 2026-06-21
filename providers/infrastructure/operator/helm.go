/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/install"
)

// EnsureKroRelease install/upgrades the kro Helm release on the runtime cluster
// (addressed by runtimeKubeconfigPath) with values derived from the CR. It
// shells out to the `helm` binary — using the Go SDK would drag k8s.io/kubernetes
// into this module. The kcp-kubeconfig Secret kro mounts is seeded separately
// (install.SeedKroClusterFromKubeconfig) before this runs.
func EnsureKroRelease(ctx context.Context, runtimeKubeconfigPath string, kro v1alpha1.KroSpec) error {
	args := []string{
		"upgrade", "--install", kro.ReleaseName, kro.Chart,
		"--version", kro.Version,
		"--namespace", kro.Namespace, "--create-namespace",
		"--set", "multicluster.enabled=true",
		"--set", "multicluster.provider=kcp-apiexport",
		"--set", "multicluster.kcp.kubeconfigSecret=" + install.KroSecretName,
		"--set", "multicluster.kcp.apiExportEndpointSlice=" + kro.APIExportEndpointSlice,
		"--set", "controller.deployToLocalRuntime=true",
	}
	if kro.Image.Repository != "" {
		args = append(args, "--set", "image.repository="+kro.Image.Repository)
	}
	// Default the image tag to the chart version when not pinned separately — the
	// fork chart otherwise defaults to the upstream image, which lacks the
	// multicluster build.
	tag := kro.Image.Tag
	if tag == "" {
		tag = kro.Version
	}
	args = append(args, "--set", "image.tag="+tag)
	for k, v := range kro.ExtraValues {
		args = append(args, "--set", k+"="+v)
	}

	cmd := exec.CommandContext(ctx, "helm", args...)
	// An empty path means "in-cluster runtime": run helm with no KUBECONFIG
	// override so it uses the pod's in-cluster service account.
	if runtimeKubeconfigPath != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+runtimeKubeconfigPath)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm upgrade --install %s: %w\n%s", kro.ReleaseName, err, string(out))
	}
	return nil
}

// DeleteKroRelease uninstalls the kro Helm release (best-effort; a missing
// release is not an error).
func DeleteKroRelease(ctx context.Context, runtimeKubeconfigPath string, kro v1alpha1.KroSpec) error {
	cmd := exec.CommandContext(ctx, "helm", "uninstall", kro.ReleaseName,
		"--namespace", kro.Namespace, "--ignore-not-found")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+runtimeKubeconfigPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm uninstall %s: %w\n%s", kro.ReleaseName, err, string(out))
	}
	return nil
}
