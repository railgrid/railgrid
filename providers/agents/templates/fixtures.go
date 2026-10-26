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
	"encoding/json"
	"fmt"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// This file is the contract between the two instantiators. internal/gentemplates
// writes CatalogJSON to the portal bundle and one FixtureJSON per template to
// portal/src/test/fixtures/agent-templates/; the Go asset test and the Vitest
// parity test both compare against those files.

// Fixture is one parity case: the inputs and the exact objects Instantiate
// produced from them.
type Fixture struct {
	Template  string                     `json:"template"`
	Inputs    Inputs                     `json:"inputs"`
	Agent     *agentsv1alpha1.Agent      `json:"agent"`
	Schedules []*agentsv1alpha1.Schedule `json:"schedules"`
}

// FixtureInputs builds deterministic sample inputs that fill every parameter
// and every wiring slot of a template, so the fixture exercises each
// substitution and each slot kind.
func FixtureInputs(t Template) Inputs {
	in := Inputs{
		Name:       t.Spec.SuggestedName,
		Parameters: map[string]string{},
		Wiring:     map[string]string{},
	}
	if in.Name == "" {
		in.Name = t.Metadata.Name
	}
	for _, p := range t.Spec.Parameters {
		switch p.Type {
		case ParameterList:
			in.Parameters[p.Key] = "alpha item\nbeta item\n"
		case ParameterText:
			in.Parameters[p.Key] = "Sample " + p.Label + "."
		default:
			if p.Default != "" {
				in.Parameters[p.Key] = p.Default
			} else {
				in.Parameters[p.Key] = "sample-" + p.Key
			}
		}
	}
	for _, w := range t.Spec.Wiring {
		switch w.Slot {
		case SlotModelCredential:
			in.Wiring[w.Key] = "fixture-model"
		case SlotChannel:
			in.Wiring[w.Key] = "fixture-channel"
		case SlotConnection:
			in.Wiring[w.Key] = "fixture-" + w.ConnectionType
		case SlotEdge:
			in.Wiring[w.Key] = "LinuxServer/fixture-edge"
		case SlotHarnessCredential:
			in.Wiring[w.Key] = "fixture-harness"
		case SlotGitHubConnection:
			in.Wiring[w.Key] = "fixture-github"
		}
	}
	return in
}

// CatalogJSON is the portal asset: the built-in catalog, pretty-printed with
// two spaces and a trailing newline.
func CatalogJSON() ([]byte, error) {
	return marshal(Builtin())
}

// FixtureJSON renders one template's parity fixture.
func FixtureJSON(t Template) ([]byte, error) {
	in := FixtureInputs(t)
	res, err := Instantiate(t, in)
	if err != nil {
		return nil, fmt.Errorf("instantiate %s with fixture inputs: %w", t.Metadata.Name, err)
	}
	return marshal(Fixture{Template: t.Metadata.Name, Inputs: in, Agent: res.Agent, Schedules: res.Schedules})
}

func marshal(v any) ([]byte, error) {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
