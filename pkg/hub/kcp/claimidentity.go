/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/kcppaths"
)

// Cross-provider claim identity verification.
//
// A provider that claims another provider's resources pins that provider's
// identityHash into its OWN APIExport, and kcp will only honour a binding whose
// claim carries the identity its export declares. But kcp keys permissionClaims
// on (group, resource) alone —
//
//	// +listType=map
//	// +listMapKey=group
//	// +listMapKey=resource
//
// — so an APIExport can name exactly ONE identity per claimed resource. A
// platform provider therefore claims from exactly one copy of its dependency,
// forever. Swap that dependency for a self-hosted one and the pin is stale:
// kcp stops projecting the claimed resources into the dependent's virtual
// workspace, and the dependent sees the GVR simply not served.
//
// The failure is invisible. The binding reports Ready with
// PermissionClaimsValid=True — the claim is well-formed and does resolve to a
// real export, just not the one this workspace binds — so the only symptom is a
// 404 in the dependent provider's logs, forever. That is worth a check at Enable
// rather than a trace through three workspaces.
//
// Nothing here can repair the mismatch: the identity lives on the dependent's
// APIExport, which is platform-wide, so fixing it for one org would break every
// other. What this does is refuse to create a binding that cannot work, and say
// exactly which value is wrong. See docs/byo-providers.md.

// Where a stale pin CAN be repaired automatically, RepointClaimIdentities does
// it: it rewrites the dependent's own APIExport to the identity of the copy the
// workspace actually binds, so a Disable/Enable after swapping a dependency
// simply works. That is only sound when the dependent's export serves a single
// tenant — an org-scoped, self-hosted copy. Repointing a platform-wide export
// because one org swapped its dependency would silently redirect every other
// org's claims to an export they do not bind, so that case is refused instead.

// ClaimIdentityMismatch reports a claim whose pinned identity does not match the
// export actually serving that group in the target workspace.
type ClaimIdentityMismatch struct {
	// Group and Resource identify the claim.
	Group    string
	Resource string
	// Declared is the identity the binding's export pins for this claim.
	Declared string
	// Actual is the identity of the export the workspace really binds for
	// Group, and therefore the value the claim would have to carry to work.
	Actual string
	// ServingExportPath is the workspace path of that export — the copy the
	// dependent would have to be repointed at.
	ServingExportPath string
}

// ErrClaimIdentityMismatch is the sentinel callers match on to turn this into a
// client-visible refusal rather than a 500.
var ErrClaimIdentityMismatch = errors.New("permission claim identity does not match the bound provider")

// ClaimIdentityMismatchError carries every mismatch found for one Enable.
type ClaimIdentityMismatchError struct {
	Provider   string
	Mismatches []ClaimIdentityMismatch
	// Repointable records whether the hub could have fixed this itself. It is
	// false for a platform-wide export, whose single identity per claimed
	// resource belongs to every org at once.
	Repointable bool
}

func (e *ClaimIdentityMismatchError) Is(target error) bool { return target == ErrClaimIdentityMismatch }

func (e *ClaimIdentityMismatchError) Error() string {
	parts := make([]string, 0, len(e.Mismatches))
	for _, m := range e.Mismatches {
		parts = append(parts, fmt.Sprintf(
			"%s/%s is pinned to identity %s but this workspace binds %s, whose identity is %s",
			m.Group, m.Resource, short(m.Declared), m.ServingExportPath, short(m.Actual)))
	}
	// The remedy differs by scope, and getting it wrong is expensive either way,
	// so name it rather than leaving the reader to infer it.
	remedy := "its APIExport is shared by every organization, so the hub cannot repoint it for one of them — " +
		"either run this provider self-hosted alongside the dependency it claims, or repoint the platform copy for everyone"
	return fmt.Sprintf("provider %q claims resources from a different copy of its dependency than this workspace uses: %s (%s)",
		e.Provider, strings.Join(parts, "; "), remedy)
}

// short trims an identity hash to a comparable-at-a-glance prefix. Full hashes
// are 64 hex characters and every one of them looks the same in a log line.
func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12] + "…"
}

// servingExport is the export a workspace binds for a particular API group.
type servingExport struct {
	path     string
	name     string
	identity string
}

