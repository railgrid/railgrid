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

package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadActivationCatalog(t *testing.T) {
	root := filepath.Join("..", "..")
	eventDirs := []string{
		filepath.Join(root, "telemetry", "events"),
		filepath.Join(root, "providers", "edges", "telemetry", "events"),
		filepath.Join(root, "providers", "app-studio", "telemetry", "events"),
		filepath.Join(root, "providers", "agents", "telemetry", "events"),
	}
	registry, err := LoadWithEventDirs(eventDirs, filepath.Join(root, "telemetry", "metrics"), filepath.Join(root, "telemetry", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Events) != 9 || len(registry.Metrics) != 12 {
		t.Fatalf("catalog sizes = (%d events, %d metrics), want (9, 12)", len(registry.Events), len(registry.Metrics))
	}
	wantActions := []string{
		"organization_created", "workspace_created", "provider_enabled", "edge_first_ready",
		"app_studio_project_created", "app_studio_preview_ready", "app_studio_project_published",
		"agents_agent_created", "agents_run_terminal",
	}
	gotActions := make(map[string]EventDefinition, len(registry.Events))
	for _, event := range registry.Events {
		gotActions[event.Action] = event
	}
	for _, action := range wantActions {
		event, ok := gotActions[action]
		if !ok {
			t.Fatalf("catalog missing event %q", action)
		}
		if event.SourcePath == "" {
			t.Fatalf("event %q did not retain its source path", action)
		}
	}
	for _, removed := range []string{"first_edge_ready", "app_studio_app_published", "agents_terminal_run_finished"} {
		if _, ok := gotActions[removed]; ok {
			t.Fatalf("catalog retained obsolete event %q", removed)
		}
	}
	for _, action := range []string{"edge_first_ready", "agents_run_terminal"} {
		event := gotActions[action]
		if contains(event.Identifiers, "actor") {
			t.Fatalf("background event %q unexpectedly identifies actor", action)
		}
		if !contains(event.Identifiers, "resource") {
			t.Fatalf("background event %q must identify its resource", action)
		}
	}
	provider := gotActions["provider_enabled"].AdditionalProperties["provider"]
	if !contains(provider.Enum, "app-studio") || !contains(provider.Enum, "vibe-studio") {
		t.Fatalf("provider enum = %v, want actual provider names", provider.Enum)
	}
}

func TestLoadRejectsJSONSchemaViolation(t *testing.T) {
	root := filepath.Join("..", "..")
	schema, err := compileSchema(filepath.Join(root, "telemetry", "schema", "event.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSchemaDocument(schema, []byte("version: 1\naction: organization_created\n"), "invalid-event.yaml"); err == nil {
		t.Fatal("JSON Schema accepted an event missing required fields")
	}
}

func TestValidateRejectsUnboundedStringProperty(t *testing.T) {
	event := validEvent()
	event.AdditionalProperties["free_text"] = PropertyDefinition{Description: "unsafe", Type: "string"}
	if err := validateEvent(event); err == nil {
		t.Fatal("unbounded string property accepted")
	}
}

func TestValidateRejectsUnboundedNumericProperty(t *testing.T) {
	event := validEvent()
	event.AdditionalProperties["duration_ms"] = PropertyDefinition{Description: "unsafe", Type: "number"}
	if err := validateEvent(event); err == nil {
		t.Fatal("unbounded numeric property accepted")
	}
}

func TestValidateRejectsRawContentProperty(t *testing.T) {
	event := validEvent()
	event.AdditionalProperties["content_type"] = PropertyDefinition{Description: "unsafe", Type: "string", Enum: []string{"text"}}
	if err := validateEvent(event); err == nil {
		t.Fatal("raw content property accepted")
	}
}

func TestValidateRejectsUnsupportedAggregateEventClass(t *testing.T) {
	event := validEvent()
	event.Privacy.Class = "aggregate"
	if err := validateEvent(event); err == nil {
		t.Fatal("aggregate event class accepted despite carrying identifiers")
	}
}

func TestCatalogSourcePathsDoNotDependOnWorkingDirectory(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	eventDirs := []string{
		filepath.Join(repositoryRoot, "telemetry", "events"),
		filepath.Join(repositoryRoot, "providers", "edges", "telemetry", "events"),
		filepath.Join(repositoryRoot, "providers", "app-studio", "telemetry", "events"),
		filepath.Join(repositoryRoot, "providers", "agents", "telemetry", "events"),
	}
	load := func() Registry {
		registry, err := LoadWithEventDirs(eventDirs, filepath.Join(repositoryRoot, "telemetry", "metrics"), filepath.Join(repositoryRoot, "telemetry", "schema"))
		if err != nil {
			t.Fatal(err)
		}
		return registry
	}
	first := load()
	t.Chdir(t.TempDir())
	second := load()
	if first.Events[0].SourcePath != second.Events[0].SourcePath || first.Metrics[0].SourcePath != second.Metrics[0].SourcePath {
		t.Fatalf("source paths changed with cwd: %q/%q versus %q/%q", first.Events[0].SourcePath, first.Metrics[0].SourcePath, second.Events[0].SourcePath, second.Metrics[0].SourcePath)
	}
}

func TestValidateRejectsRetentionOverNinetyDays(t *testing.T) {
	event := validEvent()
	event.RetentionDays = MaxRawRetentionDays + 1
	if err := validateEvent(event); err == nil {
		t.Fatal("retention over the raw-data limit accepted")
	}
}

func TestValidateRejectsDuplicateIdentifiers(t *testing.T) {
	event := validEvent()
	event.Identifiers = append(event.Identifiers, "org")
	if err := validateEvent(event); err == nil {
		t.Fatal("duplicate identifier accepted")
	}
	event = validEvent()
	event.Privacy.Pseudonymize = append(event.Privacy.Pseudonymize, "org")
	if err := validateEvent(event); err == nil {
		t.Fatal("duplicate pseudonymized identifier accepted")
	}
}

func TestValidateRejectsDynamicMetricLabel(t *testing.T) {
	event := validEvent()
	metric := validMetric(event.Action)
	metric.Labels["request_id"] = LabelDefinition{Description: "request", Values: []string{"anything"}}
	if err := validateMetric(metric, map[string]EventDefinition{event.Action: event}); err == nil {
		t.Fatal("dynamic metric label accepted")
	}
}

func TestValidateRejectsUnknownFilterAndUniqueIdentifier(t *testing.T) {
	event := validEvent()
	metric := validMetric(event.Action)
	metric.Events[0].Unique = "workspace"
	if err := validateMetric(metric, map[string]EventDefinition{event.Action: event}); err == nil {
		t.Fatal("undeclared unique identifier accepted")
	}
	metric = validMetric(event.Action)
	metric.Events[0].Filter["outcome"] = "unknown"
	if err := validateMetric(metric, map[string]EventDefinition{event.Action: event}); err == nil {
		t.Fatal("out-of-vocabulary filter accepted")
	}
}

func TestLoadRejectsFilenameActionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrong.yaml")
	if err := os.WriteFile(path, []byte("version: 1\naction: right\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEvents(dir); err == nil {
		t.Fatal("filename/action mismatch accepted")
	}
}

func TestLoadEventsRecursivelyRejectsDuplicateActions(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "platform"), filepath.Join(root, "provider")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "duplicate.yaml")
		if err := os.WriteFile(path, []byte("version: 1\ninternal_events: true\naction: duplicate\ndescription: Duplicate event.\nowner: platform\nproduct_group: platform\nproduct_categories: [test]\nidentifiers: [org]\ntiers: [free]\nmilestone: '0.1'\nstatus: active\nintroduced_by_url: https://github.com/faroshq/faros\nretention_days: 90\nprivacy:\n  class: pseudonymous\n  pseudonymize: [org]\n  no_raw_content: true\nadditional_properties: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	events, err := loadEventsFromDirs([]string{root}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Registry{Events: events}).Validate(); err == nil {
		t.Fatal("duplicate action across recursive roots accepted")
	}
}

func validEvent() EventDefinition {
	return EventDefinition{
		Version: 1, InternalEvents: true, Action: "valid_event", Description: "A valid event.", Owner: "platform",
		ProductGroup: "app_studio", ProductCategories: []string{"assistant"}, Identifiers: []string{"org", "actor"},
		Tiers: []string{"free"}, Milestone: "0.1", Status: "active", IntroducedByURL: "https://github.com/faroshq/faros", RetentionDays: 90,
		Privacy:              PrivacyDefinition{Class: "pseudonymous", Pseudonymize: []string{"org", "actor"}, NoRawContent: true},
		AdditionalProperties: map[string]PropertyDefinition{"outcome": {Description: "Bounded outcome.", Type: "string", Enum: []string{"success"}}},
	}
}

func validMetric(action string) MetricDefinition {
	return MetricDefinition{
		Version: 1, KeyPath: "valid_events_total", Description: "Valid events.", Owner: "platform", ProductGroup: "app_studio",
		ProductCategories: []string{"assistant"}, ValueType: "number", MetricKind: "counter", Status: "active",
		TimeFrame: "all", DataSource: "internal_events", Events: []EventSelection{{Name: action, Filter: map[string]string{}}},
		Labels: map[string]LabelDefinition{"outcome": {Description: "Outcome.", Values: []string{"success"}}},
	}
}
