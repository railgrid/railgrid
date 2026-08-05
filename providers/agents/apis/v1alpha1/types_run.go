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

package v1alpha1

// Run triggers. Runs are store-native records (transcripts, steps, checkpoints
// live in the provider's Postgres) exposed over /api/runs — deliberately not a
// CRD, so the schema and the execution reality cannot drift.
const (
	RunTriggerChat       = "chat"
	RunTriggerSchedule   = "schedule"
	RunTriggerHeartbeat  = "heartbeat"
	RunTriggerWakeup     = "wakeup"
	RunTriggerEvent      = "event"
	RunTriggerAPI        = "api"
	RunTriggerChannel    = "channel"
	RunTriggerDelegation = "delegation"
	// RunTriggerSpawn marks a scoped worker run started by the "spawn" tool: the
	// same agent, a fresh context, a narrowed toolset, and a parent to report
	// back to. Distinct from delegation, which hands work to a *different*
	// configured agent.
	RunTriggerSpawn = "spawn"
)
