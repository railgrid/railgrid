// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// Snapshot is an immutable Git result, with one parent and no workflow policy.
type Snapshot struct {
	BaseCommit string `json:"baseCommit"`
	Commit     string `json:"commit"`
	Tree       string `json:"tree"`
	Bundle     []byte `json:"bundle"`
	// Message is the one-line subject the snapshot commit must carry, chosen
	// by the coordinator that dispatched the attempt. Empty means the
	// canonical message. It is verified byte for byte, so a commit that says
	// anything else — anything a model might have written — is refused.
	Message string `json:"message,omitempty"`
}

// SnapshotPublisher validates Git objects before an optional atomic ref update.
// An empty expected head requires an absent ref; it never means force overwrite.
type SnapshotPublisher interface {
	VerifySnapshot(context.Context, *api.Connection, Credential, *api.Repository, Snapshot) error
	PublishSnapshot(context.Context, *api.Connection, Credential, *api.Repository, Snapshot, string, string) error
}
