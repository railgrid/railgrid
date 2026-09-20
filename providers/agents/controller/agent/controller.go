// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package agent reconciles Agent CRs. Two jobs, both of them "say out loud
// what is true of this agent":
//
//   - Phase. An Agent has no host-side resource to manage; the reconciler
//     exists so that every Agent reads as Ready. The create handler stamps the
//     phase, but agents created before that existed (or whose stamp failed)
//     stayed at status {} forever, which reads as "not ready" to anyone
//     polling. Suspended (or any other non-empty phase) is left alone.
//
//   - The Validated condition. The provider's REST create/update handlers used
//     to be the only door onto an Agent, and they rejected a bad spec with a
//     400 before it was ever stored (api/agents.go: agentFromCreateRequest,
//     normalizeAgentBudget, normalizeChannels, validateChannelUniqueness,
//     normalizeFamilies). The portal now writes Agent CRs with a kube client
//     and never reaches those handlers, so the checks that need more than the
//     CRD schema — a usdLimit that parses, a channel list with no duplicates,
//     a Connection no other agent has already claimed, a families grant that
//     actually grants something — run here instead and land on the object as
//     a condition. The MCP tools still go through api/, which still rejects
//     the same things at the door; this is the same verdict reached one step
//     later for everyone else.
//
// Nothing here rewrites spec. The old handlers normalized as they validated
// (adding "core" to a families grant, forcing exactly one primary channel);
// a reconciler that did the same would be editing the user's intent behind
// their back and fighting whatever wrote it. It reports instead.
//
// A third job, when the provider gives it somewhere to write to: purging the
// agent's rows from the provider store on delete, and revoking the hub-minted
// identity it ran unattended work with. The CR is the agent's
// configuration, but its transcripts, runs, memories and usage live in
// Postgres, and only the deleted DELETE /api/agents/{name} handler ever
// removed them. A kube-client delete leaves them behind — invisible, billable,
// and readable again the moment an agent of the same name is recreated. A
// finalizer closes that. It is added only when PurgeData is wired, so the
// dev/in-memory path never grows a finalizer it cannot honour, and it releases
// after purgeGiveUp however the purge goes: a stuck delete is worse than an
// orphaned row.
package agent

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// DataFinalizer guards an Agent until its rows in the provider store have
// been purged. Namespaced to this provider so nothing else claims it.
const DataFinalizer = "agents.railgrid.ai/purge-store-data"

const (
	// purgeRetryInterval spaces retries of a failed purge. A delete already
	// looks done to the user by then, so this is about finishing quietly
	// rather than promptly.
	purgeRetryInterval = 30 * time.Second
	// purgeGiveUp bounds how long a delete may be held open for the purge. A
	// store that is down or a row set that will not delete must not leave the
	// user with an Agent they cannot get rid of; past this the finalizer is
	// released and the orphaned rows are logged for an operator to collect.
	purgeGiveUp = 10 * time.Minute
)

// Reconciler stamps phase Ready on agents that have no phase, reports spec
// validity as the Validated condition, and purges an agent's store data when
// it is deleted.
type Reconciler struct {
	Manager mcmanager.Manager
	// PurgeData removes an agent's rows (transcripts, runs, memories, usage)
	// from the provider store. This is the teardown the deleted
	// DELETE /api/agents/{name} handler performed inline; nothing else does
	// it. clusterID is the tenant logical cluster, which the provider maps to
	// the store's org/workspace scope on its side — the reconciler has no
	// tenant identity of its own and must not invent one.
	//
	// nil (no store configured, or the dev in-memory path) disables the
	// finalizer entirely rather than adding one nothing would ever clear.
	PurgeData func(ctx context.Context, clusterID, agentName string) error

	// ReleaseIdentity revokes the agent's hub-minted identity. It runs in the
	// same finalizer as PurgeData because both answer "this agent is gone":
	// leaving the identity for the hub's own sweep keeps a usable, scoped token
	// alive for up to a full TTL after the agent stopped existing, and an agent
	// that no longer exists should not be able to reach anything for a minute
	// longer than it has to.
	//
	// nil skips the release, which is right when no identity service is
	// configured — there is nothing to revoke.
	ReleaseIdentity func(ctx context.Context, clusterID, agentName string) error
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// SetupWithManager wires the reconciler into the multicluster manager.
//
// Connections and ModelCredentials are watched as well as Agents: an agent's
// channel bindings are only valid while the Connections they name exist, and
// its ModelCredentialsReady condition tracks credentials whose readiness
// changes with no agent edit at all (a rotated key, an endpoint that started
// refusing). Creating the missing object, or a credential going Ready, must
// update the agent without it being touched. The mapping is deliberately
// coarse (every Agent in the cluster) — those events are rare and an Agent
// list is small, and the alternative is an index over refs for no gain.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-agent").
		For(&agentsv1alpha1.Agent{}).
		Watches(&agentsv1alpha1.Connection{}, allAgents).
		Watches(&agentsv1alpha1.ModelCredential{}, allAgents).
		Complete(r)
}

