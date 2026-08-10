// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantContentPartsCanonicalizeAndRemapResourceIndexes(t *testing.T) {
	resources := []projectAssistantContextResourceInput{
		{Provider: "zeta", ResourceRef: aiv1alpha1.ProjectProviderResourceReference{Name: "orders", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables"}},
		{Provider: "alpha", ResourceRef: aiv1alpha1.ProjectProviderResourceReference{Name: "customers", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables"}},
	}
	selected := []string{"team:review"}
	parts := []projectAssistantContentPart{
		projectAssistantContentPartText("inspect "),
		projectAssistantContentPartSkill("team:review"),
		projectAssistantContentPartText(" and "),
		projectAssistantContentPartResource(0),
		projectAssistantContentPartText(" then "),
		projectAssistantContentPartResource(1),
	}
	canonical, canonicalResources, derived, err := normalizeProjectAssistantContentParts(parts, selected, resources)
	if err != nil {
		t.Fatalf("normalize content parts: %v", err)
	}
	if len(canonical) != len(parts) || len(canonicalResources) != 2 {
		t.Fatalf("canonical parts/resources = %d/%d, want %d/2", len(canonical), len(canonicalResources), len(parts))
	}
	if canonicalResources[0].Provider != "alpha" || canonical[3].ResourceIndex != 1 || canonical[5].ResourceIndex != 0 {
		t.Fatalf("canonical resource order/index remap = %#v / %#v", canonicalResources, canonical)
	}
	want := "inspect [@skill:team:review] and [@resource:zeta/apps.example/v1/Table/tables/orders] then [@resource:alpha/apps.example/v1/Table/tables/customers]"
	if derived != want {
		t.Fatalf("derived content = %q, want %q", derived, want)
	}
	raw, err := json.Marshal(canonical)
	if err != nil || !strings.Contains(string(raw), `"resourceIndex":1`) || !strings.Contains(string(raw), `"resourceIndex":0`) {
		t.Fatalf("canonical JSON = %s, err=%v", raw, err)
	}
}

func TestProjectAssistantContentPartsRejectInvalidAndIncompleteSelections(t *testing.T) {
	resource := projectAssistantContextResourceInput{Provider: "demo", ResourceRef: aiv1alpha1.ProjectProviderResourceReference{Name: "orders", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables"}}
	tests := []struct {
		name  string
		parts []projectAssistantContentPart
		want  string
	}{
		{name: "unknown type", parts: []projectAssistantContentPart{{Type: "image"}}, want: "unknown content part type"},
		{name: "unselected skill", parts: []projectAssistantContentPart{projectAssistantContentPartSkill("team:other")}, want: "unselected assistant skill"},
		{name: "missing resource", parts: []projectAssistantContentPart{projectAssistantContentPartSkill("team:review")}, want: "represent selected context resource"},
		{name: "bad resource index", parts: []projectAssistantContentPart{projectAssistantContentPartResource(2)}, want: "out of range"},
		{name: "mixed fields", parts: []projectAssistantContentPart{{Type: "text", Text: "x", SkillID: "team:review"}}, want: "cannot include"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := normalizeProjectAssistantContentParts(test.parts, []string{"team:review"}, []projectAssistantContextResourceInput{resource})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
	if _, _, _, err := normalizeProjectAssistantContentParts([]projectAssistantContentPart{{Type: "text", Text: string([]byte{0xff})}}, nil, nil); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	parts := make([]projectAssistantContentPart, projectAssistantMaxContentParts+1)
	for index := range parts {
		parts[index] = projectAssistantContentPartSkill("team:review")
	}
	if _, _, _, err := normalizeProjectAssistantContentParts(parts, []string{"team:review"}, nil); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("part bound error = %v", err)
	}
}

func TestProjectAssistantContentPartsMergeEmptyAndStrictDecode(t *testing.T) {
	parts := []projectAssistantContentPart{
		{Type: "text", Text: "a"},
		{Type: "text", Text: ""},
		{Type: "text", Text: "b"},
	}
	canonical, _, derived, err := normalizeProjectAssistantContentParts(parts, nil, nil)
	if err != nil {
		t.Fatalf("normalize merged text: %v", err)
	}
	if len(canonical) != 1 || canonical[0].Text != "ab" || derived != "ab" {
		t.Fatalf("merged canonical = %#v, derived=%q", canonical, derived)
	}
	var decoded projectAssistantContentPart
	if err := json.Unmarshal([]byte(`{"type":"resource","resourceIndex":0}`), &decoded); err != nil || !decoded.resourceIndexSet {
		t.Fatalf("decode resource index zero = %#v, err=%v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`{"type":"text","text":"x","skillID":"team:review"}`), &decoded); err != nil {
		t.Fatalf("strict union decode should defer mixed-field validation: %v", err)
	}
	if _, _, _, err := normalizeProjectAssistantContentParts([]projectAssistantContentPart{decoded}, nil, nil); err == nil {
		t.Fatal("mixed content part fields were accepted")
	}
	if err := json.Unmarshal([]byte(`{"type":"text","unknown":true}`), &decoded); err == nil {
		t.Fatal("unknown content part field was accepted")
	}
}

func TestProjectAssistantContentPartsDigestAuditAndThreadProjection(t *testing.T) {
	parts := []projectAssistantContentPart{projectAssistantContentPartText("inspect")}
	legacy := projectAssistantStartRequestDigestWithSkills("alice", "inspect", store.AssistantRunModeDefault, nil)
	withParts := projectAssistantStartRequestDigestWithSelectionsAndParts("alice", "inspect", store.AssistantRunModeDefault, nil, nil, parts)
	if legacy == withParts {
		t.Fatal("content parts did not alter request identity")
	}
	run := store.AssistantRun{Mode: store.AssistantRunModeDefault}
	if err := bindProjectAssistantStartContentPartsAudit(&run, parts); err != nil {
		t.Fatalf("bind content parts audit: %v", err)
	}
	if got := projectAssistantContentPartsFromRunAudit(run); len(got) != 1 || got[0].Text != "inspect" {
		t.Fatalf("audit parts = %#v", got)
	}
	data := projectAssistantThreadSelectionDataWithParts(nil, nil, parts)
	var projection map[string][]projectAssistantContentPart
	if err := json.Unmarshal(data, &projection); err != nil || len(projection["contentParts"]) != 1 {
		t.Fatalf("thread projection = %s, err=%v", data, err)
	}
}

func TestProjectAssistantContentPartsDurableReplayAndRepairProjection(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	resource := projectAssistantContextResourceInput{
		Provider: "demo",
		ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: "demo.example/v1", Kind: "Table", Resource: "tables",
		},
	}
	receipt := projectAssistantContextResourceReceipt{
		Provider: resource.Provider, ResourceRef: resource.ResourceRef,
		UID: "uid-orders", ResourceVersion: "17", CatalogDigest: "sha256:catalog",
	}
	parts := []projectAssistantContentPart{
		projectAssistantContentPartText("inspect "),
		projectAssistantContentPartResource(0),
	}
	selection := projectAssistantDurableSkillSelection{
		ContextResources:        []projectAssistantContextResourceInput{resource},
		ContextResourceReceipts: []projectAssistantContextResourceReceipt{receipt},
		ContentParts:            parts,
	}
	start := func(store.AssistantRun, store.Message, bool) error { return nil }
	first, err := server.startProjectAssistantRunDurablyWithModeAndSkills(
		ctx, scope, "alice", "inspect [@resource:demo/demo.example/v1/Table/tables/orders]", "content-parts-1", store.AssistantRunModeDefault, selection, start,
	)
	if err != nil || !first.Started {
		t.Fatalf("first durable start = %#v, %v", first, err)
	}
	run, err := messages.GetAssistantRun(ctx, scope, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectAssistantContentPartsFromRunAudit(run); len(got) != 2 || got[1].Type != projectAssistantContentPartResourceType || got[1].ResourceIndex != 0 {
		t.Fatalf("durable audit content parts = %#v", got)
	}
	if got := projectAssistantContextResourceReceiptsFromRunAudit(run); len(got) != 1 || got[0] != receipt {
		t.Fatalf("durable audit resource receipt = %#v, want %#v", got, receipt)
	}

	replay, err := server.startProjectAssistantRunDurablyWithModeAndSkills(
		ctx, scope, "alice", "inspect [@resource:demo/demo.example/v1/Table/tables/orders]", "content-parts-1", store.AssistantRunModeDefault, selection, start,
	)
	if err != nil || replay.Started || replay.Run.ID != first.Run.ID {
		t.Fatalf("exact durable replay = %#v, %v", replay, err)
	}
	reordered := selection
	reordered.ContentParts = []projectAssistantContentPart{selection.ContentParts[1], selection.ContentParts[0]}
	if _, err := server.startProjectAssistantRunDurablyWithModeAndSkills(
		ctx, scope, "alice", "inspect [@resource:demo/demo.example/v1/Table/tables/orders]", "content-parts-1", store.AssistantRunModeDefault, reordered, start,
	); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("reordered replay error = %v, want run conflict", err)
	}

	now := run.CreatedAt
	thread := store.AssistantThread{ID: "thread-content-parts", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantThread(ctx, scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	turn, err := server.repairProjectAssistantThreadTurn(ctx, scope, thread, assistantThreadTurnCreateRequest{
		Content:             "inspect [@resource:demo/demo.example/v1/Table/tables/orders]",
		ClientUserMessageID: "content-parts-1",
		CollaborationMode:   store.AssistantRunModeDefault,
		ContextResources:    []projectAssistantContextResourceInput{resource},
		ContentParts:        parts,
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	if turn.ID != run.ID || turn.Status != store.AssistantTurnStatusInProgress {
		t.Fatalf("repaired turn = %#v, want in-progress run %s", turn, run.ID)
	}
	events, err := messages.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	items := materializeAssistantThreadItems(events)
	if len(items) < 1 || items[0].Type != assistantThreadEventUserMessage {
		t.Fatalf("repaired thread items = %#v", items)
	}
	var data struct {
		ContextResources []projectAssistantContextResourceInput `json:"contextResources"`
		ContentParts     []projectAssistantContentPart          `json:"contentParts"`
	}
	if err := json.Unmarshal(items[0].Data, &data); err != nil {
		t.Fatalf("decode repaired user projection: %v", err)
	}
	if len(data.ContextResources) != 1 || data.ContextResources[0].ResourceRef.Name != "orders" || len(data.ContentParts) != 2 {
		t.Fatalf("repaired public projection = %#v", data)
	}
	if strings.Contains(string(items[0].Data), "uid-orders") || strings.Contains(string(items[0].Data), "resourceVersion") || strings.Contains(string(items[0].Data), "catalogDigest") {
		t.Fatalf("repaired projection exposed private receipt fields: %s", items[0].Data)
	}
}
