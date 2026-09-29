// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package executor describes background agent work — schedule fires, webhook
// events, channel messages — as a serializable Job, and the one door producers
// submit it through.
//
// There is no executor here any more. There used to be: a bounded channel with
// a worker pool, an ErrQueueFull that inbound webhooks turned into 503 +
// Retry-After, and a restart that dropped everything queued. The Run OBJECT is
// the queue now — Submit writes a Pending Run, the Run reconciler claims it, and
// the process that claimed it executes it (see api/background.go) — so the
// channel only ever added a way to refuse or lose work the provider had already
// accepted.
//
// What remains is the vocabulary: the Job, which stays serializable so a durable
// execution engine could carry it unchanged, and Submitter, which is all a
// producer (the Schedule reconciler, an inbound webhook, the Discord gateway)
// ever needed.
package executor

import "context"

// JobKind identifies what submitted the job.
type JobKind string

const (
	KindSchedule JobKind = "schedule"
	KindTrigger  JobKind = "trigger"
	KindChannel  JobKind = "channel"
)

// Job is one unit of background agent work. It carries only serializable data
// (no closures, no clients) so a durable-execution backend can persist and
// replay it.
type Job struct {
	// ID is unique per submission (used for dedup/idempotency by durable backends).
	ID string `json:"id"`
	// Kind is what fired this job.
	Kind JobKind `json:"kind"`
	// ClusterID is the tenant workspace's logical-cluster ID the job acts in.
	ClusterID string `json:"clusterID"`
	// SourceName is the Schedule / Trigger / Connection name that fired.
	SourceName string `json:"sourceName"`
	// ReplyTarget optionally overrides where a channel reply is delivered — used
	// by the Discord gateway bot, where the reply channel is the one the user
	// typed in (not the connection's configured channel). Empty → the
	// connection's default channel/target.
	ReplyTarget string `json:"replyTarget,omitempty"`
	// NotifyChannel is the logical agent-channel role (a Name in the agent's
	// spec.channels) that schedule/trigger output is delivered to. Empty → the
	// agent's primary channel. Resolved to a Connection at delivery time so a
	// re-pointed channel takes effect without re-enqueuing.
	NotifyChannel string `json:"notifyChannel,omitempty"`
	// AgentRef is the Agent to run.
	AgentRef string `json:"agentRef"`
	// Task is the prompt to execute.
	Task string `json:"task"`
	// Trigger is the Run trigger value (schedule|heartbeat|wakeup|event|channel).
	Trigger string `json:"trigger"`
	// SessionID groups the run's transcript.
	SessionID string `json:"sessionID"`
	// RunID is the durable run record this job executes, when the submitter
	// persisted one before enqueueing (see the Submitter contract). Empty → the
	// handler creates the record when it starts.
	RunID string `json:"runID,omitempty"`
}

// Submitter is the one door background work goes through. Producers (the
// Schedule reconciler, inbound webhooks, the Discord gateway) depend on nothing
// more: a submission is a durable write, and it either returned or it did not.
type Submitter interface {
	Submit(ctx context.Context, job Job) error
}
