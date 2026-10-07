/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/internal/projectidentity"
)

// identityToken returns the project's current token, minting or refreshing it
// through the hub. The rule set is shared with App Studio's API so a save,
// revoke, or removal updates one Project-owned identity consistently.
func (r *Reconciler) identityToken(ctx context.Context, clusterName string, p *aiv1alpha1.Project) (string, error) {
	if !r.Identities.Enabled() {
		return "", nil
	}
	current, err := r.currentProjectForIdentity(ctx, clusterName, p)
	if err != nil {
		return "", err
	}
	return r.Identities.TokenVersioned(ctx, projectidentity.Owner(current, clusterName), current.Generation, projectidentity.Rules(current))
}

func (r *Reconciler) currentProjectForIdentity(ctx context.Context, clusterName string, project *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	if project == nil {
		return nil, fmt.Errorf("project identity requires a Project")
	}
	if r.Manager == nil {
		// Unit fixtures and REST-only use have no multicluster manager. Production
		// reconciles always have one and re-read the authoritative object below.
		return project, nil
	}
	cl, err := r.Manager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return nil, fmt.Errorf("resolve cluster %q before Project identity mint: %w", clusterName, err)
	}
	current := &aiv1alpha1.Project{}
	if err := cl.GetAPIReader().Get(ctx, types.NamespacedName{Name: project.Name}, current); err != nil {
		return nil, fmt.Errorf("read current Project before identity mint: %w", err)
	}
	if current.UID == "" || current.UID != project.UID {
		return nil, fmt.Errorf("project UID changed before identity mint")
	}
	if !current.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("project is deleting; refusing to mint its identity")
	}
	return current, nil
}

// releaseIdentity revokes the project's identity now rather than waiting for
// the hub's sweep to notice the Project is gone (up to one token TTL later).
func (r *Reconciler) releaseIdentity(ctx context.Context, clusterName string, p *aiv1alpha1.Project) error {
	if !r.Identities.Enabled() {
		return nil
	}
	return r.Identities.Release(ctx, projectidentity.Owner(p, clusterName))
}

func projectIdentityRules(p *aiv1alpha1.Project) []rbacv1.PolicyRule {
	return projectidentity.Rules(p)
}

func projectPendingCommitRef(p *aiv1alpha1.Project) string {
	if p == nil || p.Status.Workspace == nil || p.Status.Workspace.PendingCommit == nil {
		return ""
	}
	return strings.TrimSpace(p.Status.Workspace.PendingCommit.Name)
}
