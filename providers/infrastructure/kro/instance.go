// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import "errors"

// ErrInstanceNotFound is returned when no instance with that name exists
// for the tenant, OR when the instance exists but belongs to a different
// tenant (we collapse the two to avoid leaking instance existence across
// tenants).
var ErrInstanceNotFound = errors.New("instance not found")

func credentialsSecretName(instanceName string) string {
	return "cloud-credentials-" + instanceName
}

// CredentialsSecretName is the public form: the per-instance Secret on the
// runtime cluster that carries bridged credentials (cloud creds and/or the
// OIDC client secret under key oidc_client_secret). The Application
// controller creates it and stamps spec.credentialsSecretName with this
// value so the RGD references the same name.
func CredentialsSecretName(instanceName string) string { return credentialsSecretName(instanceName) }
