// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/railgrid/provider-agents/store"
)

func TestTypedResultValidationReplayAndRecovery(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemoryStore()
	d := Deps{Store: s, Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "coordinator"}, RunID: "run"}
	contract := ResultContract{Name: "factory_report_decision", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["state"],"properties":{"state":{"enum":["blocked","complete"]}}}`)}
	raw, _ := json.Marshal(contract)
	input := ResultContractPrefix + string(raw) + "\nTask"
	tool := Result(d, input)[0]
	for _, bad := range []string{`{"state":"blocked",""}`, `{"state":"unknown"}`, `{"state":"complete","surprise":true}`} {
		if _, err := tool.Exec(ctx, bad); err == nil {
			t.Fatalf("invalid decision accepted: %s", bad)
		}
	}
	if _, found, _ := s.GetRunArtifact(ctx, d.Scope, d.RunID, StructuredResultArtifact); found {
		t.Fatal("invalid result persisted")
	}
	if _, err := tool.Exec(ctx, `{"state":"blocked"}`); err != nil {
		t.Fatal(err)
	}
	// A recreated tool after recovery sees the same durable result.
	recovered := Result(d, input)[0]
	if _, err := recovered.Exec(ctx, `{ "state" : "blocked" }`); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Exec(ctx, `{"state":"complete"}`); err == nil {
		t.Fatal("accepted a conflicting terminal decision")
	}
	got, found, err := s.GetRunArtifact(ctx, d.Scope, d.RunID, StructuredResultArtifact)
	if err != nil || !found || string(got.Data) != `{"state":"blocked"}` {
		t.Fatalf("lost recorded decision: %+v %v", got, err)
	}
	if Result(d, "Task body\nprose\n"+input) != nil {
		t.Fatal("embedded task text created a result tool")
	}
}

func TestTypedResultConditionalEvidenceCanBeCorrectedBeforeRecording(t *testing.T) {
	d := Deps{Store: store.NewMemoryStore(), Scope: store.Scope{OrgUUID: "org", WorkspaceUUID: "ws", AgentName: "worker"}, RunID: "run"}
	contract := ResultContract{Name: "report_result", Schema: json.RawMessage(`{"type":"object","required":["state"],"properties":{"state":{"enum":["ready","blocked"]},"evidence":{"type":"string","minLength":1}},"if":{"properties":{"state":{"const":"ready"}}},"then":{"required":["evidence"]}}`)}
	raw, _ := json.Marshal(contract)
	tool := Result(d, ResultContractPrefix+string(raw)+"\nTask")[0]
	if _, err := tool.Exec(t.Context(), `{"state":"ready"}`); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if _, err := tool.Exec(t.Context(), `{"state":"ready","evidence":"review-run"}`); err != nil {
		t.Fatal(err)
	}
}
