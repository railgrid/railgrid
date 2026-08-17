/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// crdGVR addresses CustomResourceDefinitions on the runtime cluster.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// EnsureKroChartCRDs applies the chart's crds/ directory content to the
// runtime cluster. Helm only installs crds/-dir CRDs on FIRST install and
// never upgrades them, so without this a chart version bump (or the fork →
// upstream switch) leaves stale RGD CRDs behind and kro's newer status
// fields are silently pruned.
//
// The chart is pulled and untarred rather than read via `helm show crds`:
// depending on the helm version, OCI pull chatter ("Pulled: …") lands on
// stdout and corrupts the YAML stream.
func EnsureKroChartCRDs(ctx context.Context, runtimeConfig *rest.Config, kubeconfigPath string, kro v1alpha1.KroSpec) error {
	tmp, err := os.MkdirTemp("", "kro-chart-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	out, err := runHelm(ctx, kubeconfigPath, "pull", kro.Chart, "--version", kro.Version, "--untar", "--untardir", tmp)
	if err != nil {
		return fmt.Errorf("helm pull %s: %w\n%s", kro.Chart, err, string(out))
	}
	crdFiles, err := filepath.Glob(filepath.Join(tmp, "*", "crds", "*.yaml"))
	if err != nil {
		return fmt.Errorf("glob chart crds: %w", err)
	}

	dyn, err := dynamic.NewForConfig(runtimeConfig)
	if err != nil {
		return fmt.Errorf("runtime dynamic client: %w", err)
	}

	var raw bytes.Buffer
	for _, f := range crdFiles {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", f, rerr)
		}
		raw.Write(data)
		raw.WriteString("\n---\n")
	}

	decoder := utilyaml.NewYAMLOrJSONDecoder(&raw, 4096)
	for {
		obj := &unstructured.Unstructured{}
		if derr := decoder.Decode(obj); derr != nil {
			if strings.Contains(derr.Error(), "EOF") {
				break
			}
			return fmt.Errorf("decode chart CRDs: %w", derr)
		}
		if obj.GetName() == "" || obj.GetKind() != "CustomResourceDefinition" {
			continue
		}
		existing, gerr := dyn.Resource(crdGVR).Get(ctx, obj.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(gerr) {
			if _, cerr := dyn.Resource(crdGVR).Create(ctx, obj, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
				return fmt.Errorf("create CRD %s: %w", obj.GetName(), cerr)
			}
			continue
		}
		if gerr != nil {
			return fmt.Errorf("get CRD %s: %w", obj.GetName(), gerr)
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		if _, uerr := dyn.Resource(crdGVR).Update(ctx, obj, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update CRD %s: %w", obj.GetName(), uerr)
		}
	}
	return nil
}

// EnsureKroRelease install/upgrades the kro Helm release on the runtime cluster
// (addressed by runtimeKubeconfigPath) with values derived from the CR. It
// shells out to the `helm` binary — using the Go SDK would drag k8s.io/kubernetes
// into this module.
//
// kro runs SINGLE-CLUSTER: with the flattened Instance kind, tenants author
// instances in kcp and the infrastructure provider's instance controller
// materializes the per-template kro CRs on this runtime cluster — kro never
// watches kcp anymore, so the multicluster/kcp-apiexport and
// deploy-to-local-runtime modes are off (their flag defaults).
func EnsureKroRelease(ctx context.Context, runtimeKubeconfigPath string, kro v1alpha1.KroSpec) error {
	// Clear any release wedged in a pending-* state by an interrupted prior helm
	// operation before attempting the upgrade — otherwise helm refuses with
	// "another operation (install/upgrade/rollback) is in progress" and the
	// operator's retries loop forever (nothing else clears pending state).
	if err := recoverPendingRelease(ctx, runtimeKubeconfigPath, kro); err != nil {
		return err
	}

	// --reset-values: without it, an upgrade that passes no --set flags
	// silently REUSES the previous release's values — which is how the
	// retired fork's image pin (kro-multicluster:v0.0.1-mc.7) survived the
	// chart switch to upstream and kept the fork binary running. The
	// desired values are always exactly "chart defaults + the CR's
	// overrides below", never whatever the last release happened to carry.
	args := []string{
		"upgrade", "--install", kro.ReleaseName, kro.Chart,
		"--version", kro.Version,
		"--namespace", kro.Namespace, "--create-namespace",
		"--reset-values",
	}
	// Image overrides are opt-in: the upstream chart's own defaults
	// (registry.k8s.io/kro/kro at the chart's appVersion) are correct.
	if kro.Image.Repository != "" {
		args = append(args, "--set", "image.repository="+kro.Image.Repository)
	}
	if kro.Image.Tag != "" {
		args = append(args, "--set", "image.tag="+kro.Image.Tag)
	}
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
