// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package identityclient

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ServiceAccountPrefix marks a provider-asserted scoped identity's
// ServiceAccount, as opposed to a workload identity (railgrid-wi-).
const ServiceAccountPrefix = "railgrid-si-"

// ServiceAccountName is the deterministic ServiceAccount name the hub gives the
// scoped identity it mints for owner. Only the provider, group, kind, name and
// UID take part; version, resource and cluster do not.
//
// The hub uses this function to name the account, so a provider that serves a
// verb can recognize a specific owner's identity by its username alone. The
// hub mints only for the provider it authenticated, and only for an owner it
// verified exists with that UID. Anyone who can create ServiceAccounts in the
// workspace can still create an account with the same name, so the name does
// not prove more than a workspace administrator could already do.
func ServiceAccountName(owner Owner) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		owner.Provider, owner.Group, owner.Kind, owner.Name, owner.UID,
	}, "\x00")))
	return ServiceAccountPrefix + hex.EncodeToString(sum[:20])
}
