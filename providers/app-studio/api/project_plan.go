/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"net/http"
	"strings"
)

// The creation wizard's blueprint step. POST /api/projects/plan takes the
// user's intake prompt and returns what a project would be — a proposed name,
// the recommended development template, and whether that template attaches a
// scaffold — WITHOUT creating anything. The portal shows this as a confirm
// step ("we'll build <name> on <template>, starting from its starter code").
// Creation then
// posts to /api/projects with the confirmed template.

// ProjectPlanRequest is the wizard intake.
type ProjectPlanRequest struct {
	// Prompt is the free-text description of what to build.
	Prompt string `json:"prompt"`
	// TemplateName, when set, pins the template instead of inferring it
	// (the user picked one in the wizard).
	TemplateName string `json:"templateName,omitempty"`
}

// ProjectPlanScaffold describes the starter code a plan would attach.
type ProjectPlanScaffold struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
}

// ProjectPlan is the blueprint returned to the wizard.
type ProjectPlan struct {
	// DisplayName / RepositoryName are the proposed names (editable in the UI).
	DisplayName    string `json:"displayName"`
	RepositoryName string `json:"repositoryName"`
	// Template is the recommended development template ("" when none fits or
	// the catalog is empty — the project then starts unbound).
	Template string `json:"template,omitempty"`
	// Components maps the template's development component names to their
	// workspace directories, for the wizard to preview the project shape.
	Components map[string]string `json:"components,omitempty"`
	// Scaffold is the starter code that will be attached (nil when the
	// template ships none).
	Scaffold *ProjectPlanScaffold `json:"scaffold,omitempty"`
	// AvailableTemplates lets the wizard offer alternatives to the recommended
	// one without a second round-trip.
	AvailableTemplates []projectDevelopmentTemplateView `json:"availableTemplates"`
}

// planProject is POST /api/projects/plan.
func (s *Server) planProject(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var req ProjectPlanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.TemplateName = strings.TrimSpace(req.TemplateName)
	if req.Prompt == "" && req.TemplateName == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "prompt or templateName is required to plan a project")
		return
	}

	templates, err := listDevelopmentTemplateViews(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}

	plan := ProjectPlan{AvailableTemplates: templates}

	// Naming + template recommendation. An explicit template pin skips
	// inference; a prompt runs the same preflight the create path uses so the
	// wizard's proposal and the eventual creation agree.
	templateName := req.TemplateName
	if req.Prompt != "" {
		generate := s.generateProjectCreatePreflight
		if s.projectCreatePreflight != nil {
			generate = s.projectCreatePreflight
		}
		preflight, err := generate(r.Context(), c, req.Prompt, templates)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		plan.DisplayName = preflight.Naming.DisplayName
		plan.RepositoryName = preflight.Naming.RepositoryName
		if templateName == "" {
			templateName = strings.TrimSpace(preflight.TemplateName)
		}
	}

	if templateName != "" {
		if info, err := resolveProjectCreateTemplate(r.Context(), c, templateName, req.Prompt != ""); err == nil && info != nil {
			plan.Template = info.Name
			plan.Components = info.WorkspacePaths()
			if info.ScaffoldRepo != "" {
				plan.Scaffold = &ProjectPlanScaffold{Repository: info.ScaffoldRepo, Ref: info.ScaffoldRef}
			}
		}
	}
	if plan.DisplayName == "" {
		plan.DisplayName = defaultPlanDisplayName(plan.Template, req.Prompt)
	}
	if plan.RepositoryName == "" {
		plan.RepositoryName = slugifyProjectName(plan.DisplayName)
	}

	writeJSON(w, http.StatusOK, plan)
}

func defaultPlanDisplayName(template, prompt string) string {
	if p := strings.TrimSpace(prompt); p != "" {
		if len(p) > 60 {
			p = p[:60]
		}
		return p
	}
	if template != "" {
		return template + " project"
	}
	return "New project"
}