// allAgents enqueues every Agent in the cluster the event came from.
func allAgents(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list agentsv1alpha1.AgentList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			klog.FromContext(ctx).Error(err, "listing agents to re-validate")
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
		return reqs
	}))(clusterName, cl)
}

// Reconcile handles one Agent: purge-and-release on the way out, otherwise
// phase and verdict.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var agent agentsv1alpha1.Agent
	if err := c.Get(ctx, req.NamespacedName, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !agent.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &agent, req.ClusterName.String())
	}
	if (r.PurgeData != nil || r.ReleaseIdentity != nil) && !controllerutil.ContainsFinalizer(&agent, DataFinalizer) {
		controllerutil.AddFinalizer(&agent, DataFinalizer)
		if err := c.Update(ctx, &agent); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		// The metadata write re-delivers the object; status is written from
		// the fresh copy rather than the stale one in hand.
		return ctrl.Result{}, nil
	}

	changed := false
	// Any non-empty phase is somebody else's decision (a budget suspension, a
	// manual pause) and is left alone.
	if agent.Status.Phase == "" {
		agent.Status.Phase = agentsv1alpha1.AgentPhaseReady
		changed = true
	}

	reason, message, err := r.validate(ctx, c, &agent)
	if err != nil {
		// A read we could not complete says nothing about the spec. Leave the
		// condition as it stands and let the retry settle it, rather than
		// flagging a sound agent because the apiserver blinked.
		return ctrl.Result{}, err
	}
	if setValidated(&agent, reason, message) {
		changed = true
	}

	// ModelCredentialsReady is its own condition rather than another Validated
	// reason: an agent whose credential stopped answering has a correct spec
	// and an unreachable model, and collapsing the two would report a rotated
	// key as a malformed agent.
	credReason, credMessage, err := r.validateModelCredentials(ctx, c, &agent)
	if err != nil {
		return ctrl.Result{}, err
	}
	if setModelCredentialsReady(&agent, credReason, credMessage) {
		changed = true
	}
	if !changed {
		return ctrl.Result{}, nil
	}
	if err := c.Status().Update(ctx, &agent); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	klog.FromContext(ctx).V(2).Info("agent status written", "agent", req.Name, "cluster", req.ClusterName, "validated", reason == "")
	return ctrl.Result{}, nil
}

// setValidated records the verdict. reason=="" is valid; anything else is a
// Validated=False with that reason and message. Reports whether the stored
// conditions changed, so a reconcile that found nothing new writes nothing.
func setValidated(agent *agentsv1alpha1.Agent, reason, message string) bool {
	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionValidated,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonValidated,
		Message:            "the agent spec is usable",
		ObservedGeneration: agent.Generation,
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	return meta.SetStatusCondition(&agent.Status.Conditions, cond)
}

// setModelCredentialsReady records the credential verdict. reason=="" is
// ready; anything else is False with that reason and message.
func setModelCredentialsReady(agent *agentsv1alpha1.Agent, reason, message string) bool {
	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionModelCredentialsReady,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonModelCredentialsReady,
		Message:            "every model credential this agent references is ready",
		ObservedGeneration: agent.Generation,
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	return meta.SetStatusCondition(&agent.Status.Conditions, cond)
}

