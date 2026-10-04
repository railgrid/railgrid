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

package dispatch

import (
	"encoding/json"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner"
)

// Answer is a caller's resolution of a park.
//
// For a QUESTION, Resolution is the words. For a PERMISSION prompt, Allow is
// the verdict and Resolution is the person's own note, which reaches the model
// as the reason — never a stock phrase, because "Approved. Continue." is an
// instruction for a new turn and means nothing to a tool call that is waiting.
type Answer struct {
	// Resolution is the text of the answer, or the note on a verdict.
	Resolution string
	// Allow is the verdict on a permission prompt. Ignored for a question.
	Allow bool
}

// ResumeRequest builds the resume that answers the park the receipt describes.
//
// The receipt is authoritative for what is being answered — it is what the
// runner is waiting on right now — and pos is the fallback for a resume that
// could not re-read it. The two park shapes are mutually exclusive on the wire,
// so exactly one of ClarificationID and PermissionID is ever set: a permission
// prompt is answered with a verdict on a named call, and setting that clears
// any question id so the resume never claims to be both.
//
// The credential and approved input are the caller's: both must be sent again
// on every resume, because a resume may arrive after the runner restarted and
// nothing about the caller's identity survives on the host.
func ResumeRequest(requestID string, receipt runner.Receipt, pos Position, answer Answer, approved json.RawMessage, credential *runner.HarnessCredential) runner.ResumeRequest {
	req := runner.ResumeRequest{
		RequestID:         requestID,
		TaskID:            receipt.TaskID,
		AttemptID:         firstNonEmpty(receipt.AttemptID, pos.AttemptID),
		AttemptEpoch:      maxEpoch(receipt.AttemptEpoch, pos.Epoch),
		SessionID:         firstNonEmpty(receipt.SessionID, pos.SessionID),
		ClarificationID:   clarificationID(receipt, pos),
		Resolution:        answer.Resolution,
		ApprovedInput:     approved,
		HarnessCredential: credential,
	}
	if req.TaskID == "" {
		req.TaskID = pos.TaskID
	}
	if id := permissionID(receipt, pos); id != "" {
		req.ClarificationID = ""
		req.PermissionID = id
		req.PermissionDecision = runner.PermissionDeny
		if answer.Allow {
			req.PermissionDecision = runner.PermissionAllow
		}
		req.Resolution = strings.TrimSpace(answer.Resolution)
	}
	return req
}

func clarificationID(receipt runner.Receipt, pos Position) string {
	if receipt.Clarification != nil && receipt.Clarification.ID != "" {
		return receipt.Clarification.ID
	}
	return pos.ClarificationID
}

func permissionID(receipt runner.Receipt, pos Position) string {
	if receipt.Permission != nil && receipt.Permission.ID != "" {
		return receipt.Permission.ID
	}
	return pos.PermissionID
}
