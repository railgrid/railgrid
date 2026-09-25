/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package crossprovider names App Studio's dependencies: the APIExports they
// serve, the API groups and kinds App Studio requires from them, and the one
// error distinction the teardown paths turn on.
//
// A cross-provider call no longer needs to know which PROVIDER serves a
// dependency in a workspace. App Studio reaches a dependency's kinds and verbs
// through its OWN APIExport virtual workspace, under the claims declared in
// manifest.yaml spec.requires[].resources[]; kcp resolves each claim per
// consumer workspace against whichever copy of the dependency that workspace
// bound. The /services/providers/{name}/ segment that once had to be read off
// the tenant's APIBinding is gone with the grammar that carried it.
package crossprovider

import (
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// InfrastructureAPIExport is the APIExport the infrastructure provider
	// serves. App Studio's dependency on it is the manifest.yaml spec.requires
	// entry naming `provider: infrastructure`; this is the name of the export a workspace binds
	// when it enables that dependency, whichever copy of the provider it is.
	InfrastructureAPIExport = "infrastructure.providers.railgrid.ai"
	// CodeAPIExport is the APIExport the code provider serves.
	CodeAPIExport = "code.providers.railgrid.ai"
)

// APIBindingsGVR is kcp's APIBinding, one per enabled provider in the tenant's
// workspace.
var APIBindingsGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

// ProviderNameForExport is the dependency's label for an APIExport: the
// export's first label, which is the name the hub enables a provider under.
// It labels a dependency in messages and the project view; it is never a URL
// segment.
func ProviderNameForExport(exportName string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(exportName), ".")
	return name
}

// ClaimUnaccepted reports whether err means the workspace does not serve a
// CLAIMED kind through App Studio's APIExport virtual workspace — its
// APIBinding has not accepted the permission claim, or has not caught up with
// it yet — rather than that a particular object is missing.
//
// The distinction is load-bearing on the teardown path. kcp answers a resource
// that is not part of a workspace's API surface with a RESTMapper miss, and an
// earlier attempt to reach these kinds over the virtual workspace read that as
// "already gone" and released a Project's finalizer over live instances. A
// caller that would otherwise treat NotFound as success must ask this first,
// keep the finalizer, log, and let the controller's backoff retry: the answer
// changes by itself the moment the binding accepts the claim.
//
// Forbidden is included because a workspace whose binding accepted the claim
// for a narrower verb set fails the same way and wants the same retry, not a
// silent success.
func ClaimUnaccepted(err error) bool {
	if err == nil {
		return false
	}
	return meta.IsNoMatchError(err) || apierrors.IsForbidden(err)
}
