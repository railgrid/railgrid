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
	"time"

	"github.com/faroshq/faros/telemetry/catalog"
	"github.com/faroshq/faros/telemetry/generated"
)

type catalogRetention struct {
	Action    string
	Retention time.Duration
}

func effectiveEventRetention(action string, configured time.Duration) time.Duration {
	configured = boundedRawRetention(configured)
	definition, ok := generated.LookupEvent(action)
	if !ok {
		return configured
	}
	declared := time.Duration(definition.RetentionDays) * 24 * time.Hour
	if declared < configured {
		return declared
	}
	return configured
}

func boundedRawRetention(configured time.Duration) time.Duration {
	maximum := time.Duration(catalog.MaxRawRetentionDays) * 24 * time.Hour
	if configured > maximum {
		return maximum
	}
	return configured
}

func catalogRetentions(configured time.Duration) []catalogRetention {
	definitions := generated.MustRegistry().Events
	result := make([]catalogRetention, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, catalogRetention{Action: definition.Action, Retention: effectiveEventRetention(definition.Action, configured)})
	}
	return result
}