// validateModelCredentials checks that every ModelCredential the agent names —
// in spec.models and spec.modelFallbacks — exists and is Ready.
//
// It reports the offending NAMES, because that is what the person edits. An
// agent naming nothing at all is reported too: it is a configuration gap that
// reads as a run failing at its first turn otherwise.
func (r *Reconciler) validateModelCredentials(ctx context.Context, c client.Client, agent *agentsv1alpha1.Agent) (reason, message string, err error) {
	names := referencedCredentials(agent)
	if len(names) == 0 {
		return agentsv1alpha1.ReasonNoModelCredential,
			"spec.models names no model credential, so this agent cannot run; point spec.models.chat at a ModelCredential", nil
	}
	var missing, notReady []string
	for _, name := range names {
		var cred agentsv1alpha1.ModelCredential
		switch err := c.Get(ctx, types.NamespacedName{Name: name}, &cred); {
		case apierrors.IsNotFound(err):
			missing = append(missing, name)
			continue
		case err != nil:
			return "", "", err
		}
		if !meta.IsStatusConditionTrue(cred.Status.Conditions, agentsv1alpha1.ConditionReady) {
			notReady = append(notReady, name)
		}
	}
	if len(missing) > 0 {
		return agentsv1alpha1.ReasonUnknownModelCredential,
			fmt.Sprintf("model credential(s) %s do not exist in this workspace", strings.Join(missing, ", ")), nil
	}
	if len(notReady) > 0 {
		return agentsv1alpha1.ReasonModelCredentialNotReady,
			fmt.Sprintf("model credential(s) %s are not Ready; check their status for what to fix", strings.Join(notReady, ", ")), nil
	}
	return "", "", nil
}

// referencedCredentials is every credential name the agent runs on, in a
// stable order and without duplicates: the per-purpose map first (purposes
// sorted so the message never reorders itself between reconciles), then the
// fallback list in the order it is written.
func referencedCredentials(agent *agentsv1alpha1.Agent) []string {
	purposes := make([]string, 0, len(agent.Spec.Models))
	for purpose := range agent.Spec.Models {
		purposes = append(purposes, purpose)
	}
	sort.Strings(purposes)

	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, purpose := range purposes {
		add(agent.Spec.Models[purpose])
	}
	for _, name := range agent.Spec.ModelFallbacks {
		add(name)
	}
	return out
}

// budgetDecimalPattern mirrors api.budgetDecimalPattern: what the REST/MCP
// boundary accepts as spec.budget.usdLimit.
var budgetDecimalPattern = regexp.MustCompile(`^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$`)

// validate returns the first problem found, as (reason, message). An error is
// a failed read, not a verdict.
//
// The order is the order a reader would want to be told: what is wrong with
// this object on its own terms first, then what is wrong about it in the
// company of the other objects in the workspace.
func (r *Reconciler) validate(ctx context.Context, c client.Client, agent *agentsv1alpha1.Agent) (reason, message string, err error) {
	if strings.TrimSpace(agent.Spec.DisplayName) == "" {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.displayName is required", nil
	}
	if reason, message := validateBudget(agent.Spec.Budget); reason != "" {
		return reason, message, nil
	}
	if agent.Spec.Limits.MaxToolTurns < 0 {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.limits.maxToolTurns must be zero or greater", nil
	}
	if agent.Spec.Limits.TimeoutSeconds < 0 {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.limits.timeoutSeconds must be zero or greater", nil
	}
	for _, d := range agent.Spec.Delegates {
		if strings.TrimSpace(d) == agent.Name {
			return agentsv1alpha1.ReasonInvalidSpec, "spec.delegates lists the agent itself; an agent cannot delegate to itself", nil
		}
	}
	for _, class := range []struct {
		field string
		grant agentsv1alpha1.ToolGrant
	}{
		{"spec.tools.interactive", agent.Spec.Tools.Interactive},
		{"spec.tools.background", agent.Spec.Tools.Background},
	} {
		if reason, message := validateFamilies(class.field, class.grant.Families); reason != "" {
			return reason, message, nil
		}
	}
	if reason, message := validateChannelShape(agent.Spec.Channels); reason != "" {
		return reason, message, nil
	}
	return r.validateChannelRefs(ctx, c, agent)
}

