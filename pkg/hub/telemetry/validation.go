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

package telemetry

import (
	"fmt"
	"math"
	"strings"

	"github.com/faroshq/faros/telemetry/catalog"
	"github.com/faroshq/faros/telemetry/generated"
)

var identifierValue = map[string]func(Event) string{
	"org": func(e Event) string { return e.OrgID }, "workspace": func(e Event) string { return e.WorkspaceID },
	"scope":   func(e Event) string { return e.ScopeID },
	"project": func(e Event) string { return e.ProjectID }, "resource": func(e Event) string { return e.ResourceID },
	"actor": func(e Event) string { return e.Actor }, "run": func(e Event) string { return e.CorrelationID },
}

func validateProviderEvent(provider string, e Event) error {
	e.Action = strings.TrimSpace(e.Action)
	definition, ok := generated.LookupEvent(e.Action)
	if !ok || definition.Status != "active" || definition.Owner != provider {
		return fmt.Errorf("action is not declared for provider: %w", ErrInvalidEvent)
	}
	declaredIdentifiers := make(map[string]struct{}, len(definition.Identifiers))
	for _, name := range definition.Identifiers {
		declaredIdentifiers[name] = struct{}{}
		read, known := identifierValue[name]
		if !known || strings.TrimSpace(read(e)) == "" {
			return fmt.Errorf("identifier %s is required: %w", name, ErrInvalidEvent)
		}
	}
	for name, read := range identifierValue {
		if _, declared := declaredIdentifiers[name]; !declared && strings.TrimSpace(read(e)) != "" {
			return fmt.Errorf("identifier %s is not declared for action: %w", name, ErrInvalidEvent)
		}
	}
	if len(e.Properties) != len(definition.AdditionalProperties) {
		return fmt.Errorf("properties do not match catalog: %w", ErrInvalidEvent)
	}
	for name, value := range e.Properties {
		property, ok := definition.AdditionalProperties[name]
		if !ok || validateProperty(property, value) != nil {
			return fmt.Errorf("property %s violates catalog: %w", name, ErrInvalidEvent)
		}
	}
	return nil
}

func validateProperty(p catalog.PropertyDefinition, value any) error {
	switch p.Type {
	case "string":
		v, ok := value.(string)
		if !ok {
			return ErrInvalidEvent
		}
		for _, allowed := range p.Enum {
			if v == allowed {
				return nil
			}
		}
		return ErrInvalidEvent
	case "boolean":
		if _, ok := value.(bool); !ok {
			return ErrInvalidEvent
		}
		return nil
	case "number", "integer":
		v, ok := numeric(value)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return ErrInvalidEvent
		}
		if p.Type == "integer" && math.Trunc(v) != v {
			return ErrInvalidEvent
		}
		if p.Minimum == nil || p.Maximum == nil || v < *p.Minimum || v > *p.Maximum {
			return ErrInvalidEvent
		}
		return nil
	default:
		return ErrInvalidEvent
	}
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}
