/*
Copyright 2026 The Railgrid Authors.

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

package templates

import (
	"regexp"
	"strings"
)

// placeholderRE matches {{key}}, with optional inner whitespace. The portal's
// agent-template.ts carries the same expression; the parity fixtures keep the
// two in step.
var placeholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}`)

// placeholders lists the distinct parameter keys referenced in s, in order of
// first appearance.
func placeholders(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// render substitutes every placeholder in s from values. Unknown keys are left
// alone — Validate guarantees there are none in a catalog template, and a
// literal "{{x}}" in a user override is theirs to keep.
func render(s string, values map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		key := placeholderRE.FindStringSubmatch(m)[1]
		if v, ok := values[key]; ok {
			return v
		}
		return m
	})
}

// ListItems splits a list parameter's raw value into items: one per line,
// trimmed, blanks dropped.
func ListItems(raw string) []string {
	var out []string
	for line := range strings.SplitSeq(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// renderList is how a list parameter appears in prose: a markdown bullet
// list, one item per line.
func renderList(raw string) string {
	items := ListItems(raw)
	for i := range items {
		items[i] = "- " + items[i]
	}
	return strings.Join(items, "\n")
}

// textFields enumerates every string in a template that may carry a
// placeholder, with a path for error messages. Validate scans these for
// undeclared keys; instantiate renders them.
func textFields(t *Template) []*string {
	fields := []*string{
		&t.Spec.Agent.Description,
		&t.Spec.Agent.SystemPrompt,
	}
	for i := range t.Spec.Schedules {
		s := &t.Spec.Schedules[i].Spec
		fields = append(fields, &s.Schedule, &s.TimeZone, &s.Task, &s.Checklist)
	}
	return fields
}