// validateBudget reproduces api.normalizeAgentBudget's checks. The CRD bounds
// tokenLimit, but usdLimit is a free-form decimal string (it is a money
// amount, not a float on the wire) and nothing but this has ever parsed it —
// an unparseable one silently disables the cost cap at run time.
func validateBudget(budget *agentsv1alpha1.AgentBudget) (reason, message string) {
	if budget == nil {
		return "", ""
	}
	if budget.TokenLimit < 0 {
		return agentsv1alpha1.ReasonInvalidBudget, "spec.budget.tokenLimit must be zero or greater"
	}
	usd := strings.TrimSpace(budget.USDLimit)
	if usd == "" {
		return "", ""
	}
	limit, err := strconv.ParseFloat(usd, 64)
	if !budgetDecimalPattern.MatchString(usd) || err != nil || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return agentsv1alpha1.ReasonInvalidBudget, fmt.Sprintf("spec.budget.usdLimit %q must be a finite number zero or greater", budget.USDLimit)
	}
	if limit < 0 {
		return agentsv1alpha1.ReasonInvalidBudget, fmt.Sprintf("spec.budget.usdLimit %q must be zero or greater", budget.USDLimit)
	}
	return "", ""
}

// validateFamilies reproduces what api.normalizeFamilies used to guarantee
// about a stored grant, as a verdict rather than a rewrite: every entry names
// a real family, and a grant that names any family names "core".
//
// The second half matters more than it looks. Toolset assembly wires the core
// tools only when the resolved grant contains "core" (api/toolset.go), and an
// empty grant falls back to the defaults — so ["web"] is the one shape that
// silently costs an agent notify, memory and delegate. The old handler made
// that shape unreachable by always prepending "core"; a direct writer can
// reach it, so it is called out.
func validateFamilies(field string, families []string) (reason, message string) {
	if len(families) == 0 {
		return "", "" // the run-time default grant applies
	}
	core := false
	for _, f := range families {
		f = strings.TrimSpace(f)
		if f == "core" {
			core = true
		}
		if !agentsv1alpha1.KnownToolFamilies[f] {
			return agentsv1alpha1.ReasonUnknownToolFamily, fmt.Sprintf("%s.families contains unknown family %q", field, f)
		}
	}
	if !core {
		return agentsv1alpha1.ReasonMissingCoreFamily,
			field + `.families omits "core", so the agent has no notify, memory or delegate tools; add "core" to the list`
	}
	return "", ""
}

// validateChannelShape reproduces api.normalizeChannels: every row is whole,
// names are unique, and at most one row is primary.
//
// A half-filled row was an error there rather than a silent drop, because
// dropping it made a save look successful while binding nothing — the agent
// appeared configured and no message ever reached it. Same reasoning here.
func validateChannelShape(channels []agentsv1alpha1.AgentChannel) (reason, message string) {
	seen := map[string]bool{}
	primaries := 0
	for i, ch := range channels {
		name := strings.TrimSpace(ch.Name)
		conn := strings.TrimSpace(ch.ConnectionRef)
		switch {
		case name == "" && conn == "":
			return agentsv1alpha1.ReasonInvalidSpec, fmt.Sprintf("spec.channels[%d] is empty", i)
		case conn == "":
			return agentsv1alpha1.ReasonInvalidSpec, fmt.Sprintf("spec.channels[%d] (%q) has no connectionRef", i, name)
		case name == "":
			return agentsv1alpha1.ReasonInvalidSpec, fmt.Sprintf("spec.channels[%d], bound to connection %q, has no name", i, conn)
		}
		if seen[name] {
			return agentsv1alpha1.ReasonDuplicateChannel, fmt.Sprintf("spec.channels has duplicate channel name %q", name)
		}
		seen[name] = true
		if ch.Primary {
			primaries++
		}
	}
	if primaries > 1 {
		return agentsv1alpha1.ReasonInvalidSpec, "spec.channels marks more than one channel primary; exactly one channel is the agent's default notify target"
	}
	return "", ""
}