// verifyClaimIdentities compares the identity each claim is pinned to against
// the identity of the export the workspace actually binds for that claim's
// group.
//
// declared maps "group/resource" to the identity the binding's own export pins,
// as read from that export by exportClaimIdentities.
//
// Claims whose group is not served by any binding in the workspace are skipped:
// a claim on a resource nobody provides yet is not a mismatch, it is a claim
// that will start working once the dependency is enabled — which the caller's
// dependency gate covers.
func (b *Bootstrapper) verifyClaimIdentities(
	ctx context.Context,
	orgUUID, wsUUID, providerName, exportPath, exportName string,
	claims []ProviderClaim,
	declared map[string]string,
) (map[string]string, error) {
	// Only first-party claims carry an identity worth checking. Core types
	// (secrets, serviceaccounts, RBAC) have no identity by construction, and a
	// claim we have no pinned value for cannot be compared against anything.
	needed := map[string]bool{}
	for _, c := range claims {
		if !c.Accepted {
			continue
		}
		if declared[c.Group+"/"+c.Resource] != "" {
			needed[c.Group] = true
		}
	}
	if len(needed) == 0 {
		return declared, nil
	}

	serving, err := b.workspaceGroupExports(ctx, orgUUID, wsUUID, needed)
	if err != nil {
		return nil, err
	}

	mismatches := compareClaimIdentities(claims, declared, serving)
	if len(mismatches) == 0 {
		return declared, nil
	}

	// A platform-wide export is shared by every org, so its single identity per
	// claimed resource is not this workspace's to change. Say precisely what is
	// wrong instead of binding something that cannot work.
	if !orgScopedExport(exportPath) {
		return nil, &ClaimIdentityMismatchError{Provider: providerName, Mismatches: mismatches, Repointable: false}
	}

	// Single-tenant export: the pin is simply stale relative to the dependency
	// this org now runs, so bring it up to date and carry on. This is what makes
	// "swap the dependency, then Disable/Enable" work without hand-editing a
	// hash into a values file.
	changed, err := b.repointExportClaims(ctx, exportPath, exportName, mismatches)
	if err != nil {
		return nil, err
	}
	logger := klog.FromContext(ctx)
	for _, m := range mismatches {
		logger.Info("Repointed permission claim to the export this workspace binds",
			"provider", providerName, "export", exportName, "exportPath", exportPath,
			"claim", m.Group+"/"+m.Resource,
			"from", short(m.Declared), "to", short(m.Actual), "servingExport", m.ServingExportPath)
	}
	if !changed {
		// Nothing to write means the export already carried these values, so the
		// mismatch came from somewhere this cannot fix. Refuse rather than bind
		// a claim kcp will drop as unexpected.
		return nil, &ClaimIdentityMismatchError{Provider: providerName, Mismatches: mismatches, Repointable: false}
	}

	// The binding must carry exactly what the export now declares — kcp
	// intersects the two on (resource, group, identityHash) and silently drops
	// anything that does not match on all three.
	updated := make(map[string]string, len(declared))
	for k, v := range declared {
		updated[k] = v
	}
	for _, m := range mismatches {
		updated[m.Group+"/"+m.Resource] = m.Actual
	}
	return updated, nil
}

// compareClaimIdentities is the decision itself, separated from the two cluster
// reads that feed it so it can be exercised directly. A false positive blocks
// Enable on a working configuration, which is worse than the bug this catches,
// so every "cannot tell" case stays silent.
func compareClaimIdentities(claims []ProviderClaim, declared map[string]string, serving map[string]servingExport) []ClaimIdentityMismatch {
	var mismatches []ClaimIdentityMismatch
	for _, c := range claims {
		// A rejected claim grants nothing, so its identity cannot matter.
		if !c.Accepted {
			continue
		}
		pinned := declared[c.Group+"/"+c.Resource]
		if pinned == "" {
			continue
		}
		got, ok := serving[c.Group]
		// Not served here yet (the dependency gate covers that, and reporting it
		// as an identity error would only obscure a clear "enable X first"), or
		// its identity is not stamped yet — kcp does that asynchronously, and
		// failing mid-provisioning would make Enable flaky.
		if !ok || got.identity == "" {
			continue
		}
		if got.identity != pinned {
			mismatches = append(mismatches, ClaimIdentityMismatch{
				Group:             c.Group,
				Resource:          c.Resource,
				Declared:          pinned,
				Actual:            got.identity,
				ServingExportPath: got.path,
			})
		}
	}
	sort.Slice(mismatches, func(i, j int) bool {
		if mismatches[i].Group != mismatches[j].Group {
			return mismatches[i].Group < mismatches[j].Group
		}
		return mismatches[i].Resource < mismatches[j].Resource
	})
	return mismatches
}

