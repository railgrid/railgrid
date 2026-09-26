/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

// The actor — who owns a thread, an attachment, an approval decision, an audit
// entry — is the identity kcp authenticated, never a header this provider
// merely receives.
//
// X-Railgrid-User used to be that actor, and then a SelfSubjectReview with the
// caller's bearer was. A verb no longer carries a bearer at all: a kcp shard
// authenticates the caller, authorizes `create` on {resource}/{verb} with
// ordinary RBAC, and reverse-proxies the request to this provider with the
// caller's name, groups and extras stamped into requestheader headers
// (X-Remote-User, X-Remote-Group, X-Remote-Extra-*). Those headers are
// believed only because the connection is one the provider already trusts —
// the DataPlaneEndpointSlice publishes its shard-facing address, and the hub
// strips X-Remote-* from everything it forwards — and serve's subresource
// adapter is the one place that reads them into the request context
// (dataplane.ProxiedIdentityFrom). The identity there is the same subject kcp
// authorized the verb for, so it is what every ownership check keys on.
//
// X-Railgrid-User remains as a display label only.

import (
	"context"
	"errors"

	"github.com/railgrid/provider-sdk/dataplane"
)

// actorLookup is a TEST SEAM over the stamped identity: it maps the caller the
// shard stamped (or a test stamped) to the username the handlers see.
// Production leaves Server.tenantActors nil and uses the stamp verbatim.
type actorLookup func(ctx context.Context, clusterID string, caller dataplane.ProxiedIdentity) (string, error)

// errNoActorLookup is the userErr when the request carries no stamped caller:
// it did not come through serve's subresource adapter, or the shard sent no
// X-Remote-User. Anonymous is not a fallback.
var errNoActorLookup = errors.New("no authenticated caller on the request (no kcp-stamped identity)")