// validateChannelRefs checks the channel bindings against the rest of the
// workspace: the Connections they name must exist, and no Connection may back
// two agents.
//
// The uniqueness rule is not cosmetic. Inbound routing maps a Connection to
// exactly one agent, so a second agent binding the same Connection does not
// share it — one of them stops receiving. api.validateChannelUniqueness
// refused the write; here the newcomer is the one flagged, which is the same
// outcome read a moment later.
func (r *Reconciler) validateChannelRefs(ctx context.Context, c client.Client, agent *agentsv1alpha1.Agent) (reason, message string, err error) {
	if len(agent.Spec.Channels) == 0 {
		return "", "", nil
	}
	mine := map[string]bool{}
	for _, ch := range agent.Spec.Channels {
		conn := strings.TrimSpace(ch.ConnectionRef)
		var got agentsv1alpha1.Connection
		switch err := c.Get(ctx, types.NamespacedName{Name: conn}, &got); {
		case apierrors.IsNotFound(err):
			return agentsv1alpha1.ReasonUnknownConnectionRef,
				fmt.Sprintf("spec.channels[%q].connectionRef names connection %q, which does not exist", ch.Name, conn), nil
		case err != nil:
			return "", "", err
		}
		mine[conn] = true
	}

	var agents agentsv1alpha1.AgentList
	if err := c.List(ctx, &agents); err != nil {
		return "", "", err
	}
	for i := range agents.Items {
		other := &agents.Items[i]
		if other.Name == agent.Name || !other.DeletionTimestamp.IsZero() {
			continue
		}
		// Only agents older than this one are contenders, so the pair does not
		// flag each other: the agent that bound the connection first keeps it,
		// exactly as the REST handler let the first create through and refused
		// the second. Same-second creations tie-break by name, so the verdict
		// is the same on every replica and every reconcile.
		if !olderThan(other, agent) {
			continue
		}
		for _, och := range other.Spec.Channels {
			if mine[strings.TrimSpace(och.ConnectionRef)] {
				return agentsv1alpha1.ReasonChannelConflict,
					fmt.Sprintf("connection %q is already a channel of agent %q; a connection routes inbound messages to one agent only", och.ConnectionRef, other.Name), nil
			}
		}
	}
	return "", "", nil
}

// olderThan orders two agents deterministically: by creation time, then by
// name for the same timestamp (kube timestamps have second granularity, so
// two agents created together is ordinary rather than exotic).
func olderThan(a, b *agentsv1alpha1.Agent) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// ---- deletion: purge the agent's store data ---------------------------------

// finalize purges the agent's rows from the provider store and releases the
// finalizer.
//
// The release is unconditional past purgeGiveUp. Holding a deletion open on a
// failing purge trades a recoverable problem (rows an operator can delete) for
// an unrecoverable one (an object the user cannot remove and cannot explain),
// so the deadline is measured from the deletion timestamp and needs no state
// of its own: every replica and every retry reads the same clock against the
// same object.
func (r *Reconciler) finalize(ctx context.Context, c client.Client, agent *agentsv1alpha1.Agent, clusterID string) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("agent", agent.Name, "cluster", clusterID)
	if !controllerutil.ContainsFinalizer(agent, DataFinalizer) {
		return ctrl.Result{}, nil
	}
	// Revocation first, and independently of the purge: it is the half with a
	// security consequence, and it must not be held hostage by a store that is
	// down. A failure here is retried on the same schedule as the purge.
	if r.ReleaseIdentity != nil {
		if err := r.ReleaseIdentity(ctx, clusterID, agent.Name); err != nil {
			if r.now().Sub(agent.DeletionTimestamp.Time) < purgeGiveUp {
				logger.Error(err, "revoking the agent's identity; will retry", "retryIn", purgeRetryInterval)
				return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil
			}
			logger.Error(err, "giving up on revoking the agent's identity; it will lapse when its token expires",
				"deadline", purgeGiveUp)
		}
	}
	if r.PurgeData != nil {
		if err := r.PurgeData(ctx, clusterID, agent.Name); err != nil {
			if r.now().Sub(agent.DeletionTimestamp.Time) < purgeGiveUp {
				logger.Error(err, "purging the agent's store data; will retry", "retryIn", purgeRetryInterval)
				return ctrl.Result{RequeueAfter: purgeRetryInterval}, nil
			}
			// Past the deadline the rows stay and the object goes. Logged at
			// error level because somebody has to know they are there.
			logger.Error(err, "giving up on purging the agent's store data; releasing the finalizer and leaving the rows behind",
				"deadline", purgeGiveUp)
		}
	}
	controllerutil.RemoveFinalizer(agent, DataFinalizer)
	if err := c.Update(ctx, agent); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return ctrl.Result{}, err
	}
	logger.V(2).Info("agent store data purged and finalizer released")
	return ctrl.Result{}, nil
}
