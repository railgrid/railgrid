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
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantContextResourceNormalizationCanonicalizesAndBounds(t *testing.T) {
	ref := func(name string) projectAssistantContextResourceInput {
		return projectAssistantContextResourceInput{
			Provider: " databricks ",
			ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
				Name: " " + name + " ", APIVersion: " apps.example/v1 ", Kind: " Table ", Resource: " tables ",
			},
		}
	}
	requested := make([]projectAssistantContextResourceInput, 0, projectAssistantMaxContextResources+1)
	for i := 0; i < projectAssistantMaxContextResources; i++ {
		requested = append(requested, ref(string(rune('a'+i))))
	}
	// A duplicate does not consume a canonical selection slot.
	requested = append(requested, ref("a"))
	canonical, err := normalizeProjectAssistantContextResources(requested)
	if err != nil {
		t.Fatalf("normalize canonical selections: %v", err)
	}
	if len(canonical) != projectAssistantMaxContextResources {
		t.Fatalf("canonical selection count = %d, want %d", len(canonical), projectAssistantMaxContextResources)
	}
	if canonical[0].Provider != "databricks" || canonical[0].ResourceRef.Name != "a" || canonical[0].ResourceRef.APIVersion != "apps.example/v1" {
		t.Fatalf("canonical selection = %#v, want trimmed provider/reference", canonical[0])
	}
	if _, err := normalizeProjectAssistantContextResources(append(requested, ref("z"))); err == nil {
		t.Fatal("expected more than eight unique context resources to be rejected")
	}
}

