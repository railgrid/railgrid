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

package v1alpha1

// Conditions on the agents provider's tenant-authored resources.
//
// Every kind here carries exactly one condition type, Validated, answering one
// question: is this spec usable as written? It exists because the provider's
// REST CRUD used to be the only way to author these resources, and it rejected
// a bad spec at the door with an HTTP 400. Writers now go through the kube API
// directly (the portal writes CRs with a kube client), where the only
// synchronous gate is the CRD's OpenAPI schema — which cannot see across
// objects (does this agentRef exist? is this connection already another
// agent's channel?) and cannot express the cross-field rules the handlers had.
//
// So the answer moved from the write path to the read path: the reconciler
// says what is wrong, on the object, where the author and the portal can see
// it. Validated=False never blocks anything on its own — the reconcilers keep
// the flat Phase/Message fields exactly as they were — it is a diagnosis.
const (
	// ConditionValidated reports whether the spec is usable. True means the
	// reconciler found nothing wrong. False carries a Reason from the list
	// below and a Message naming the offending field and value.
	ConditionValidated = "Validated"
)

// Reasons for Validated. A reconciler reports the first problem it finds, in
// the order the checks are written, so the message stays about one thing.
const (
	// ReasonValidated accompanies Validated=True.
	ReasonValidated = "Validated"

	// ReasonInvalidSpec is a field that is empty, malformed, or out of range
	// on its own terms — no other object needed to tell.
	ReasonInvalidSpec = "InvalidSpec"

	// ReasonInvalidBudget is a spec.budget the provider cannot enforce: a
	// negative token limit, or a usdLimit that is not a finite number.
	ReasonInvalidBudget = "InvalidBudget"

	// ReasonDuplicateChannel is two entries in spec.channels sharing a name,
	// which makes a schedule's or trigger's channelRef ambiguous.
	ReasonDuplicateChannel = "DuplicateChannel"

	// ReasonChannelConflict is a Connection bound as a channel by more than
	// one Agent. Inbound routing maps a Connection to exactly one agent, so
	// the second binding would silently steal — or lose — the messages.
	ReasonChannelConflict = "ChannelConflict"

	// ReasonUnknownAgentRef is a spec.agentRef naming an Agent that does not
	// exist in this workspace.
	ReasonUnknownAgentRef = "UnknownAgentRef"

	// ReasonUnknownConnectionRef is a connectionRef naming a Connection that
	// does not exist in this workspace.
	ReasonUnknownConnectionRef = "UnknownConnectionRef"

	// ReasonUnknownToolFamily is a families entry that is not one of the
	// built-in tool families; it grants nothing.
	ReasonUnknownToolFamily = "UnknownToolFamily"

	// ReasonMissingCoreFamily is a non-empty families grant that omits "core".
	// The core family carries notify, memory and delegate: a grant without it
	// leaves the agent unable to say anything back.
	ReasonMissingCoreFamily = "MissingCoreFamily"

	// ReasonInvalidSource is a spec.source the provider has no listener for.
	ReasonInvalidSource = "InvalidSource"

	// ReasonSecretMissing is a Connection whose credential Secret does not
	// exist. The Secret is written by whoever creates the Connection, so this
	// is a half-finished create rather than a transient state.
	ReasonSecretMissing = "SecretMissing"

	// ReasonSecretIncomplete is a credential Secret that exists but lacks a
	// key this connection type cannot work without.
	ReasonSecretIncomplete = "SecretIncomplete"

	// ReasonUnsupportedType is a spec.type outside the set the provider knows
	// how to speak.
	ReasonUnsupportedType = "UnsupportedType"
)

// KnownToolFamilies are the grantable built-in tool families, the same set the
// provider's REST/MCP layer validates against (api.knownToolFamilies). "core"
// is one of them rather than implicit: toolset assembly wires the core tools
// only when the resolved grant names it.
var KnownToolFamilies = map[string]bool{
	"core":   true,
	"web":    true,
	"github": true,
	"mcp":    true,
	"edges":  true,
	"files":  true,
	"spawn":  true,
}
