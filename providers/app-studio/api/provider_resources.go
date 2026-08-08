/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
)

// Provider-resource bindings are CONVERGED BY THE PROJECT RECONCILER
// (controller/project), not by this package: handlers write Project spec and
// the reconciler creates/updates/deletes the instances under the provider's
// claimed identity, mirroring status into Project.status.environments.
//
// What remains here is read-through status (so a GET right after a spec write
// shows fresh instance state without waiting for the mirror) and the
// best-effort teardown used when project CREATION fails halfway (the Project
// CR may not have gained the reconciler's finalizer yet). The desired-state
// and status-fold logic itself lives in the bindings package, shared with the
// reconciler so the two can never disagree.

func (s *Server) deleteProjectProviderResources(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) error {
	if c == nil || p == nil {
		return nil
	}
	for _, env := range p.Spec.Environments {
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			gvr, err := projectProviderResourceGVR(binding.ResourceRef)
			if err != nil {
				return err
			}
			values, err := projectProviderBindingValues(binding)
			if err != nil {
				return err
			}
			name := projectProviderBindingResourceName(p, binding, values, id)
			if name == "" {
				continue
			}
			// Deleting the instance is enough: the infrastructure provider's
			// kro template owns the runtime namespace and garbage-collects it
			// (and every materialized workload) when the instance goes away.
			err = c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Delete(ctx, name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// projectWithLiveBindingStatus enriches a Project with freshly-observed live
// binding state for the response payload. Read-only — never patches the CR
// (the reconciler owns the durable mirror).
func projectWithLiveBindingStatus(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) *aiv1alpha1.Project {
	if c == nil || p == nil {
		return p
	}
	statuses := projectLiveEnvironmentStatuses(ctx, c, p, id)
	if len(statuses) == 0 {
		return p
	}
	next := p.DeepCopy()
	next.Status.Environments = bindings.MergeEnvironmentStatuses(next.Status.Environments, statuses)
	return next
}

func projectLiveEnvironmentStatuses(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, id identity) []aiv1alpha1.ProjectEnvironmentStatus {
	if c == nil || p == nil {
		return nil
	}
	statuses := []aiv1alpha1.ProjectEnvironmentStatus{}
	for _, env := range p.Spec.Environments {
		if env.Mode != aiv1alpha1.ProjectEnvironmentModeLive {
			continue
		}
		var bindingStatuses []aiv1alpha1.ProjectProviderBindingStatus
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			bindingStatuses = append(bindingStatuses, projectProviderBindingStatus(ctx, c, p, binding, id))
		}
		if len(bindingStatuses) == 0 {
			continue
		}
		statuses = append(statuses, bindings.FoldEnvironment(env, bindingStatuses))
	}
	return statuses
}

func projectProviderBindingStatus(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, id identity) aiv1alpha1.ProjectProviderBindingStatus {
	gvr, err := projectProviderResourceGVR(binding.ResourceRef)
	if err != nil {
		return bindings.InvalidStatus(binding)
	}
	values, err := projectProviderBindingValues(binding)
	if err != nil {
		return bindings.InvalidStatus(binding)
	}
	name := projectProviderBindingResourceName(p, binding, values, id)
	if name == "" {
		return bindings.InvalidStatus(binding)
	}
	obj, err := c.Resource(providerBindingResource(gvr, binding.ResourceRef.Kind), "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return bindings.StatusFromObject(binding, nil)
	}
	return bindings.StatusFromObject(binding, obj)
}

// Thin delegations — the shared bindings package owns the logic; these names
// keep the api package's existing call sites and tests stable.

func projectProviderResourceGVR(ref *aiv1alpha1.ProjectProviderResourceReference) (schema.GroupVersionResource, error) {
	return bindings.GVR(ref)
}

func projectProviderBindingValues(binding aiv1alpha1.ProjectProviderBindingSpec) (map[string]any, error) {
	return bindings.Values(binding)
}

func projectProviderBindingResourceName(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, values map[string]any, _ identity) string {
	return bindings.ResourceName(p, binding, values)
}