func TestProjectAssistantContextResourceValidationProducesImmutableReceipt(t *testing.T) {
	ref := &aiv1alpha1.ProjectProviderResourceReference{Name: "orders", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables"}
	discovery := automaticIntegrationDiscovery{
		targets: []automaticIntegrationTarget{{
			provider: "databricks", ref: ref, uid: "uid-orders", resourceVersion: "17",
			actions: []aiv1alpha1.ProjectProviderActionSpec{{Name: "query_table", Version: "v1", SchemaDigest: "sha256:query"}},
		}},
		failedResourceTypes: map[string]struct{}{},
	}
	requested, err := normalizeProjectAssistantContextResources([]projectAssistantContextResourceInput{{
		Provider: " databricks ", ResourceRef: *ref,
	}})
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := validateDiscoveredProjectAssistantContextResources(discovery, requested)
	if err != nil {
		t.Fatalf("validate selected resource: %v", err)
	}
	if len(receipts) != 1 || receipts[0].UID != "uid-orders" || receipts[0].ResourceVersion != "17" || !strings.HasPrefix(receipts[0].CatalogDigest, "sha256:") {
		t.Fatalf("receipt = %#v, want immutable UID/resourceVersion/catalog digest", receipts)
	}

	missing := append([]projectAssistantContextResourceInput(nil), requested...)
	missing[0].ResourceRef.Name = "customers"
	if _, err := validateDiscoveredProjectAssistantContextResources(discovery, missing); !errors.Is(err, errProjectAssistantContextResourceStale) {
		t.Fatalf("missing selected resource error = %v, want stale", err)
	}
	discovery.failedResourceTypes[automaticDiscoveryResourceTypeKey("databricks", ref)] = struct{}{}
	if _, err := validateDiscoveredProjectAssistantContextResources(discovery, missing); !errors.Is(err, errProjectAssistantContextResourceUnavailable) {
		t.Fatalf("failed resource-list error = %v, want unavailable", err)
	}
	if _, err := validateDiscoveredProjectAssistantContextResources(automaticIntegrationDiscovery{catalogUnavailable: true}, requested); !errors.Is(err, errProjectAssistantContextResourceUnavailable) {
		t.Fatalf("catalog error = %v, want unavailable", err)
	}
	withoutIdentity := discovery
	withoutIdentity.targets = append([]automaticIntegrationTarget(nil), discovery.targets...)
	withoutIdentity.targets[0].uid = ""
	if _, err := validateDiscoveredProjectAssistantContextResources(withoutIdentity, requested); !errors.Is(err, errProjectAssistantContextResourceUnavailable) {
		t.Fatalf("missing object identity error = %v, want unavailable", err)
	}
}

func TestProjectAssistantContextResourceCatalogDigestScopesToSelectedContract(t *testing.T) {
	selectedRef := &aiv1alpha1.ProjectProviderResourceReference{Name: "orders", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables"}
	selected := automaticIntegrationTarget{
		provider: "databricks", ref: selectedRef, uid: "uid-orders", resourceVersion: "17",
		actions: []aiv1alpha1.ProjectProviderActionSpec{{Name: "query_table", Version: "v1", SchemaDigest: "sha256:query"}},
	}
	base := automaticIntegrationDiscovery{targets: []automaticIntegrationTarget{selected}, failedResourceTypes: map[string]struct{}{}}
	requested := []projectAssistantContextResourceInput{{Provider: "databricks", ResourceRef: *selectedRef}}
	baseReceipts, err := validateDiscoveredProjectAssistantContextResources(base, requested)
	if err != nil {
		t.Fatalf("validate base selection: %v", err)
	}
	baseDigest := baseReceipts[0].CatalogDigest

	// A second discovered object and its identity must not alter the selected
	// object's action-contract digest.
	unrelated := base
	unrelated.targets = append([]automaticIntegrationTarget(nil), base.targets...)
	unrelated.targets = append(unrelated.targets, automaticIntegrationTarget{
		provider: "databricks",
		ref:      &aiv1alpha1.ProjectProviderResourceReference{Name: "customers", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables"},
		uid:      "uid-customers", resourceVersion: "91",
		actions: []aiv1alpha1.ProjectProviderActionSpec{{Name: "query_table", Version: "v1", SchemaDigest: "sha256:query"}},
	})
	unrelatedReceipts, err := validateDiscoveredProjectAssistantContextResources(unrelated, requested)
	if err != nil {
		t.Fatalf("validate unrelated selection: %v", err)
	}
	if unrelatedReceipts[0].CatalogDigest != baseDigest {
		t.Fatalf("unrelated target changed catalog digest: base=%q unrelated=%q", baseDigest, unrelatedReceipts[0].CatalogDigest)
	}
	selectedRVChanged := base
	selectedRVChanged.targets = append([]automaticIntegrationTarget(nil), base.targets...)
	selectedRVChanged.targets[0].resourceVersion = "18"
	rvReceipts, err := validateDiscoveredProjectAssistantContextResources(selectedRVChanged, requested)
	if err != nil {
		t.Fatalf("validate selected resource-version change: %v", err)
	}
	if rvReceipts[0].CatalogDigest != baseDigest {
		t.Fatalf("selected resourceVersion changed catalog digest: base=%q changed=%q", baseDigest, rvReceipts[0].CatalogDigest)
	}

	actionChanged := base
	actionChanged.targets = append([]automaticIntegrationTarget(nil), base.targets...)
	actionChanged.targets[0].actions = []aiv1alpha1.ProjectProviderActionSpec{{Name: "query_table", Version: "v2", SchemaDigest: "sha256:query-v2"}}
	actionReceipts, err := validateDiscoveredProjectAssistantContextResources(actionChanged, requested)
	if err != nil {
		t.Fatalf("validate selected action-contract change: %v", err)
	}
	if actionReceipts[0].CatalogDigest == baseDigest {
		t.Fatalf("selected action contract did not change catalog digest: %q", baseDigest)
	}
}

func TestProjectAssistantContextResourceReceiptsSurviveAuditCheckpointAndPromptProjection(t *testing.T) {
	receipts := []projectAssistantContextResourceReceipt{{
		Provider: "databricks",
		ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables",
		},
		UID: "uid-orders", ResourceVersion: "17", CatalogDigest: "sha256:catalog",
	}}
	run := store.AssistantRun{}
	if err := bindProjectAssistantStartContextResourceAudit(&run, receipts); err != nil {
		t.Fatalf("bind audit receipt: %v", err)
	}
	if got := projectAssistantContextResourceReceiptsFromRunAudit(run); len(got) != 1 || got[0] != receipts[0] {
		t.Fatalf("audit receipts = %#v, want %#v", got, receipts)
	}
	state := newProjectEinoAssistantRunState()
	state.SetContextResources(receipts)
	checkpoint := state.CheckpointState()
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if got := restored.ContextResources(); len(got) != 1 || got[0] != receipts[0] {
		t.Fatalf("checkpoint receipts = %#v, want %#v", got, receipts)
	}
	prompt := projectAssistantContextResourcesPrompt(receipts)
	for _, phrase := range []string{"untrusted", "no authority", "no evidence", "no action"} {
		if !strings.Contains(strings.ToLower(prompt), phrase) {
			t.Fatalf("prompt %q missing safety phrase %q", prompt, phrase)
		}
	}
	threadData := projectAssistantThreadSelectionData(nil, receipts)
	if strings.Contains(string(threadData), "uid-orders") || strings.Contains(string(threadData), "resourceVersion") || strings.Contains(string(threadData), "catalogDigest") {
		t.Fatalf("thread data exposed private receipt fields: %s", threadData)
	}
	var view struct {
		ContextResources []projectAssistantContextResourceInput `json:"contextResources"`
	}
	if err := json.Unmarshal(threadData, &view); err != nil {
		t.Fatalf("decode thread data: %v", err)
	}
	if len(view.ContextResources) != 1 || view.ContextResources[0].ResourceRef.Name != "orders" {
		t.Fatalf("thread context-resource view = %#v", view.ContextResources)
	}
}

func TestProjectAssistantContextResourceReplayIdentityUsesCanonicalReferences(t *testing.T) {
	first := []projectAssistantContextResourceInput{{
		Provider: " databricks ", ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
			Name: " orders ", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables",
		},
	}}
	second := []projectAssistantContextResourceInput{{
		Provider: "databricks", ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: "apps.example/v1", Kind: "Table", Resource: "tables",
		},
	}}
	if got, want := projectAssistantStartRequestDigestWithSelections("alice", "build", store.AssistantRunModeDefault, nil, first), projectAssistantStartRequestDigestWithSelections("alice", "build", store.AssistantRunModeDefault, nil, second); got != want {
		t.Fatalf("canonical replay digests differ: %q != %q", got, want)
	}
	changed := append([]projectAssistantContextResourceInput(nil), second...)
	changed[0].ResourceRef.Name = "customers"
	if got, want := projectAssistantStartRequestDigestWithSelections("alice", "build", store.AssistantRunModeDefault, nil, changed), projectAssistantStartRequestDigestWithSelections("alice", "build", store.AssistantRunModeDefault, nil, second); got == want {
		t.Fatal("different context-resource selection reused the same replay identity")
	}
}

