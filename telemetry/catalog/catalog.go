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

// Package catalog defines and validates the OSS-visible telemetry catalog.
// Definitions are deliberately provider-neutral: runtime emitters may route a
// validated event to one or more sinks without changing the public contract.
package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

const (
	CurrentVersion       = 1
	MaxRawRetentionDays  = 90
	MaxDescriptionBytes  = 512
	MaxPropertyValueSize = 64
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	propertyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	actionPattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	keyPathPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$`)
	ownerPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	enumValuePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

var allowedIdentifiers = map[string]struct{}{
	"org": {}, "workspace": {}, "scope": {}, "project": {}, "actor": {}, "resource": {}, "run": {},
}

var allowedTiers = map[string]struct{}{
	"free": {}, "premium": {}, "ultimate": {},
}

var allowedPropertyTypes = map[string]struct{}{
	"boolean": {}, "number": {}, "string": {},
}

var allowedPrivacyClasses = map[string]struct{}{"pseudonymous": {}}

var allowedMetricKinds = map[string]struct{}{
	"counter": {}, "funnel": {},
}

// PropertyDefinition is the declared shape of one additional event property.
// String properties enumerate their bounded vocabulary. Numeric properties
// declare an explicit range so they cannot smuggle an unbounded measurement
// into the product-event stream.
type PropertyDefinition struct {
	Description string   `json:"description" yaml:"description"`
	Type        string   `json:"type" yaml:"type"`
	Enum        []string `json:"enum,omitempty" yaml:"enum,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty" yaml:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty" yaml:"maximum,omitempty"`
}

// PrivacyDefinition makes the data handling boundary part of the catalog.
type PrivacyDefinition struct {
	Class        string   `json:"class" yaml:"class"`
	Pseudonymize []string `json:"pseudonymize,omitempty" yaml:"pseudonymize,omitempty"`
	NoRawContent bool     `json:"no_raw_content" yaml:"no_raw_content"`
}

// EventDefinition is the GitLab-shaped declaration for one Faros action.
// SourcePath is populated by the loader and is not accepted from YAML. It is
// retained for generated catalogs and review docs so ownership stays visible.
type EventDefinition struct {
	Version              int                           `json:"version" yaml:"version"`
	InternalEvents       bool                          `json:"internal_events" yaml:"internal_events"`
	Action               string                        `json:"action" yaml:"action"`
	Description          string                        `json:"description" yaml:"description"`
	Owner                string                        `json:"owner" yaml:"owner"`
	ProductGroup         string                        `json:"product_group" yaml:"product_group"`
	ProductCategories    []string                      `json:"product_categories" yaml:"product_categories"`
	Identifiers          []string                      `json:"identifiers" yaml:"identifiers"`
	Tiers                []string                      `json:"tiers" yaml:"tiers"`
	Milestone            string                        `json:"milestone,omitempty" yaml:"milestone,omitempty"`
	Status               string                        `json:"status" yaml:"status"`
	IntroducedByURL      string                        `json:"introduced_by_url,omitempty" yaml:"introduced_by_url,omitempty"`
	RemovedByURL         string                        `json:"removed_by_url,omitempty" yaml:"removed_by_url,omitempty"`
	MilestoneRemoved     string                        `json:"milestone_removed,omitempty" yaml:"milestone_removed,omitempty"`
	RetentionDays        int                           `json:"retention_days" yaml:"retention_days"`
	Privacy              PrivacyDefinition             `json:"privacy" yaml:"privacy"`
	AdditionalProperties map[string]PropertyDefinition `json:"additional_properties" yaml:"additional_properties"`
	SourcePath           string                        `json:"source_path,omitempty" yaml:"-"`
}

// LabelDefinition lists the only values a metric exposition may render for a
// label. Request-derived values are never valid metric labels.
type LabelDefinition struct {
	Description string   `json:"description" yaml:"description"`
	Values      []string `json:"values" yaml:"values"`
}

type EventSelection struct {
	Name   string            `json:"name" yaml:"name"`
	Unique string            `json:"unique,omitempty" yaml:"unique,omitempty"`
	Filter map[string]string `json:"filter,omitempty" yaml:"filter,omitempty"`
}

