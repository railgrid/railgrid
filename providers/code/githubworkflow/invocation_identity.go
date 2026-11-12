// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"strings"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/identityclient"
)

// The Agents provider does not hold a permission claim on Code, which would make
// Code a prerequisite for enabling Agents anywhere. An Agents tool calls
// pullrequests/{name}/operate as the hub-minted scoped identity of the Run
// executing the invocation. That identity lives in the tenant workspace and is
// granted get and operate on the one PullRequest only.
const (
	invokingRunProvider = "agents"
	invokingRunGroup    = "agents.railgrid.ai"
	invokingRunKind     = "Run"
)

// trustedTaskConsumer reports whether a task-mode operation comes from a
// coordinator Code trusts to hold scope proofs. That is either another
// provider's ServiceAccount, acting through its own export (Factory), or the
// scoped identity the hub minted for the Agents Run the operation names.
func trustedTaskConsumer(identity dataplane.ProxiedIdentity, cluster string, in Operation) bool {
	return identity.IsForeignProvider(cluster) || invokingRunIdentity(identity, cluster, in)
}

// invokingRunIdentity reports whether the caller is the hub-minted identity of
// the Run named by in.InvocationRun and in.InvocationRunUID, in this workspace.
//
// The hub names that ServiceAccount after its owner (identityclient.
// ServiceAccountName), mints only for the provider it authenticated, and only
// for an owner it found with that UID. A workspace administrator could create
// an account with the same name, but such a principal can already read the
// Connection Secret the scope proofs derive from (see README "Trust boundary").
func invokingRunIdentity(identity dataplane.ProxiedIdentity, cluster string, in Operation) bool {
	if in.InvocationRun == "" || in.InvocationRunUID == "" || cluster == "" || identity.ClusterName() != cluster {
		return false
	}
	parts := strings.Split(identity.User, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] == "" {
		return false
	}
	return parts[3] == identityclient.ServiceAccountName(identityclient.Owner{
		Provider: invokingRunProvider, Group: invokingRunGroup, Kind: invokingRunKind,
		Name: in.InvocationRun, UID: in.InvocationRunUID,
	})
}
