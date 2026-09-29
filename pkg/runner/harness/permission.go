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

package harness

import "context"

// The permission round-trip: how a harness that wants to run something its
// permission mode does not pre-approve ASKS a human, instead of being denied
// because nobody was listening.
//
// It is deliberately a different thing from a Clarification, and the two must
// not be collapsed. A clarification is the model asking a PRODUCT question, and
// what answers it is free text that becomes part of the next turn's
// instructions. A permission request is the harness asking whether ONE named
// tool call, with the input it is holding right now, may proceed — and what
// answers it is a verdict, delivered back into the call that is still waiting.
// A verdict resumes the same turn; an answer starts the next one.
//
// The request carries a tool name and a bounded rendering of that tool's input.
// It carries nothing else on purpose: no credential, no session token, no
// caller identity. A human deciding "may this run `rm -rf /`" needs the call,
// and everything beyond the call is something that should not be travelling to
// wherever the question is rendered.

// MaxPermissionInputBytes bounds the rendering of a tool input that travels to
// a human. Tool inputs routinely carry whole files; an approval card is not
// where a file belongs, and an unbounded field is a way to push arbitrary bytes
// through every layer between here and a portal.
const MaxPermissionInputBytes = 4 << 10

// PermissionRequest is one harness permission prompt waiting on a human.
type PermissionRequest struct {
	// ID is stable for one session and one tool call, and a resume must echo
	// it. It is the same fencing a Clarification has, for the same reason: a
	// verdict that arrives for a request the harness is no longer waiting on
	// must be refused rather than applied to whatever is waiting now.
	ID string `json:"id"`
	// Tool is the harness tool name the call is for, e.g. "Bash" or "WebFetch".
	Tool string `json:"tool"`
	// Input is a bounded JSON rendering of the tool's input, exactly as the
	// harness proposed it. It is what the human is shown and what the verdict
	// authorizes — that call, those arguments.
	Input string `json:"input,omitempty"`
}

// PermissionVerdict is a human's answer to a PermissionRequest.
//
// A denial is a real answer, not a failure: it goes back to the harness, which
// continues the turn and can explain itself or take another route. Message is
// what the harness is told, and it is the only place the human's words reach
// the model.
type PermissionVerdict struct {
	Allow   bool   `json:"allow"`
	Message string `json:"message,omitempty"`
}

// PermissionAsker is how an adapter reaches a human mid-turn. The runner
// implements it; an adapter calls it and BLOCKS, because the tool call it is
// answering is blocked too.
//
// AskPermission returns only when there is a verdict or when ctx is done. A
// ctx-done return must be treated as a denial by the adapter rather than as an
// error that fails the turn: the turn is being cancelled, and a permission call
// left hanging would keep the harness child alive after everything else has
// been torn down.
type PermissionAsker interface {
	AskPermission(ctx context.Context, request PermissionRequest) (PermissionVerdict, error)
}