// MetricDefinition describes an aggregate or ordered funnel derived from one
// or more events. Metric definitions stay centralized even when event
// definitions are owned by provider subtrees.
type MetricDefinition struct {
	Version           int                        `json:"version" yaml:"version"`
	KeyPath           string                     `json:"key_path" yaml:"key_path"`
	Description       string                     `json:"description" yaml:"description"`
	Owner             string                     `json:"owner" yaml:"owner"`
	ProductGroup      string                     `json:"product_group" yaml:"product_group"`
	ProductCategories []string                   `json:"product_categories" yaml:"product_categories"`
	ValueType         string                     `json:"value_type" yaml:"value_type"`
	MetricKind        string                     `json:"metric_kind" yaml:"metric_kind"`
	Status            string                     `json:"status" yaml:"status"`
	TimeFrame         string                     `json:"time_frame" yaml:"time_frame"`
	DataSource        string                     `json:"data_source" yaml:"data_source"`
	IntroducedByURL   string                     `json:"introduced_by_url,omitempty" yaml:"introduced_by_url,omitempty"`
	RemovedByURL      string                     `json:"removed_by_url,omitempty" yaml:"removed_by_url,omitempty"`
	MilestoneRemoved  string                     `json:"milestone_removed,omitempty" yaml:"milestone_removed,omitempty"`
	Events            []EventSelection           `json:"events" yaml:"events"`
	Labels            map[string]LabelDefinition `json:"labels" yaml:"labels"`
	SourcePath        string                     `json:"source_path,omitempty" yaml:"-"`
}

type Registry struct {
	Events  []EventDefinition
	Metrics []MetricDefinition
}

// Load reads one YAML document per file from one event directory and the
// central metric directory. Event subdirectories are discovered recursively.
// Use LoadFromEventDirs when provider-owned event roots are also needed.
func Load(eventsDir, metricsDir string) (Registry, error) {
	schemaDir := filepath.Join(filepath.Dir(eventsDir), "schema")
	return LoadWithSchemas(eventsDir, metricsDir, schemaDir)
}

// LoadWithSchemas reads and validates catalog definitions against the checked-in
// JSON Schemas before applying the cross-file semantic rules in Validate.
func LoadWithSchemas(eventsDir, metricsDir, schemaDir string) (Registry, error) {
	return LoadWithEventDirs([]string{eventsDir}, metricsDir, schemaDir)
}

// LoadFromEventDirs loads platform and provider event roots together. All
// action names must be globally unique, even when their definitions live in
// different provider modules.
func LoadFromEventDirs(eventDirs []string, metricsDir string) (Registry, error) {
	schemaDir := filepath.Join(filepath.Dir(metricsDir), "schema")
	return LoadWithEventDirs(eventDirs, metricsDir, schemaDir)
}

// LoadWithEventDirs is the explicit-root variant used by code generation and
// tests that need the full platform/provider catalog.
func LoadWithEventDirs(eventDirs []string, metricsDir, schemaDir string) (Registry, error) {
	repositoryRoot := filepath.Dir(filepath.Dir(filepath.Clean(schemaDir)))
	if absoluteRoot, err := filepath.Abs(repositoryRoot); err == nil {
		repositoryRoot = absoluteRoot
	}
	eventSchema, err := compileSchema(filepath.Join(schemaDir, "event.schema.json"))
	if err != nil {
		return Registry{}, err
	}
	metricSchema, err := compileSchema(filepath.Join(schemaDir, "metric.schema.json"))
	if err != nil {
		return Registry{}, err
	}
	events, err := loadEventsFromDirs(eventDirs, repositoryRoot, eventSchema)
	if err != nil {
		return Registry{}, err
	}
	metrics, err := loadMetrics(metricsDir, repositoryRoot, metricSchema)
	if err != nil {
		return Registry{}, err
	}
	registry := Registry{Events: events, Metrics: metrics}
	if err := registry.Validate(); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

// loadEvents is kept small and useful for package tests that exercise a
// single temporary root. It recursively discovers YAML files.
func loadEvents(dir string, schemas ...*jsonschema.Schema) ([]EventDefinition, error) {
	return loadEventsFromDirs([]string{dir}, filepath.Dir(filepath.Clean(dir)), schemas...)
}

func loadEventsFromDirs(dirs []string, displayRoot string, schemas ...*jsonschema.Schema) ([]EventDefinition, error) {
	files, err := yamlFilesInDirs(dirs)
	if err != nil {
		return nil, err
	}
	events := make([]EventDefinition, 0, len(files))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read event %s: %w", path, err)
		}
		if len(schemas) > 0 {
			if err := validateSchemaDocument(schemas[0], raw, path); err != nil {
				return nil, fmt.Errorf("validate event schema %s: %w", path, err)
			}
		}
		var event EventDefinition
		if err := yaml.UnmarshalStrict(raw, &event); err != nil {
			return nil, fmt.Errorf("parse event %s: %w", path, err)
		}
		event.Action = strings.TrimSpace(event.Action)
		want := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if event.Action != want {
			return nil, fmt.Errorf("event %s action %q does not match filename %q", path, event.Action, want)
		}
		event.SourcePath = displayPath(displayRoot, path)
		events = append(events, event)
	}
	return events, nil
}

