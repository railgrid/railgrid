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
	"fmt"
	"net/http"
	"sync"
)

// projectAssistantSkillMetricCounters intentionally has a fixed label set.
// Skill IDs, package paths, tenant IDs, request bodies, and resource paths are
// never metric labels.
type projectAssistantSkillMetricCounters struct {
	mu       sync.Mutex
	counters map[string]uint64
}

var projectAssistantSkillMetrics = &projectAssistantSkillMetricCounters{counters: map[string]uint64{}}

func projectAssistantSkillMetric(kind, outcome string) {
	if projectAssistantSkillMetrics == nil {
		return
	}
	key := kind + "\x00" + outcome
	projectAssistantSkillMetrics.mu.Lock()
	projectAssistantSkillMetrics.counters[key]++
	projectAssistantSkillMetrics.mu.Unlock()
}

func projectAssistantSkillMetricValue(kind, outcome string) uint64 {
	projectAssistantSkillMetrics.mu.Lock()
	defer projectAssistantSkillMetrics.mu.Unlock()
	return projectAssistantSkillMetrics.counters[kind+"\x00"+outcome]
}

func writeProjectAssistantSkillMetrics(w http.ResponseWriter) {
	type metric struct {
		name string
		kind string
	}
	metrics := []metric{
		{name: "app_studio_assistant_skill_catalog_total", kind: "catalog"},
		{name: "app_studio_assistant_skill_lifecycle_total", kind: "lifecycle"},
		{name: "app_studio_assistant_skill_selection_total", kind: "selection"},
		{name: "app_studio_assistant_skill_load_total", kind: "load"},
		{name: "app_studio_assistant_skill_resource_total", kind: "resource"},
		{name: "app_studio_assistant_skill_drift_total", kind: "drift"},
	}
	for _, item := range metrics {
		_, _ = fmt.Fprintf(w, "# TYPE %s counter\n", item.name)
		outcomes := skillMetricOutcomes(item.kind)
		for _, outcome := range outcomes {
			_, _ = fmt.Fprintf(w, "%s{outcome=\"%s\"} %d\n", item.name, outcome, projectAssistantSkillMetricValue(item.kind, outcome))
		}
	}
}

func skillMetricOutcomes(kind string) []string {
	switch kind {
	case "catalog":
		return []string{"success", "failure", "detail", "not_found"}
	case "lifecycle":
		return []string{"create", "import", "update", "activate", "export", "delete", "invalid", "stale", "conflict", "rollback", "forbidden", "not_found"}
	case "selection":
		return []string{"accepted", "rejected"}
	case "load":
		return []string{"success", "rejected", "failure"}
	case "resource":
		return []string{"success", "rejected", "failure"}
	case "drift":
		return []string{"detected", "accepted"}
	default:
		return []string{"success", "failure"}
	}
}