// workspaceGroupExports maps each requested API group to the export the
// workspace binds for it, with that export's identity resolved.
//
// The mapping comes from APIBinding.status.boundResources, which is what kcp
// actually serves in the workspace — not from the registry, which records what
// the platform believes it installed. When those two disagree this check exists
// precisely to notice.
func (b *Bootstrapper) workspaceGroupExports(ctx context.Context, orgUUID, wsUUID string, groups map[string]bool) (map[string]servingExport, error) {
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return nil, fmt.Errorf("creating child workspace client: %w", err)
	}
	list, err := wsClient.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing APIBindings in %s/%s: %w", orgUUID, wsUUID, err)
	}

	out := map[string]servingExport{}
	for _, item := range list.Items {
		if phase, _, _ := unstructured.NestedString(item.Object, "status", "phase"); phase != "Bound" {
			continue
		}
		path, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "path")
		name, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "name")
		if path == "" || name == "" {
			continue
		}
		bound, _, _ := unstructured.NestedSlice(item.Object, "status", "boundResources")
		for _, br := range bound {
			m, ok := br.(map[string]any)
			if !ok {
				continue
			}
			group, _ := m["group"].(string)
			if !groups[group] {
				continue
			}
			if _, seen := out[group]; seen {
				continue
			}
			identity, ierr := b.apiExportIdentity(ctx, path, name)
			if ierr != nil {
				return nil, ierr
			}
			out[group] = servingExport{path: path, name: name, identity: identity}
		}
	}
	return out, nil
}

// repointExportClaims rewrites exportName's permissionClaims so each listed
// mismatch carries the identity the workspace actually binds, and reports
// whether anything changed.
//
// Safe only for a single-tenant (org-scoped) export — see the package comment
// above. kcp keys permissionClaims on (group, resource) alone, so there is no
// way to hold both identities at once; this really is a repoint, not an
// addition.
func (b *Bootstrapper) repointExportClaims(ctx context.Context, exportPath, exportName string, mismatches []ClaimIdentityMismatch) (bool, error) {
	exportClient, err := dynamic.NewForConfig(configForPath(b.config, exportPath))
	if err != nil {
		return false, fmt.Errorf("creating export workspace client for %s: %w", exportPath, err)
	}
	ex, err := exportClient.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("getting APIExport %q in %s: %w", exportName, exportPath, err)
	}

	want := make(map[string]string, len(mismatches))
	for _, m := range mismatches {
		want[m.Group+"/"+m.Resource] = m.Actual
	}

	claims, _, err := unstructured.NestedSlice(ex.Object, "spec", "permissionClaims")
	if err != nil {
		return false, fmt.Errorf("reading permissionClaims of %q in %s: %w", exportName, exportPath, err)
	}
	changed := false
	for i, raw := range claims {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		g, _ := m["group"].(string)
		r, _ := m["resource"].(string)
		target, ok := want[g+"/"+r]
		if !ok {
			continue
		}
		if cur, _ := m["identityHash"].(string); cur == target {
			continue
		}
		m["identityHash"] = target
		claims[i] = m
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := unstructured.SetNestedSlice(ex.Object, claims, "spec", "permissionClaims"); err != nil {
		return false, fmt.Errorf("setting permissionClaims of %q in %s: %w", exportName, exportPath, err)
	}
	if _, err := exportClient.Resource(apiExportGVR).Update(ctx, ex, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("repointing permissionClaims of %q in %s: %w", exportName, exportPath, err)
	}
	return true, nil
}

// orgScopedExport reports whether an export path belongs to a single org, and
// is therefore one this hub may repoint without affecting anyone else.
func orgScopedExport(exportPath string) bool {
	return strings.HasPrefix(exportPath, kcppaths.TenantsParent+":")
}

// apiExportIdentity reads status.identityHash off an APIExport. An export that
// exists but has no hash yet is reported as empty rather than an error — kcp
// stamps it asynchronously, and a caller mid-provisioning should not be told
// its claims are wrong.
func (b *Bootstrapper) apiExportIdentity(ctx context.Context, exportPath, exportName string) (string, error) {
	exportClient, err := dynamic.NewForConfig(configForPath(b.config, exportPath))
	if err != nil {
		return "", fmt.Errorf("creating export workspace client for %s: %w", exportPath, err)
	}
	ex, err := exportClient.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting APIExport %q in %s: %w", exportName, exportPath, err)
	}
	hash, _, _ := unstructured.NestedString(ex.Object, "status", "identityHash")
	return hash, nil
}