func TestProjectAssistantContextResourceInheritedContinuationPreservesReceiptAfterDiscovery(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: aiv1alpha1.ProjectSpec{DisplayName: "Demo"}}
	server, client := automaticIntegrationTestServer(t, project, []string{"orders"})
	discoveryCalls := 0
	resolver := server.providerActionCatalogResolver
	server.providerActionCatalogResolver = func(ctx context.Context, id identity) ([]providerCatalogEntry, error) {
		discoveryCalls++
		return resolver(ctx, id)
	}
	inherited := []projectAssistantContextResourceReceipt{{
		Provider: "databricks",
		ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource,
		},
		UID: "predecessor-uid", ResourceVersion: "predecessor-rv", CatalogDigest: "sha256:predecessor-catalog",
	}}
	updated, got, err := server.prepareProjectAssistantContextResources(context.Background(), client, identity{user: "alice"}, project, projectAssistantContextResourceInputsFromReceipts(inherited), inherited)
	if err != nil {
		t.Fatalf("prepare inherited continuation: %v", err)
	}
	if updated == nil || len(updated.Spec.Environments) == 0 || discoveryCalls != 1 {
		t.Fatalf("inherited continuation discovery/materialization = project=%#v calls=%d, want one pass", updated, discoveryCalls)
	}
	if len(got) != 1 || got[0] != inherited[0] {
		t.Fatalf("inherited receipts = %#v, want exact predecessor receipt %#v", got, inherited)
	}
}

