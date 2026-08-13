// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
)

func TestParseProviderActionsCanonicalCatalogShape(t *testing.T) {
	parsed, err := ParseProviderActions([]providersv1alpha1.ProviderActionSpec{{
		ID: "query_table/v1",
		BoundResource: providersv1alpha1.ProviderActionBoundResource{
			APIVersion: "databricks.faros.sh/v1alpha1",
			Kind:       "Table",
			Resource:   "tables",
		},
		InputSchema:   &runtime.RawExtension{Raw: []byte(`{"type":"object","additionalProperties":false}`)},
		OutputSchema:  &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
		SchemaDigest:  "sha256:abc",
		ExecutionMode: providersv1alpha1.ProviderActionExecutionSync,
		Idempotency:   providersv1alpha1.ProviderActionIdempotencyKeyed,
		Limits:        providersv1alpha1.ProviderActionLimits{TimeoutSeconds: 45, MaxInputBytes: 8192, MaxOutputBytes: 65536, MaxResultItems: 100},
	}})
	if err != nil {
		t.Fatalf("parse provider actions: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("parsed actions = %#v, want one action", parsed)
	}
	action := parsed[0]
	if action.Name != "query_table" || action.Version != "v1" {
		t.Fatalf("action identity = %#v", action)
	}
	if action.Resource.APIVersion != "databricks.faros.sh/v1alpha1" || action.Resource.Kind != "Table" || action.Resource.Resource != "tables" {
		t.Fatalf("action resource = %#v", action.Resource)
	}
	if string(action.InputSchema) != `{"type":"object","additionalProperties":false}` {
		t.Fatalf("action input schema = %s", action.InputSchema)
	}
	if string(action.OutputSchema) != `{"type":"object"}` || action.SchemaDigest != "sha256:abc" || action.Idempotency != "keyed" || action.Limits.MaxOutputBytes != 65536 {
		t.Fatalf("action policy fields = %#v", action)
	}
	if action.InputValidator == nil || action.OutputValidator == nil {
		t.Fatal("action schemas were not compiled")
	}
}

func TestParseProviderActionsRejectsExternalSchemaReferences(t *testing.T) {
	_, err := ParseProviderActions([]providersv1alpha1.ProviderActionSpec{{
		ID: "query_table/v1",
		BoundResource: providersv1alpha1.ProviderActionBoundResource{
			APIVersion: "databricks.faros.sh/v1alpha1", Kind: "Table", Resource: "tables",
		},
		InputSchema:  &runtime.RawExtension{Raw: json.RawMessage(`{"type":"object","$ref":"https://attacker.invalid/schema"}`)},
		OutputSchema: &runtime.RawExtension{Raw: json.RawMessage(`{"type":"object"}`)},
	}})
	if err == nil || !strings.Contains(err.Error(), "local fragment") {
		t.Fatalf("external schema reference error = %v, want local-fragment rejection", err)
	}
}

// The registry receives liveness from two directions: the beat this replica
// served directly, and CatalogEntry status arriving over the catalog watch.
// Merging them must be monotone, or a status write that predates a local beat
// (or was throttled away entirely) would drag the provider backwards and let
// the sweeper mark a healthy provider stale.
func TestUpsertKeepsNewestHeartbeat(t *testing.T) {
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	reg := NewRegistry()

	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})
	reg.Heartbeat("cost", "v2", base.Add(time.Minute))

	// A status update carrying an older (throttled) timestamp arrives.
	reg.Upsert(Provider{
		Name: "cost", EndpointsValid: true,
		LastHeartbeat: base, HeartbeatRequired: true, ReportedVersion: "v1",
	})

	got, ok := reg.Get("cost")
	if !ok {
		t.Fatal("cost missing from registry")
	}
	if !got.LastHeartbeat.Equal(base.Add(time.Minute)) {
		t.Fatalf("LastHeartbeat = %v, want the newer local beat %v", got.LastHeartbeat, base.Add(time.Minute))
	}
	if got.ReportedVersion != "v2" {
		t.Fatalf("ReportedVersion = %q, want v2", got.ReportedVersion)
	}
}

// The converse: a replica that never served a beat learns liveness purely from
// status, which is the whole point of routing it through the API.
func TestUpsertAdoptsHeartbeatFromStatus(t *testing.T) {
	beat := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})

	reg.Upsert(Provider{
		Name: "cost", EndpointsValid: true,
		LastHeartbeat: beat, HeartbeatRequired: true, ReportedVersion: "v9",
	})

	got, _ := reg.Get("cost")
	if !got.LastHeartbeat.Equal(beat) || got.ReportedVersion != "v9" || !got.HeartbeatRequired {
		t.Fatalf("registry did not adopt status heartbeat: %+v", got)
	}
	if !got.Ready() {
		t.Fatal("provider with a fresh status heartbeat should be Ready")
	}
}

// HeartbeatRequired must never regress: a provider that has beaten once is
// expected to keep beating, whichever replica observed the first one.
func TestUpsertKeepsHeartbeatRequired(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})
	reg.Heartbeat("cost", "", time.Now())

	reg.Upsert(Provider{Name: "cost", EndpointsValid: true})

	if got, _ := reg.Get("cost"); !got.HeartbeatRequired {
		t.Fatal("HeartbeatRequired regressed to false on a spec-only upsert")
	}
}
