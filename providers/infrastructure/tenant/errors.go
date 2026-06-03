// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package tenant talks to the tenant's kcp workspace via the
// kedge-provider-kubeconfig the hub catalog controller mints for this
// provider. The base kubeconfig targets the provider's own workspace
// (root:kedge:providers:infrastructure); per-tenant operations derive
// a scoped client by swapping the cluster path in the host URL — the
// recipe lives in pkg/hub/providers/provision.go around clientFor().
//
// The single supported tenant-side read in v1 is the `cloud-credentials`
// Secret, gated by the permissionClaim declared on the APIExport. See
// providers/infrastructure/manifest.yaml.
package tenant

import "errors"

// ErrCredentialsMissing means the tenant has APIBound this provider's
// APIExport but has not created a `cloud-credentials` Secret yet. The
// HTTP handler maps this to a 404 with reason:CloudCredentialsMissing
// so the portal can redirect to the MissingCredentialsPage.
var ErrCredentialsMissing = errors.New("cloud-credentials secret not found in tenant workspace")

// ErrAPIBindingMissing means the tenant has NOT APIBound this
// provider's APIExport, so the provider's SA token gets a 403 trying to
// read their secrets. The handler maps this to a 403 with
// reason:APIBindingMissing.
var ErrAPIBindingMissing = errors.New("tenant has no APIBinding to this provider's APIExport")
