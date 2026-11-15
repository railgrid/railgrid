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

// Package modelcatalogjson renders the shared model catalog for the portal.
// Separate from the command so a test can render into memory and compare,
// which is what keeps the committed JSON honest.
package modelcatalogjson

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/railgrid/provider-sdk/modelcatalog"
)

// portalModel contains the fields consumed by the portal's pricing and
// capability presentation. The full catalog, including pricing provenance and
// display metadata, remains authoritative in provider-sdk/modelcatalog.
type portalModel struct {
	ID            string  `json:"id"`
	ContextWindow int     `json:"contextWindow,omitempty"`
	InputPer1M    float64 `json:"inputPer1M"`
	OutputPer1M   float64 `json:"outputPer1M"`
	Vision        bool    `json:"vision,omitempty"`
	ToolCall      bool    `json:"toolCall,omitempty"`
	Reasoning     bool    `json:"reasoning,omitempty"`
}

// Render returns the portal projection as deterministic, newline-terminated JSON.
// Sort by ID so changes reflect catalog data rather than iteration order.
func Render() ([]byte, error) {
	entries := make([]portalModel, 0, len(modelcatalog.Catalog()))
	for _, item := range modelcatalog.Catalog() {
		entries = append(entries, portalModel{
			ID: item.ID, ContextWindow: item.ContextWindow,
			InputPer1M: item.InputPer1M, OutputPer1M: item.OutputPer1M,
			Vision: item.Vision, ToolCall: item.ToolCall, Reasoning: item.Reasoning,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	// The catalog carries no user input, so escaping HTML in it only makes
	// the committed file harder to read in a diff.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(entries); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderReasoningProfiles returns the supported reasoning catalog as
// deterministic, newline-terminated JSON. Profiles are sorted by canonical
// model ID so the committed artifact changes only when the source table does.
func RenderReasoningProfiles() ([]byte, error) {
	profiles := modelcatalog.ReasoningProfiles()
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(profiles); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
