/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package workloadidentity contains the stable naming contract for hub-minted
// workload ServiceAccounts. It is shared by the hub that creates the identity
// and providers that verify the stamped kcp username.
package workloadidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const serviceAccountNamePrefix = "railgrid-wi-"

// Scope is the verified owner tuple used to derive one workload identity.
// Every field participates in the name so a changed tenant, project
// incarnation, environment, or instance produces a different credential
// subject.
type Scope struct {
	TenantPath  string
	Project     string
	ProjectUID  string
	Environment string
	Instance    string
}

// ServiceAccountName returns the deterministic ServiceAccount name for scope.
// Callers must first validate the scope from authoritative Project state; the
// name itself is not an attestation.
func ServiceAccountName(scope Scope) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		scope.TenantPath,
		scope.Project,
		scope.ProjectUID,
		scope.Environment,
		scope.Instance,
	}, "\x00")))
	return serviceAccountNamePrefix + hex.EncodeToString(sum[:20])
}

// IsServiceAccountName reports whether name uses the hub-managed workload
// identity prefix. It is useful for fail-closed handling when an incoming kcp
// subject claims to be a workload identity but does not match current owner
// state.
func IsServiceAccountName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), serviceAccountNamePrefix)
}
