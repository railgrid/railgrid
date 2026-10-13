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

package sharedstore

import (
	"context"
	"fmt"
	"time"

	"github.com/railgrid/railgrid/pkg/hub/actionproof"
)

const ActionProofUseKind = "railgrid-action-proof-use"

// ActionProofUseStore adapts a Store collection to actionproof.UseStore.
type ActionProofUseStore struct {
	store *Store
}

// NewActionProofUseStore adapts the supplied collection for durable
// cross-replica action-proof use tracking.
func NewActionProofUseStore(store *Store) *ActionProofUseStore {
	return &ActionProofUseStore{store: store}
}

func (s *ActionProofUseStore) Consume(ctx context.Context, nonce, capability, method, path string, expiresAt time.Time) (bool, error) {
	if s == nil || s.store == nil {
		return false, fmt.Errorf("sharedstore: action-proof use store is unavailable")
	}
	if nonce == "" || capability == "" || method == "" || path == "" || expiresAt.IsZero() {
		return false, fmt.Errorf("sharedstore: action-proof use coordinate is incomplete")
	}
	if !s.store.now().Before(expiresAt) {
		return false, nil
	}
	key := actionproof.UseKey(nonce, capability, method, path)
	return s.store.CreateIfAbsent(ctx, key, []byte("used"), expiresAt)
}
