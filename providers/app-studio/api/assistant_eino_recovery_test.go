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

package api

import "testing"

func TestProjectEinoAssistantModelMaxRetriesEnforcesExecutionCap(t *testing.T) {
	tests := []struct {
		name        string
		settings    projectLLMSettings
		wantRetries int
	}{
		{
			name: "default",
			settings: projectLLMSettings{
				MaxRetriesConfigured: false,
			},
			wantRetries: projectEinoAssistantDefaultModelMaxRetries,
		},
		{
			name: "normal configured range is preserved",
			settings: projectLLMSettings{
				MaxRetries:           10,
				MaxRetriesConfigured: true,
			},
			wantRetries: 10,
		},
		{
			name: "execution cap",
			settings: projectLLMSettings{
				MaxRetries:           projectEinoAssistantMaxModelRetries + 1,
				MaxRetriesConfigured: true,
			},
			wantRetries: projectEinoAssistantMaxModelRetries,
		},
		{
			name: "negative values disable retries",
			settings: projectLLMSettings{
				MaxRetries:           -1,
				MaxRetriesConfigured: true,
			},
			wantRetries: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := projectEinoAssistantModelMaxRetries(test.settings); got != test.wantRetries {
				t.Fatalf("max retries = %d, want %d", got, test.wantRetries)
			}
		})
	}
}
