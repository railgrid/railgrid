/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/faroshq/provider-sdk/tenantaccess"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// Per-project ServiceAccount identity.
//
// Repository commits and instance lifecycling run in this controller, long
// after the request that caused them — so they cannot borrow the editing
// user's bearer (request-scoped, and the same trap the agents provider hit).
// Instead each project gets its own ServiceAccount in the tenant workspace;
// the controller uses its token for hub MCP calls AND as the identity behind
// the tenant-path client that lifecycles instances/repositories. That client
// goes through the workspace's own bindings, so it reaches whichever copy of
// the infrastructure/code provider the workspace binds — platform or
// self-hosted — with no permission-claim identity pinning involved (see
// package tenantaccess).
//
// SCOPE, STATED PLAINLY: this token can manage the workspace's infrastructure
// instances and read/write its code resources, it does not expire, and
// revoking it means deleting the ServiceAccount — which happens
// automatically, because the objects are owned by the Project and
// garbage-collected with it.

func identityName(project string) string { return "faros-appstudio-" + project }

// projectOwnerRef points identity objects at the Project so kcp
// garbage-collects them with it.
func projectOwnerRef(p *aiv1alpha1.Project) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: aiv1alpha1.SchemeGroupVersion.String(),
		Kind:       "Project",
		Name:       p.Name,
		UID:        p.UID,
		Controller: &controller,
	}
}

// ensureIdentity provisions (idempotently) the project's ServiceAccount,
// RBAC, and token Secret, and returns the token. An empty token with a nil
// error means "not ready yet, requeue".
func (r *Reconciler) ensureIdentity(ctx context.Context, c client.Client, p *aiv1alpha1.Project) (string, error) {
	rules := []rbacv1.PolicyRule{
		{
			// Full lifecycle: the reconciler creates, converges, and (on
			// Project deletion) deletes the project's environment instances.
			APIGroups: []string{"infrastructure.faros.sh"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
		{
			// No delete: repositories hold user code and deliberately survive
			// project deletion.
			APIGroups: []string{"code.faros.sh"},
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
	}
	return tenantaccess.EnsureIdentity(ctx, c, identityName(p.Name), []metav1.OwnerReference{projectOwnerRef(p)}, rules)
}
