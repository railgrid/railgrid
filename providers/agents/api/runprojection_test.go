// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

var (
	projectedStart  = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	projectedFinish = projectedStart.Add(90 * time.Second)
)

// The Run object carries a run's identity, phase, timings and cost — and
// nothing that belongs in Postgres. This pins both halves of that: what is
// projected, and what deliberately is not.
func TestRunStatusForProjectsTheRunAndNotItsContent(t *testing.T) {
	worked := int64(45_000)
	status := runStatusFor(store.Run{
		ID: "r1", AgentName: "scout", SessionID: "s1", Trigger: agentsv1alpha1.RunTriggerAPI,
		Phase: store.RunPhaseSucceeded, Message: "",
		Input:  "summarise the incident report and post it to #ops",
		Output: "Here is the summary …",
		// Checkpoint is the engine's resume state — opaque, large, and of no
		// use to anyone reading the object.
		Checkpoint:       []byte(`{"messages":[…]}`),
		InputTokens:      1200,
		OutputTokens:     340,
		USDMicros:        12_345,
		StartedAt:        &projectedStart,
		FinishedAt:       &projectedFinish,
		WorkedDurationMS: &worked,
	})

	if status.Phase != agentsv1alpha1.RunPhaseSucceeded {
		t.Errorf("phase = %q, want the store and the object to spell a phase the same way", status.Phase)
	}
	if status.StartedAt == nil || !status.StartedAt.Time.Equal(projectedStart) {
		t.Errorf("startedAt = %v", status.StartedAt)
	}
	if status.FinishedAt == nil || !status.FinishedAt.Time.Equal(projectedFinish) {
		t.Errorf("finishedAt = %v", status.FinishedAt)
	}
	if status.TranscriptRef == nil || status.TranscriptRef.SessionID != "s1" {
		t.Errorf("transcriptRef = %+v, want the session the rows are filed under", status.TranscriptRef)
	}
	if status.Usage == nil {
		t.Fatal("no usage was projected")
	}
	if status.Usage.InputTokens != 1200 || status.Usage.OutputTokens != 340 || status.Usage.USDMicros != 12_345 {
		t.Errorf("usage = %+v", status.Usage)
	}
	if status.Usage.USD != "0.0123" {
		t.Errorf("usd = %q, want the same rendering an Agent's usage carries", status.Usage.USD)
	}
	if status.Usage.DurationMS != 90_000 {
		t.Errorf("durationMS = %d, want the wall clock between the two stamps", status.Usage.DurationMS)
	}
	if status.Usage.WorkedDurationMS == nil || *status.Usage.WorkedDurationMS != worked {
		t.Errorf("workedDurationMS = %v, want the measured %d", status.Usage.WorkedDurationMS, worked)
	}

	// The content stays in Postgres. Encode the status and look for it: a field
	// added later that quietly carried the input or the checkpoint onto the API
	// object would fail here rather than in production.
	encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&status)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range encoded {
		text, ok := value.(string)
		if !ok {
			continue
		}
		for _, leaked := range []string{"summarise the incident", "Here is the summary", "messages"} {
			if contains(text, leaked) {
				t.Errorf("status.%s carries run content (%q); the transcript belongs in the store", key, leaked)
			}
		}
	}
}

// A run with no measured cost projects no usage block at all, rather than a
// block of zeroes. Zero tokens and zero dollars is a claim; absence is not.
func TestRunStatusForOmitsUsageItDoesNotHave(t *testing.T) {
	status := runStatusFor(store.Run{ID: "r1", AgentName: "scout", Phase: store.RunPhasePending})
	if status.Usage != nil {
		t.Errorf("usage = %+v on a run that has not cost anything yet", status.Usage)
	}
	if status.TranscriptRef != nil {
		t.Errorf("transcriptRef = %+v on a run with no session", status.TranscriptRef)
	}
	if status.StartedAt != nil || status.FinishedAt != nil {
		t.Error("a pending run has no timings to project")
	}
}

// Every status update is a watch event, and a long run checkpoints repeatedly.
// An identical projection must be a no-op, or the reconciler and every portal
// subscriber wake for nothing.
func TestEqualStatusSkipsAnIdenticalProjection(t *testing.T) {
	run := store.Run{
		ID: "r1", AgentName: "scout", SessionID: "s1", Phase: store.RunPhaseRunning,
		StartedAt: &projectedStart,
	}
	first, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr(runStatusFor(run)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr(runStatusFor(run)))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStatus(first, second) {
		t.Error("the same run projected twice is not recognised as unchanged")
	}

	// Fields the RECONCILER owns are carried over rather than compared, so the
	// projection never fights it for the deadline or the conditions.
	first["deadlineAt"] = "2026-09-19T12:10:00Z"
	first["owner"] = "replica-1"
	if !equalStatus(first, second) {
		t.Error("the projection treats a reconciler-owned field as a difference")
	}
	if second["deadlineAt"] != "2026-09-19T12:10:00Z" || second["owner"] != "replica-1" {
		t.Errorf("the reconciler's fields were not carried over: %v / %v", second["deadlineAt"], second["owner"])
	}

	// A real transition is a difference.
	run.Phase = store.RunPhaseSucceeded
	changed, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr(runStatusFor(run)))
	if err != nil {
		t.Fatal(err)
	}
	if equalStatus(first, changed) {
		t.Error("a phase change was treated as no change")
	}
}

func ptr[T any](v T) *T { return &v }

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
