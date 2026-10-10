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

package api

import (
	"strings"
	"testing"
)

func TestProjectEinoFilesystemPromptMatchesMutationContract(t *testing.T) {
	const staleInstruction = "Before replacing, editing, deleting, or moving"
	for name, prompt := range map[string]string{
		"read description":   projectEinoFilesystemReadDescription,
		"system instruction": projectEinoFilesystemInstruction,
	} {
		if strings.Contains(prompt, staleInstruction) {
			t.Errorf("%s still requires a read before every mutation", name)
		}
	}

	for name, prompt := range map[string]string{
		"read description":   projectEinoFilesystemReadDescription,
		"system instruction": projectEinoFilesystemInstruction,
	} {
		if !strings.Contains(prompt, "edit_file") || !strings.Contains(prompt, "separate read and expectedVersion are optional") {
			t.Errorf("%s does not explain that edit_file is self-contained", name)
		}
	}
}

func TestProjectAssistantModePromptGuidesKnownArgumentBatchingWithoutWeakeningReadProof(t *testing.T) {
	var builder strings.Builder
	appendProjectAssistantV2ModePrompt(&builder, projectAssistantCollaborationModeDefault, "", false, false)
	prompt := builder.String()
	for _, want := range []string{
		"When all arguments are already known, group file changes and their dependent reads or checks in one tool-call batch.",
		"The runtime orders calls that conflict on a path; independent reads may run together.",
		"A read receipt returned in that same batch does not authorize replace_file, delete_file, or move_file",
		"those still require the complete read in an earlier model response",
		"Wait for a receipt if you need its content or version to form a later call.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("default mode prompt missing batching/proof guidance %q", want)
		}
	}
	if strings.Contains(prompt, "all tool calls run in listed order") || strings.Contains(prompt, "every tool call runs in listed order") {
		t.Fatal("default mode prompt overstates batch ordering for independent tools")
	}
}