func loadMetrics(dir, displayRoot string, schemas ...*jsonschema.Schema) ([]MetricDefinition, error) {
	files, err := yamlFiles(dir)
	if err != nil {
		return nil, err
	}
	metrics := make([]MetricDefinition, 0, len(files))
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read metric %s: %w", path, err)
		}
		if len(schemas) > 0 {
			if err := validateSchemaDocument(schemas[0], raw, path); err != nil {
				return nil, fmt.Errorf("validate metric schema %s: %w", path, err)
			}
		}
		var metric MetricDefinition
		if err := yaml.UnmarshalStrict(raw, &metric); err != nil {
			return nil, fmt.Errorf("parse metric %s: %w", path, err)
		}
		metric.SourcePath = displayPath(displayRoot, path)
		metrics = append(metrics, metric)
	}
	return metrics, nil
}

func compileSchema(path string) (*jsonschema.Schema, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve schema %s: %w", path, err)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", path, err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", path, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(absPath, document); err != nil {
		return nil, fmt.Errorf("register schema %s: %w", path, err)
	}
	schema, err := compiler.Compile(absPath)
	if err != nil {
		return nil, fmt.Errorf("compile schema %s: %w", path, err)
	}
	return schema, nil
}

func validateSchemaDocument(schema *jsonschema.Schema, raw []byte, path string) error {
	document, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return fmt.Errorf("convert %s to JSON: %w", path, err)
	}
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return fmt.Errorf("decode %s as JSON: %w", path, err)
	}
	if err := schema.Validate(value); err != nil {
		return err
	}
	return nil
}

// yamlFiles returns YAML files recursively in deterministic path order.
func yamlFiles(dir string) ([]string, error) {
	return yamlFilesInDirs([]string{dir})
}

func yamlFilesInDirs(dirs []string) ([]string, error) {
	seen := make(map[string]struct{})
	files := make([]string, 0)
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		root, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve catalog directory %s: %w", dir, err)
		}
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("read catalog directory %s: %w", dir, err)
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}
			path, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if _, exists := seen[path]; exists {
				return nil
			}
			seen[path] = struct{}{}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read catalog directory %s: %w", dir, err)
		}
	}
	sort.Strings(files)
	return files, nil
}

func displayPath(root, path string) string {
	path = filepath.Clean(path)
	if relative, err := filepath.Rel(filepath.Clean(root), path); err == nil {
		path = relative
	}
	return filepath.ToSlash(path)
}

func (r Registry) Validate() error {
	events := make(map[string]EventDefinition, len(r.Events))
	for _, event := range r.Events {
		if err := validateEvent(event); err != nil {
			return err
		}
		if _, exists := events[event.Action]; exists {
			return fmt.Errorf("duplicate event action %q", event.Action)
		}
		events[event.Action] = event
	}
	keys := make(map[string]struct{}, len(r.Metrics))
	for _, metric := range r.Metrics {
		if err := validateMetric(metric, events); err != nil {
			return err
		}
		if _, exists := keys[metric.KeyPath]; exists {
			return fmt.Errorf("duplicate metric key_path %q", metric.KeyPath)
		}
		keys[metric.KeyPath] = struct{}{}
	}
	return nil
}

