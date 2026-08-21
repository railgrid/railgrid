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

package telemetryreceiver

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/faroshq/faros/telemetry/catalog"
	"github.com/faroshq/faros/telemetry/generated"
)

type ProjectionRule struct {
	MetricKey, MetricKind, EventType, FunnelStep, Unique string
	StepOrder, WindowDays                                int
	Filters                                              map[string]string
	Labels                                               []string
}

type Projection struct {
	BucketStart, MetricKey, MetricKind, FunnelStep, LabelsKey string
	StepOrder, WindowDays                                     int
	Labels                                                    []byte
	UniqueKind, UniqueHash                                    string
}

type ProjectionPlan struct {
	rulesByEvent map[string][]ProjectionRule
	metrics      []ProjectionRule
}

func GeneratedProjectionPlan() (ProjectionPlan, error) {
	return BuildProjectionPlan(generated.MustRegistry().Metrics)
}

func BuildProjectionPlan(metrics []catalog.MetricDefinition) (ProjectionPlan, error) {
	metrics = append([]catalog.MetricDefinition(nil), metrics...)
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].KeyPath < metrics[j].KeyPath })
	plan := ProjectionPlan{rulesByEvent: make(map[string][]ProjectionRule)}
	for _, metric := range metrics {
		if metric.Status != "active" {
			continue
		}
		window, err := metricWindowDays(metric.TimeFrame)
		if err != nil {
			return ProjectionPlan{}, fmt.Errorf("metric %s: %w", metric.KeyPath, err)
		}
		labels := make([]string, 0, len(metric.Labels))
		for name := range metric.Labels {
			labels = append(labels, name)
		}
		sort.Strings(labels)
		for index, selection := range metric.Events {
			step := ""
			if metric.MetricKind == "funnel" {
				step = selection.Name
			}
			rule := ProjectionRule{MetricKey: metric.KeyPath, MetricKind: metric.MetricKind, EventType: selection.Name, FunnelStep: step, StepOrder: index + 1, WindowDays: window, Unique: selection.Unique, Filters: cloneStrings(selection.Filter), Labels: append([]string(nil), labels...)}
			plan.rulesByEvent[selection.Name] = append(plan.rulesByEvent[selection.Name], rule)
			plan.metrics = append(plan.metrics, rule)
		}
	}
	for action := range plan.rulesByEvent {
		sort.Slice(plan.rulesByEvent[action], func(i, j int) bool {
			a, b := plan.rulesByEvent[action][i], plan.rulesByEvent[action][j]
			if a.MetricKey != b.MetricKey {
				return a.MetricKey < b.MetricKey
			}
			return a.StepOrder < b.StepOrder
		})
	}
	return plan, nil
}

func (p ProjectionPlan) Project(event Event) ([]Projection, error) {
	// Aggregate by receiver time, not caller-controlled occurrence time. The
	// latter remains available in the bounded raw row for debugging, but must
	// not let a compromised sender backdate or future-date retained metrics.
	if event.ReceivedAt.IsZero() {
		return nil, fmt.Errorf("projection received_at is required")
	}
	rules := p.rulesByEvent[event.Type]
	result := make([]Projection, 0, len(rules))
	for _, rule := range rules {
		if !matchesFilters(event.Record.Properties, rule.Filters) {
			continue
		}
		labels := make(map[string]string, len(rule.Labels))
		for _, name := range rule.Labels {
			value, ok := event.Record.Properties[name].(string)
			if !ok {
				return nil, fmt.Errorf("projection label %s is not a string", name)
			}
			labels[name] = value
		}
		encoded, err := json.Marshal(labels)
		if err != nil {
			return nil, err
		}
		projection := Projection{BucketStart: event.ReceivedAt.UTC().Format("2006-01-02"), MetricKey: rule.MetricKey, MetricKind: rule.MetricKind, FunnelStep: rule.FunnelStep, StepOrder: rule.StepOrder, WindowDays: rule.WindowDays, Labels: encoded, LabelsKey: string(encoded), UniqueKind: rule.Unique}
		if rule.Unique != "" {
			projection.UniqueHash = event.Record.Identifiers[rule.Unique]
			if !identifierHashPattern.MatchString(projection.UniqueHash) {
				return nil, fmt.Errorf("projection unique identifier %s is invalid", rule.Unique)
			}
		}
		result = append(result, projection)
	}
	return result, nil
}

func (p ProjectionPlan) CatalogRows() []ProjectionRule {
	return append([]ProjectionRule(nil), p.metrics...)
}

func metricWindowDays(frame string) (int, error) {
	switch frame {
	case "all":
		return 0, nil
	case "7d":
		return 7, nil
	case "28d":
		return 28, nil
	default:
		return 0, fmt.Errorf("unsupported time frame %q", frame)
	}
}

func matchesFilters(properties map[string]interface{}, filters map[string]string) bool {
	for name, want := range filters {
		if got, ok := properties[name].(string); !ok || got != want {
			return false
		}
	}
	return true
}

func cloneStrings(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
