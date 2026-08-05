// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package session

import (
	"fmt"
	"strings"
)

// KickoffInput is the opening instruction for the first studio turn, written
// from the blueprint the user approved.
//
// It exists because the wizard is the whole product: by the time provisioning
// finishes, the user has described the app, answered the questions, and
// pressed "Create app". Asking them to then type "build it" makes them state
// their intent a second time, and until they do, the preview shows the
// scaffold's hello-world rather than their app.
//
// The success criteria carry the actual contract — the studio system prompt
// only names the title, summary, and template — so they are repeated here as
// the checklist for the first pass.
func KickoffInput(bp *Blueprint) string {
	if bp == nil {
		return ""
	}
	title := strings.TrimSpace(bp.Title)
	summary := strings.TrimSpace(bp.Summary)
	if title == "" && summary == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("Build the first working version of this app now, from the plan we agreed:\n\n")
	if title != "" {
		fmt.Fprintf(&b, "%s\n", title)
	}
	if summary != "" {
		fmt.Fprintf(&b, "%s\n", summary)
	}
	if len(bp.SuccessCriteria) > 0 {
		b.WriteString("\nIt is done when:\n")
		for _, c := range bp.SuccessCriteria {
			if c = strings.TrimSpace(c); c != "" {
				fmt.Fprintf(&b, "- %s\n", c)
			}
		}
	}
	if len(bp.Assumptions) > 0 {
		b.WriteString("\nAssumptions we made:\n")
		for _, a := range bp.Assumptions {
			if a = strings.TrimSpace(a); a != "" {
				fmt.Fprintf(&b, "- %s\n", a)
			}
		}
	}
	// The sandbox is already running the scaffold: editing it in place keeps
	// the dev server, its dependencies, and the platform contract in AGENTS.md
	// intact, where re-bootstrapping throws all three away.
	b.WriteString("\nEdit the scaffold that is already in the workspace rather than starting a new project, " +
		"and follow AGENTS.md. Replace its placeholder content with the real thing.")
	return b.String()
}
