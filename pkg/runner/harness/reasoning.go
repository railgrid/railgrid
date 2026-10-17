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

package harness

import (
	"fmt"
	"regexp"
)

var effortPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidateReasoningEffort validates the transport value. Model-specific support
// is checked against the installed Codex app-server catalog before a turn starts.
func ValidateReasoningEffort(name, effort string) error {
	if effort == "" {
		return nil
	}
	if name != "codex" {
		return fmt.Errorf("reasoningEffort is supported only by Codex; harness %q does not support it", name)
	}
	if !effortPattern.MatchString(effort) {
		return fmt.Errorf("reasoningEffort must be a lowercase Codex effort name of at most 32 characters")
	}
	return nil
}
