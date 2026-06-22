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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/install"
)

// EnsureKroRelease install/upgrades the kro Helm release on the runtime cluster
// (addressed by runtimeKubeconfigPath) with values derived from the CR. It
// shells out to the `helm` binary — using the Go SDK would drag k8s.io/kubernetes
// into this module. The kcp-kubeconfig Secret kro mounts is seeded separately
// (install.SeedKroClusterFromKubeconfig) before this runs.
func EnsureKroRelease(ctx context.Context, runtimeKubeconfigPath string, kro v1alpha1.KroSpec) error {
	// Clear any release wedged in a pending-* state by an interrupted prior helm
	// operation before attempting the upgrade — otherwise helm refuses with
	// "another operation (install/upgrade/rollback) is in progress" and the
	// operator's retries loop forever (nothing else clears pending state).
	if err := recoverPendingRelease(ctx, runtimeKubeconfigPath, kro); err != nil {
		return err
	}

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

	if out, err := runHelm(ctx, runtimeKubeconfigPath, args...); err != nil {
		return fmt.Errorf("helm upgrade --install %s: %w\n%s", kro.ReleaseName, err, string(out))
	}
	return nil
}

// DeleteKroRelease uninstalls the kro Helm release (best-effort; a missing
// release is not an error).
func DeleteKroRelease(ctx context.Context, runtimeKubeconfigPath string, kro v1alpha1.KroSpec) error {
	if out, err := runHelm(ctx, runtimeKubeconfigPath, "uninstall", kro.ReleaseName,
		"--namespace", kro.Namespace, "--ignore-not-found"); err != nil {
		return fmt.Errorf("helm uninstall %s: %w\n%s", kro.ReleaseName, err, string(out))
	}
	return nil
}

// recoverPendingRelease detects a kro release left in a pending-* state by an
// interrupted helm operation (most commonly the operator pod killed mid-upgrade
// during a rollout) and clears it so the subsequent upgrade can proceed. Helm
// stores the in-flight state in the release record and refuses any new operation
// until it resolves; nothing clears it automatically, so the operator's 2-minute
// retries would otherwise wedge permanently.
//
// Strategy (see planRecovery):
//   - pending-upgrade / pending-rollback → roll back to the newest non-pending
//     revision (a known-good state exists);
//   - pending-install → the first install never completed, there is nothing to
//     roll back to, so uninstall the half-applied release and let the upgrade
//     reinstall it.
func recoverPendingRelease(ctx context.Context, kubeconfigPath string, kro v1alpha1.KroSpec) error {
	status, err := helmReleaseStatus(ctx, kubeconfigPath, kro)
	if err != nil {
		// Release not found (fresh install) or status unreadable — nothing to
		// recover; let the upgrade --install proceed and surface any real error.
		return nil
	}

	rev, hasDeployed := lastDeployedRevision(ctx, kubeconfigPath, kro)
	switch action, target := planRecovery(status, rev, hasDeployed); action {
	case recoverRollback:
		if out, rerr := runHelm(ctx, kubeconfigPath, "rollback", kro.ReleaseName,
			strconv.Itoa(target), "--namespace", kro.Namespace); rerr != nil {
			return fmt.Errorf("recover pending %q: helm rollback %s to %d: %w\n%s",
				status, kro.ReleaseName, target, rerr, string(out))
		}
	case recoverUninstall:
		if out, rerr := runHelm(ctx, kubeconfigPath, "uninstall", kro.ReleaseName,
			"--namespace", kro.Namespace, "--ignore-not-found"); rerr != nil {
			return fmt.Errorf("recover pending %q: helm uninstall %s: %w\n%s",
				status, kro.ReleaseName, rerr, string(out))
		}
	case recoverNone:
		// Healthy (deployed/failed) — helm upgrade --install handles it directly.
	}
	return nil
}

// recoveryAction is the remediation planRecovery selects for a release status.
type recoveryAction int

const (
	recoverNone recoveryAction = iota
	recoverRollback
	recoverUninstall
)

// planRecovery maps a helm release status to the remediation needed before a
// fresh upgrade can run. Pure (no I/O) so the decision logic is unit-testable;
// the revision/hasDeployed inputs come from lastDeployedRevision.
func planRecovery(status string, lastDeployedRev int, hasDeployed bool) (recoveryAction, int) {
	switch status {
	case "pending-upgrade", "pending-rollback":
		if hasDeployed {
			return recoverRollback, lastDeployedRev
		}
		// No good revision to fall back to — treat like a failed first install.
		return recoverUninstall, 0
	case "pending-install":
		return recoverUninstall, 0
	default:
		return recoverNone, 0
	}
}

// helmReleaseStatus returns the release's current status (e.g. "deployed",
// "pending-upgrade"). A non-nil error means the release was not found or its
// status could not be read — callers treat that as "nothing to recover".
func helmReleaseStatus(ctx context.Context, kubeconfigPath string, kro v1alpha1.KroSpec) (string, error) {
	out, err := runHelm(ctx, kubeconfigPath, "status", kro.ReleaseName,
		"--namespace", kro.Namespace, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("helm status %s: %w\n%s", kro.ReleaseName, err, string(out))
	}
	var s struct {
		Info struct {
			Status string `json:"status"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return "", fmt.Errorf("decode helm status %s: %w", kro.ReleaseName, err)
	}
	return s.Info.Status, nil
}

// lastDeployedRevision returns the highest revision whose status is a known-good
// rollback target (deployed or superseded), and whether such a revision exists.
func lastDeployedRevision(ctx context.Context, kubeconfigPath string, kro v1alpha1.KroSpec) (int, bool) {
	out, err := runHelm(ctx, kubeconfigPath, "history", kro.ReleaseName,
		"--namespace", kro.Namespace, "-o", "json")
	if err != nil {
		return 0, false
	}
	var hist []struct {
		Revision int    `json:"revision"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(out, &hist); err != nil {
		return 0, false
	}
	best, found := 0, false
	for _, h := range hist {
		if (h.Status == "deployed" || h.Status == "superseded") && h.Revision > best {
			best, found = h.Revision, true
		}
	}
	return best, found
}

// runHelm runs the helm CLI, pointing it at the runtime cluster. An empty
// kubeconfigPath means "in-cluster runtime": helm runs with no KUBECONFIG
// override so it uses the pod's in-cluster service account.
func runHelm(ctx context.Context, kubeconfigPath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "helm", args...)
	if kubeconfigPath != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	}
	return cmd.CombinedOutput()
}
