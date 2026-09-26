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

// Conditions on ModelCredential. This kind is the exception to the one-
// condition rule above, because a credential is a pair — an object and a
// Secret — reached over the network, and "is it usable?" has two independent
// answers a reader has to be able to tell apart: the Secret is not there yet
// (fix it in this workspace) versus the endpoint refused the key (fix it at
// the provider). Ready is the conjunction, and it is what an Agent's
// ModelCredentialsReady reads.
const (
	// ConditionSecretResolved reports whether spec.secretRef names a Secret
	// that exists, carries spec.secretKey, and is labelled
	// railgrid.ai/owner: agents (without which the provider's label-scoped
	// claim hides it from every unattended run).
	ConditionSecretResolved = "SecretResolved"

	// ConditionReachable reports whether GET {spec.baseURL}/models answered
	// with the resolved key.
	ConditionReachable = "Reachable"

	// ConditionReady is True when both SecretResolved and Reachable are.
	ConditionReady = "Ready"

	// ConditionModelCredentialsReady is on an AGENT: every ModelCredential it
	// references in spec.models and spec.modelFallbacks exists and is Ready.
	// It is separate from Validated because a credential going unready is not
	// a defect in the agent's spec — the agent is correct and the model is
	// unreachable, and conflating the two would make a rotated key read as a
	// malformed agent.
	ConditionModelCredentialsReady = "ModelCredentialsReady"
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

	// ReasonRunDeadlineExceeded is a Run that used more than its agent's
	// spec.limits.timeoutSeconds. It is on the Run rather than a phase of its
	// own because the phase is the executor's to write — the reconciler saw
	// the deadline pass, the executor is what stops the work.
	ReasonRunDeadlineExceeded = "DeadlineExceeded"

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

// Reasons for the ModelCredential and Agent conditions above.
const (
	// ReasonSecretResolved accompanies SecretResolved=True.
	ReasonSecretResolved = "SecretResolved"

	// ReasonSecretUnreadable is a Secret the provider could not read at all —
	// most often a Secret that exists but does not carry the owner label, so
	// the label-scoped claim answers the read with a 404.
	ReasonSecretUnreadable = "SecretUnreadable"

	// ReasonOwnerLabelMissing is a Secret the provider CAN read (the caller's
	// own view) but which is not labelled railgrid.ai/owner: agents, so no
	// unattended run will ever see it.
	ReasonOwnerLabelMissing = "OwnerLabelMissing"

	// ReasonReachable accompanies Reachable=True.
	ReasonReachable = "Reachable"

	// ReasonProbeFailed is a GET {baseURL}/models that did not answer 2xx.
	ReasonProbeFailed = "ProbeFailed"

	// ReasonSecretUnresolved accompanies Reachable/Ready=False when the probe
	// could not even be attempted because the Secret is not resolved.
	ReasonSecretUnresolved = "SecretUnresolved"

	// ReasonReady accompanies Ready=True.
	ReasonReady = "Ready"

	// ReasonNotReady accompanies Ready=False.
	ReasonNotReady = "NotReady"

	// ReasonModelCredentialsReady accompanies an Agent's
	// ModelCredentialsReady=True.
	ReasonModelCredentialsReady = "ModelCredentialsReady"

	// ReasonUnknownModelCredential is a spec.models / spec.modelFallbacks
	// entry naming a ModelCredential that does not exist in this workspace.
	ReasonUnknownModelCredential = "UnknownModelCredential"

	// ReasonModelCredentialNotReady is a referenced ModelCredential that
	// exists but whose Ready condition is not True.
	ReasonModelCredentialNotReady = "ModelCredentialNotReady"

	// ReasonNoModelCredential is an Agent that names no model credential at
	// all, so it cannot run.
	ReasonNoModelCredential = "NoModelCredential"
)

// KnownToolFamilies are the grantable built-in tool families, the same set the
// provider's REST/MCP layer validates against (api.knownToolFamilies). "core"
// is one of them rather than implicit: toolset assembly wires the core tools
// only when the resolved grant names it.
var KnownToolFamilies = map[string]bool{
	"core":          true,
	"web":           true,
	"github":        true,
	"mcp":           true,
	"edges":         true,
	"files":         true,
	"spawn":         true,
	"visualization": true,
}
