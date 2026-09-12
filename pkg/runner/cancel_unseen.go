/*
Copyright 2026 The Faros Authors.

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

package runner

import "maps"

// cancelUnseenLocked records a cancellation without inventing an execution or
// worktree. Start's immutable attempt binding rejects every delayed start for
// this attempt ID. The caller holds r.mu across persistence and publication.
func (r *Runner) cancelUnseenLocked(request CancelRequest, opKey, fingerprint string) (Receipt, error) {
	if err := r.checkEpochLocked(request.TaskID, request.AttemptID, request.AttemptEpoch); err != nil {
		return Receipt{}, err
	}
	now := eventNow()
	receipt := Receipt{
		ProtocolVersion: ProtocolVersion,
		TaskID:          request.TaskID,
		AttemptID:       request.AttemptID,
		AttemptEpoch:    request.AttemptEpoch,
		Phase:           PhaseCancelled,
		UpdatedAt:       now,
		Cursor:          1,
		Blocker:         "cancelled while absent from local journal; delayed start fenced",
	}
	// Do not expose a terminal receipt in memory until it is durable. In
	// particular a failed write followed by Inspect or Cancel replay must not
	// make the coordinator believe cancellation succeeded.
	next := r.state
	next.Attempts = maps.Clone(r.state.Attempts)
	next.Operations = maps.Clone(r.state.Operations)
	next.Events = maps.Clone(r.state.Events)
	next.Attempts[request.AttemptID] = &attemptRecord{Receipt: receipt, Fingerprint: fingerprint, CancelPending: true}
	next.Operations[opKey] = operationRecord{Fingerprint: fingerprint, AttemptID: request.AttemptID}
	next.Events[request.AttemptID] = []Event{{Cursor: 1, Timestamp: now, AttemptEpoch: request.AttemptEpoch, Type: EventCancelled, Message: receipt.Blocker}}
	if err := r.store.save(next); err != nil {
		return Receipt{}, protocolError(ErrorUnavailable, true, "persist cancellation fence: "+err.Error(), nil)
	}
	r.state = next
	return receiptForResponse(receipt), nil
}