func validateEvent(event EventDefinition) error {
	if event.Version != CurrentVersion {
		return fmt.Errorf("event %q has unsupported version %d", event.Action, event.Version)
	}
	if !event.InternalEvents {
		return fmt.Errorf("event %q must set internal_events: true", event.Action)
	}
	if !actionPattern.MatchString(event.Action) || len(event.Action) > 128 {
		return fmt.Errorf("event action %q must be lowercase snake_case and <=128 bytes", event.Action)
	}
	if strings.TrimSpace(event.Description) == "" || len([]byte(event.Description)) > MaxDescriptionBytes {
		return fmt.Errorf("event %q description is required and <=%d bytes", event.Action, MaxDescriptionBytes)
	}
	if !ownerPattern.MatchString(event.Owner) {
		return fmt.Errorf("event %q owner %q must be lowercase and <=64 bytes", event.Action, event.Owner)
	}
	if strings.TrimSpace(event.ProductGroup) == "" || len(event.ProductCategories) == 0 || len(event.Tiers) == 0 {
		return fmt.Errorf("event %q product_group, product_categories, and tiers are required", event.Action)
	}
	if err := validateUniqueStrings("event", event.Action, event.ProductCategories, false); err != nil {
		return err
	}
	if event.Milestone == "" || event.IntroducedByURL == "" {
		return fmt.Errorf("event %q requires milestone and introduced_by_url", event.Action)
	}
	if event.Status != "active" && event.Status != "removed" {
		return fmt.Errorf("event %q has invalid status %q", event.Action, event.Status)
	}
	if event.Status == "removed" && (event.RemovedByURL == "" || event.MilestoneRemoved == "") {
		return fmt.Errorf("removed event %q requires removed_by_url and milestone_removed", event.Action)
	}
	if event.RetentionDays < 1 || event.RetentionDays > MaxRawRetentionDays {
		return fmt.Errorf("event %q retention_days must be between 1 and %d", event.Action, MaxRawRetentionDays)
	}
	if err := validateURL(event.IntroducedByURL, "introduced_by_url", event.Action); err != nil {
		return err
	}
	if err := validateURL(event.RemovedByURL, "removed_by_url", event.Action); err != nil {
		return err
	}
	if len(event.Identifiers) == 0 {
		return fmt.Errorf("event %q requires at least one identifier", event.Action)
	}
	if err := validateIdentifiers(event.Action, "identifiers", event.Identifiers); err != nil {
		return err
	}
	if err := validateIdentifiers(event.Action, "privacy.pseudonymize", event.Privacy.Pseudonymize); err != nil {
		return err
	}
	identifierSet := make(map[string]struct{}, len(event.Identifiers))
	for _, identifier := range event.Identifiers {
		identifierSet[identifier] = struct{}{}
	}
	for _, identifier := range event.Privacy.Pseudonymize {
		if _, ok := identifierSet[identifier]; !ok {
			return fmt.Errorf("event %q pseudonymizes undeclared identifier %q", event.Action, identifier)
		}
	}
	if err := validateUniqueStrings("event", event.Action, event.Tiers, true); err != nil {
		return err
	}
	for _, tier := range event.Tiers {
		if _, ok := allowedTiers[tier]; !ok {
			return fmt.Errorf("event %q has unsupported tier %q", event.Action, tier)
		}
	}
	if _, ok := allowedPrivacyClasses[event.Privacy.Class]; !ok {
		return fmt.Errorf("event %q has unsupported privacy class %q", event.Action, event.Privacy.Class)
	}
	if !event.Privacy.NoRawContent {
		return fmt.Errorf("event %q must set privacy.no_raw_content", event.Action)
	}
	for name, property := range event.AdditionalProperties {
		if err := validateProperty(event.Action, name, property); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentifiers(action, field string, identifiers []string) error {
	seen := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		if !identifierPattern.MatchString(identifier) {
			return fmt.Errorf("event %q has invalid %s identifier %q", action, field, identifier)
		}
		if _, ok := allowedIdentifiers[identifier]; !ok {
			return fmt.Errorf("event %q has unsupported %s identifier %q", action, field, identifier)
		}
		if _, exists := seen[identifier]; exists {
			return fmt.Errorf("event %q repeats %s identifier %q", action, field, identifier)
		}
		seen[identifier] = struct{}{}
	}
	return nil
}

func validateUniqueStrings(kind, owner string, values []string, checkTier bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s %q contains an empty value", kind, owner)
		}
		if checkTier {
			if _, ok := allowedTiers[value]; !ok {
				return fmt.Errorf("%s %q has unsupported tier %q", kind, owner, value)
			}
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s %q repeats value %q", kind, owner, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateProperty(action, name string, property PropertyDefinition) error {
	if forbiddenPropertyName(name) {
		return fmt.Errorf("event %q has forbidden or invalid property %q", action, name)
	}
	if strings.TrimSpace(property.Description) == "" || len([]byte(property.Description)) > MaxDescriptionBytes {
		return fmt.Errorf("event %q property %q requires a description <=%d bytes", action, name, MaxDescriptionBytes)
	}
	if _, ok := allowedPropertyTypes[property.Type]; !ok {
		return fmt.Errorf("event %q property %q has unsupported type %q", action, name, property.Type)
	}
	if property.Type == "string" {
		if len(property.Enum) == 0 {
			return fmt.Errorf("event %q string property %q requires enum", action, name)
		}
		if err := validateEnum(action, name, property.Enum); err != nil {
			return err
		}
		if property.Minimum != nil || property.Maximum != nil {
			return fmt.Errorf("event %q string property %q cannot declare numeric bounds", action, name)
		}
	}
	if property.Type == "number" {
		if len(property.Enum) != 0 {
			return fmt.Errorf("event %q number property %q may not enumerate strings", action, name)
		}
		if property.Minimum == nil || property.Maximum == nil {
			return fmt.Errorf("event %q number property %q requires minimum and maximum", action, name)
		}
		if math.IsNaN(*property.Minimum) || math.IsInf(*property.Minimum, 0) || math.IsNaN(*property.Maximum) || math.IsInf(*property.Maximum, 0) || *property.Minimum > *property.Maximum {
			return fmt.Errorf("event %q number property %q has invalid bounds", action, name)
		}
	}
	if property.Type == "boolean" && (len(property.Enum) != 0 || property.Minimum != nil || property.Maximum != nil) {
		return fmt.Errorf("event %q boolean property %q cannot declare enum or numeric bounds", action, name)
	}
	return nil
}

func validateEnum(action, name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !enumValuePattern.MatchString(value) || len(value) > MaxPropertyValueSize {
			return fmt.Errorf("event %q property %q has invalid enum value %q", action, name, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("event %q property %q repeats enum value %q", action, name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func forbiddenPropertyName(name string) bool {
	if !propertyPattern.MatchString(name) || name == "id" || strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_uuid") {
		return true
	}
	lower := strings.ToLower(name)
	for _, fragment := range []string{"secret", "token", "password", "prompt", "content", "source", "url", "uri", "path", "args", "command", "query", "body", "header", "email"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func validateMetric(metric MetricDefinition, events map[string]EventDefinition) error {
	if metric.Version != CurrentVersion {
		return fmt.Errorf("metric %q has unsupported version %d", metric.KeyPath, metric.Version)
	}
	if !keyPathPattern.MatchString(metric.KeyPath) || len(metric.KeyPath) > 160 {
		return fmt.Errorf("metric key_path %q is invalid", metric.KeyPath)
	}
	if strings.TrimSpace(metric.Description) == "" || len([]byte(metric.Description)) > MaxDescriptionBytes || !ownerPattern.MatchString(metric.Owner) || strings.TrimSpace(metric.ProductGroup) == "" || len(metric.ProductCategories) == 0 {
		return fmt.Errorf("metric %q description, owner, product_group, and product_categories are required", metric.KeyPath)
	}
	if err := validateUniqueStrings("metric", metric.KeyPath, metric.ProductCategories, false); err != nil {
		return err
	}
	if metric.ValueType != "number" {
		return fmt.Errorf("metric %q must have value_type number", metric.KeyPath)
	}
	if _, ok := allowedMetricKinds[metric.MetricKind]; !ok {
		return fmt.Errorf("metric %q has invalid metric_kind %q", metric.KeyPath, metric.MetricKind)
	}
	if metric.Status != "active" && metric.Status != "removed" {
		return fmt.Errorf("metric %q has invalid status %q", metric.KeyPath, metric.Status)
	}
	if metric.TimeFrame != "all" && metric.TimeFrame != "7d" && metric.TimeFrame != "28d" {
		return fmt.Errorf("metric %q has invalid time_frame %q", metric.KeyPath, metric.TimeFrame)
	}
	if metric.DataSource != "internal_events" {
		return fmt.Errorf("metric %q must use data_source internal_events", metric.KeyPath)
	}
	if metric.Status == "removed" && (metric.RemovedByURL == "" || metric.MilestoneRemoved == "") {
		return fmt.Errorf("removed metric %q requires removed_by_url and milestone_removed", metric.KeyPath)
	}
	if err := validateURL(metric.IntroducedByURL, "introduced_by_url", metric.KeyPath); err != nil {
		return err
	}
	if err := validateURL(metric.RemovedByURL, "removed_by_url", metric.KeyPath); err != nil {
		return err
	}
	if len(metric.Events) == 0 {
		return fmt.Errorf("metric %q requires at least one event selection", metric.KeyPath)
	}
	seenEvents := make(map[string]struct{}, len(metric.Events))
	var funnelUnique string
	for _, selection := range metric.Events {
		if !actionPattern.MatchString(selection.Name) || len(selection.Name) > 128 {
			return fmt.Errorf("metric %q references invalid event %q", metric.KeyPath, selection.Name)
		}
		if _, duplicate := seenEvents[selection.Name]; duplicate {
			return fmt.Errorf("metric %q repeats event selection %q", metric.KeyPath, selection.Name)
		}
		seenEvents[selection.Name] = struct{}{}
		event, ok := events[selection.Name]
		if !ok {
			return fmt.Errorf("metric %q references unknown event %q", metric.KeyPath, selection.Name)
		}
		if selection.Unique != "" {
			if _, ok := allowedIdentifiers[selection.Unique]; !ok {
				return fmt.Errorf("metric %q has unsupported unique identifier %q", metric.KeyPath, selection.Unique)
			}
			if !contains(event.Identifiers, selection.Unique) {
				return fmt.Errorf("metric %q unique identifier %q is not declared by event %q", metric.KeyPath, selection.Unique, selection.Name)
			}
		}
		if metric.MetricKind == "funnel" {
			if selection.Unique == "" {
				return fmt.Errorf("funnel metric %q requires unique on every event selection", metric.KeyPath)
			}
			if funnelUnique == "" {
				funnelUnique = selection.Unique
			} else if funnelUnique != selection.Unique {
				return fmt.Errorf("funnel metric %q must use one unique identifier", metric.KeyPath)
			}
		}
		for property, value := range selection.Filter {
			definition, ok := event.AdditionalProperties[property]
			if !ok || definition.Type != "string" {
				return fmt.Errorf("metric %q filters undeclared/non-string property %q", metric.KeyPath, property)
			}
			if !contains(definition.Enum, value) {
				return fmt.Errorf("metric %q filter %s=%q is outside event enum", metric.KeyPath, property, value)
			}
		}
	}
	if metric.MetricKind == "funnel" && len(metric.Events) < 2 {
		return fmt.Errorf("funnel metric %q requires at least two event selections", metric.KeyPath)
	}
	for name, label := range metric.Labels {
		if forbiddenPropertyName(name) || len(label.Values) == 0 || strings.TrimSpace(label.Description) == "" {
			return fmt.Errorf("metric %q label %q requires description and values", metric.KeyPath, name)
		}
		if err := validateLabelValues(metric.KeyPath, name, label.Values); err != nil {
			return err
		}
		for _, selection := range metric.Events {
			property, ok := events[selection.Name].AdditionalProperties[name]
			if !ok || property.Type != "string" {
				return fmt.Errorf("metric %q label %q is not an event property", metric.KeyPath, name)
			}
			for _, value := range label.Values {
				if !contains(property.Enum, value) {
					return fmt.Errorf("metric %q label %s=%q is outside event enum", metric.KeyPath, name, value)
				}
			}
		}
	}
	return nil
}

func validateLabelValues(metric, label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !enumValuePattern.MatchString(value) || len(value) > MaxPropertyValueSize {
			return fmt.Errorf("metric %q label %q has invalid value %q", metric, label, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("metric %q label %q repeats value %q", metric, label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateURL(value, field, owner string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%s for %q must be an https URL", field, owner)
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
