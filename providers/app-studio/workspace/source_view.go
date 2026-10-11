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

package workspace

import "context"

// SourceState is a single durable snapshot of the revision and its provenance.
// Reading these together keeps API responses coherent during concurrent writes.
type SourceState struct {
	SourceRevision        uint64
	LastSourceChange      *SourceChange
	LastPreviewCheckpoint *SourceChange
}

func (s *FileStore) SourceState(ctx context.Context, scope Scope) (SourceState, error) {
	ledger, err := s.ledgerFor(ctx)
	if err != nil {
		return SourceState{}, err
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		return SourceState{}, err
	}
	revision := record.SourceRevision
	if revision == 0 {
		revision = 1
	}
	state := SourceState{SourceRevision: revision}
	if record.LastSourceChange != nil {
		change := *record.LastSourceChange
		state.LastSourceChange = &change
	}
	if record.LastPreviewCheckpoint != nil {
		checkpoint := *record.LastPreviewCheckpoint
		state.LastPreviewCheckpoint = &checkpoint
	}
	return state, nil
}
