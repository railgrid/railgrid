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
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// primaryChannelName is the channel role a template's channel slot creates,
// and the only channelRef a template schedule may name.
const primaryChannelName = "primary"

// InputError is a problem with the caller's inputs — a missing parameter, an
// unknown slot — as opposed to a malformed template. Callers map it to a 400.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func inputErr(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// Instantiate renders a template into an Agent and its Schedules. It is pure:
// it reads nothing from the workspace, so whether a wired name exists (or a
// connection has the slot's type) is the caller's check. The portal's
// agent-template.ts is the same function in TypeScript; the fixtures under
// portal/src/test/fixtures/agent-templates hold both to the same output.
func Instantiate(t Template, in Inputs) (*Result, error) {
	if err := Validate(t); err != nil {
		return nil, fmt.Errorf("template %q is invalid: %w", t.Metadata.Name, err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, inputErr("name is required")
	}
	if !dnsLabelRE.MatchString(name) {
		return nil, inputErr("name %q must be lowercase letters, digits and dashes", name)
	}

	var errs []error
	values := map[string]string{}
	for _, p := range t.Spec.Parameters {
		raw := strings.TrimSpace(in.Parameters[p.Key])
		if raw == "" {
			raw = p.Default
		}
		if p.Type == ParameterList {
			if len(ListItems(raw)) == 0 {
				raw = ""
			}
		}
		if raw == "" && p.Required {
			errs = append(errs, inputErr("parameter %q (%s) is required", p.Key, p.Label))
			continue
		}
		if p.Type == ParameterList {
			values[p.Key] = renderList(raw)
		} else {
			values[p.Key] = raw
		}
	}
	for key := range in.Parameters {
		if !hasParameter(t, key) {
			errs = append(errs, inputErr("parameter %q is not declared by template %q", key, t.Metadata.Name))
		}
	}

	wired := map[string]string{}
	for _, w := range t.Spec.Wiring {
		v := strings.TrimSpace(in.Wiring[w.Key])
		if v == "" {
			if w.Required {
				errs = append(errs, inputErr("wiring %q (%s) is required", w.Key, w.Slot))
			}
			continue
		}
		wired[w.Key] = v
	}
	for key := range in.Wiring {
		if !hasSlot(t, key) {
			errs = append(errs, inputErr("wiring %q is not declared by template %q", key, t.Metadata.Name))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Deep-copy the spec so rendering never writes back into the catalog.
	spec := *t.Spec.Agent.DeepCopy()
	spec.DisplayName = strings.TrimSpace(in.DisplayName)
	if spec.DisplayName == "" {
		spec.DisplayName = t.Spec.DisplayName
	}
	if strings.TrimSpace(spec.Description) == "" {
		spec.Description = t.Spec.Description
	}
	rendered := Template{Spec: Spec{Agent: spec, Schedules: deepCopySchedules(t.Spec.Schedules)}}
	for _, field := range textFields(&rendered) {
		*field = render(*field, values)
	}
	spec = rendered.Spec.Agent

	for _, w := range t.Spec.Wiring {
		v, ok := wired[w.Key]
		if !ok {
			continue
		}
		switch w.Slot {
		case SlotModelCredential:
			spec.SetModelCredential(agentsv1alpha1.PurposeChat, v)
		case SlotChannel:
			spec.Channels = []agentsv1alpha1.AgentChannel{{Name: primaryChannelName, ConnectionRef: v, Primary: true}}
		case SlotConnection:
			spec.Tools.Interactive.Connections = appendUnique(spec.Tools.Interactive.Connections, v)
			spec.Tools.Background.Connections = appendUnique(spec.Tools.Background.Connections, v)
		case SlotEdge:
			kind, edgeName, ok := strings.Cut(v, "/")
			if !ok || kind == "" || edgeName == "" {
				return nil, inputErr("wiring %q must be \"<Kind>/<name>\" (LinuxServer/dev-box), got %q", w.Key, v)
			}
			if kind != "LinuxServer" && kind != "MacOSServer" {
				return nil, inputErr("wiring %q names edge kind %q; a harness runs on a LinuxServer or MacOSServer", w.Key, kind)
			}
			harnessBlock(&spec).EdgeRef = agentsv1alpha1.AgentHarnessEdgeRef{Kind: kind, Name: edgeName}
		case SlotHarnessCredential:
			harnessBlock(&spec).CredentialRef = v
		case SlotGitHubConnection:
			harnessBlock(&spec).GitHubConnectionRef = v
		}
	}
	// Spell the backend type out even where the template left it defaulted, so
	// the created object reads the same whichever front door wrote it.
	if spec.BackendType() == agentsv1alpha1.AgentBackendHarness {
		spec.Backend.Type = agentsv1alpha1.AgentBackendHarness
		h := harnessBlock(&spec)
		if h.Workspace == "" {
			h.Workspace = agentsv1alpha1.HarnessWorkspacePersistent
		}
	} else {
		spec.Backend.Type = agentsv1alpha1.AgentBackendModel
	}

	agent := &agentsv1alpha1.Agent{
		TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "Agent"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
	out := &Result{Agent: agent}
	for _, s := range rendered.Spec.Schedules {
		sched := &agentsv1alpha1.Schedule{
			TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "Schedule"},
			ObjectMeta: metav1.ObjectMeta{Name: name + "-" + s.Name},
			Spec:       s.Spec,
		}
		sched.Spec.AgentRef = name
		sched.Spec.Schedule = strings.TrimSpace(sched.Spec.Schedule)
		out.Schedules = append(out.Schedules, sched)
	}
	return out, nil
}

func harnessBlock(spec *agentsv1alpha1.AgentSpec) *agentsv1alpha1.AgentHarnessBackend {
	if spec.Backend.Harness == nil {
		spec.Backend.Harness = &agentsv1alpha1.AgentHarnessBackend{}
	}
	return spec.Backend.Harness
}

func appendUnique(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

func hasParameter(t Template, key string) bool {
	for _, p := range t.Spec.Parameters {
		if p.Key == key {
			return true
		}
	}
	return false
}

func hasSlot(t Template, key string) bool {
	for _, w := range t.Spec.Wiring {
		if w.Key == key {
			return true
		}
	}
	return false
}

func deepCopySchedules(in []TemplateSchedule) []TemplateSchedule {
	out := make([]TemplateSchedule, len(in))
	for i, s := range in {
		out[i] = TemplateSchedule{Name: s.Name, Spec: *s.Spec.DeepCopy()}
	}
	return out
}
