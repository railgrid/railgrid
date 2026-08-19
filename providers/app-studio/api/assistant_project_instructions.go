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

import (
	"context"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantAgentsFilename = "AGENTS.md"
	// Match Codex's bounded aggregate project-document budget. The wrapper and
	// scope contract are server-authored and intentionally outside this limit.
	projectAssistantInstructionsMaxBytes = 32 << 10
)

// projectAssistantWorkspaceInstructions loads repository-owned instructions
// from the root of the durable cloned project. Unlike Codex's general local
// workspace discovery, App Studio has one fixed project root and supports one
// root AGENTS.md whose instructions apply to the entire project filesystem.
func projectAssistantWorkspaceInstructions(
	ctx context.Context,
	store *workspace.FileStore,
	scope workspace.Scope,
) (string, bool) {
	if store == nil {
		return "", false
	}
	file, err := store.ReadFile(ctx, scope, workspace.ReadOptions{
		Path:     projectAssistantAgentsFilename,
		MaxBytes: projectAssistantInstructionsMaxBytes,
	})
	if err != nil || file.Binary || strings.TrimSpace(file.Content) == "" {
		return "", false
	}

	var body strings.Builder
	body.WriteString("# AGENTS.md instructions for the App Studio project root\n\n")
	body.WriteString("<INSTRUCTIONS>\n")
	body.WriteString(file.Content)
	if !strings.HasSuffix(file.Content, "\n") {
		body.WriteByte('\n')
	}
	body.WriteString("</INSTRUCTIONS>")
	if file.Truncated {
		body.WriteString("\n\n[This AGENTS.md was truncated by App Studio's 32 KiB project-instruction limit.]")
	}

	return "App Studio automatically loaded AGENTS.md from the cloned project root. Treat it as workspace-scoped instructions for the entire project filesystem. App Studio refreshes this section before every model sample, so do not re-read the file. Direct system and user instructions take precedence.\n\n" + body.String(), true
}