func TestProjectAssistantContextResourceExplicitContinuationValidatesReplacement(t *testing.T) {
	ref := projectAssistantContextResourceInput{Provider: "databricks", ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
		Name: "orders", APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource,
	}}
	server := &Server{}
	_, _, err := server.prepareProjectAssistantContextResources(context.Background(), nil, identity{}, &aiv1alpha1.Project{}, []projectAssistantContextResourceInput{ref}, nil)
	if !errors.Is(err, errProjectAssistantContextResourceStale) {
		t.Fatalf("explicit replacement validation error = %v, want stale", err)
	}
}

func TestProjectAssistantContextResourceErrorsMapToHTTPStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "stale", err: errProjectAssistantContextResourceStale, want: 409},
		{name: "unavailable", err: errProjectAssistantContextResourceUnavailable, want: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			(&Server{}).writeAssistantThreadError(recorder, test.err)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestProjectAssistantContextResourceNoSelectionPreservesLegacyTurnShape(t *testing.T) {
	if got, err := normalizeProjectAssistantContextResources(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil context-resource normalization = %#v, %v; want empty success", got, err)
	}
	if got, err := validateDiscoveredProjectAssistantContextResources(automaticIntegrationDiscovery{catalogUnavailable: true}, nil); err != nil || len(got) != 0 {
		t.Fatalf("nil selection validation = %#v, %v; want empty success even with unavailable catalog", got, err)
	}
	if prompt := projectAssistantContextResourcesPrompt(nil); prompt != "" {
		t.Fatalf("nil selection prompt = %q, want omitted", prompt)
	}
	if data := projectAssistantThreadSelectionData(nil, nil); data != nil {
		t.Fatalf("nil selection thread data = %s, want omitted", data)
	}
	legacy := projectAssistantStartRequestDigestWithSkills("alice", "build", store.AssistantRunModeDefault, nil)
	withNoSelection := projectAssistantStartRequestDigestWithSelections("alice", "build", store.AssistantRunModeDefault, nil, nil)
	if withNoSelection != legacy {
		t.Fatalf("no-selection replay digest = %q, legacy digest = %q; compatibility changed", withNoSelection, legacy)
	}
}

func TestProjectAssistantContextResourceReceiptStateIsDetachedAcrossCheckpointBoundaries(t *testing.T) {
	receipts := []projectAssistantContextResourceReceipt{{
		Provider: "demo",
		ResourceRef: aiv1alpha1.ProjectProviderResourceReference{
			Name: "orders", APIVersion: "demo.example/v1", Kind: "Table", Resource: "tables",
		},
		UID: "uid-1", ResourceVersion: "3", CatalogDigest: "sha256:catalog",
	}}
	state := newProjectEinoAssistantRunState()
	state.SetContextResources(receipts)
	receipts[0].ResourceRef.Name = "mutated-input"
	if got := state.ContextResources(); got[0].ResourceRef.Name != "orders" {
		t.Fatalf("state retained caller-owned receipt slice: %#v", got)
	}
	checkpoint := state.CheckpointState()
	checkpoint.SelectedContextResourceReceipts[0].ResourceRef.Name = "mutated-checkpoint"
	if got := state.ContextResources(); got[0].ResourceRef.Name != "orders" {
		t.Fatalf("checkpoint exposed state-owned receipt slice: %#v", got)
	}
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	checkpoint.SelectedContextResourceReceipts[0].ResourceRef.Name = "mutated-after-restore"
	if got := restored.ContextResources(); got[0].ResourceRef.Name != "mutated-checkpoint" {
		t.Fatalf("restored state did not preserve checkpoint snapshot: %#v", got)
	}
}
