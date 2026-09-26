/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

// The verb table: every route this provider serves, and the only place a new
// one can be added. It is checked against manifest.yaml's spec.export.resources[].verbs
// by TestDataPlaneVerbsMatchManifest, so a verb that is served but not
// declared (or declared but not served) fails the build rather than becoming a
// coordinate the hub's scoped-identity service cannot verify.

import (
	"net/http"
)

func get(h func(*Server) http.HandlerFunc) map[string]func(*Server) http.HandlerFunc {
	return map[string]func(*Server) http.HandlerFunc{http.MethodGet: h}
}

func post(h func(*Server) http.HandlerFunc) map[string]func(*Server) http.HandlerFunc {
	return map[string]func(*Server) http.HandlerFunc{http.MethodPost: h}
}

// projectVerbs are the verbs on a Project. The caller needs `create` on
// projects/{verb} for the named project.
var projectVerbs = []verbRoute{
	// The joined project view: the CR, plus live Instance status, the code
	// commit ledger and the pod-local source-revision fence. It is a verb and
	// not a CR read because the join is exactly what no single CR carries —
	// see the Cut B note in docs/roadmap/provider-contract-remediation.md.
	{verb: "view", handlers: get(func(s *Server) http.HandlerFunc { return s.getProject })},
	// There is no `delete` verb. Deleting a project is a DELETE of the
	// Project CR through the hub's kcp proxy, authorized by the tenant's own
	// RBAC on the object, and everything it used to orchestrate here —
	// instances, the repository claim, the conversation rows, the identity,
	// the workspace tree — runs behind the Project finalizer instead
	// (controller/project/teardown.go). `kubectl delete project` and the
	// portal's delete button are now the same operation.
	{verb: "set-repository", handlers: post(func(s *Server) http.HandlerFunc { return s.putProjectRepository })},
	{verb: "set-template", handlers: post(func(s *Server) http.HandlerFunc { return s.putProjectTemplate })},
	{verb: "thumbnail", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectThumbnail })},

	// Build → launch.
	{verb: "promote", handlers: post(func(s *Server) http.HandlerFunc { return s.promoteProjectHandler })},
	{verb: "promotion", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectPromotion })},
	{verb: "releases", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectReleases })},
	{verb: "checkpoints", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectCheckpoints })},

	// Development sandbox.
	{verb: "hydrate-workspace", handlers: post(func(s *Server) http.HandlerFunc { return s.hydrateProjectWorkspace })},
	{verb: "restore-workspace", handlers: post(func(s *Server) http.HandlerFunc { return s.restoreProjectWorkspace })},
	{verb: "scaffold", handlers: post(func(s *Server) http.HandlerFunc { return s.reseedProjectScaffold })},
	{verb: "sync-development", handlers: post(func(s *Server) http.HandlerFunc { return s.syncProjectDevelopment })},
	{verb: "restart-development", handlers: post(func(s *Server) http.HandlerFunc { return s.restartProjectDevelopment })},
	{verb: "development-logs", handlers: get(func(s *Server) http.HandlerFunc { return s.logsProjectDevelopment })},
	{verb: "development-status", handlers: get(func(s *Server) http.HandlerFunc { return s.statusProjectDevelopment })},
	{verb: "authorize-development-preview", handlers: post(func(s *Server) http.HandlerFunc { return s.authorizeProjectDevelopmentPreview })},

	// Files. The tail carries the path, so one grant covers the tree rather
	// than needing one per file.
	{verb: "files", handlers: get(func(s *Server) http.HandlerFunc { return s.listProjectFiles })},
	{verb: "files-content", handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:    func(s *Server) http.HandlerFunc { return s.readProjectFile },
		http.MethodPut:    func(s *Server) http.HandlerFunc { return s.writeProjectFile },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.deleteProjectFile },
	}},
	{verb: "files-raw", handlers: get(func(s *Server) http.HandlerFunc { return s.readProjectFileRaw })},
	{verb: "files-upload", handlers: post(func(s *Server) http.HandlerFunc { return s.uploadProjectFiles })},

	// Preview and publishing.
	{verb: "preview", handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:    func(s *Server) http.HandlerFunc { return s.getProjectPreviewAccess },
		http.MethodPost:   func(s *Server) http.HandlerFunc { return s.setProjectPreviewAccess },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.resetProjectPreviewAccess },
	}},
	{verb: "preview-grants", tailVars: []string{"grant"}, handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:  func(s *Server) http.HandlerFunc { return s.listProjectPreviewGrants },
		http.MethodPost: func(s *Server) http.HandlerFunc { return s.projectPreviewGrantWrite },
	}},
	{verb: "publishing", handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:    func(s *Server) http.HandlerFunc { return s.getProjectPublishing },
		http.MethodPost:   func(s *Server) http.HandlerFunc { return s.publishProject },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.unpublishProject },
	}},
	{verb: "publishing-members", handlers: get(func(s *Server) http.HandlerFunc { return s.listProjectPublishingMembers })},
	{verb: "publishing-grants", tailVars: []string{"grant"}, handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:  func(s *Server) http.HandlerFunc { return s.listProjectPublishingGrants },
		http.MethodPost: func(s *Server) http.HandlerFunc { return s.projectPublishingGrantWrite },
	}},
	{verb: "preview-bridge-sessions", tailVars: []string{"session"}, handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodPost:   func(s *Server) http.HandlerFunc { return s.createProjectPreviewBridgeSession },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.deleteProjectPreviewBridgeSession },
	}},

	// Integrations. `integrations` is the binding list/edit; invoking a bound
	// provider's action is a SEPARATE verb, so a project may be allowed to
	// manage its integrations without being allowed to call through them.
	{verb: "integrations", tailVars: []string{"integration"}, handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:    func(s *Server) http.HandlerFunc { return s.listProjectIntegrations },
		http.MethodPost:   func(s *Server) http.HandlerFunc { return s.addProjectIntegration },
		http.MethodPatch:  func(s *Server) http.HandlerFunc { return s.patchProjectIntegration },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.removeProjectIntegration },
	}},
	{verb: "integration-actions", tailVars: []string{"integration", "action"}, handlers: post(func(s *Server) http.HandlerFunc { return s.invokeProjectIntegration })},

	// Assistant surface that is about the project rather than one thread.
	{verb: "approval-mode", handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:   func(s *Server) http.HandlerFunc { return s.getProjectAssistantApprovalMode },
		http.MethodPatch: func(s *Server) http.HandlerFunc { return s.patchProjectAssistantApprovalMode },
	}},
	{verb: "attachments", tailVars: []string{"attachment"}, handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:    func(s *Server) http.HandlerFunc { return s.projectAssistantAttachmentRead },
		http.MethodPost:   func(s *Server) http.HandlerFunc { return s.createProjectAssistantAttachment },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.deleteProjectAssistantAttachment },
	}},
	{verb: "skills", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectAssistantSkills })},
	{verb: "skill-detail", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectAssistantSkillDetailByID })},
	{verb: "skills-create", handlers: post(func(s *Server) http.HandlerFunc { return s.createProjectAssistantSkill })},
	{verb: "skills-import", handlers: post(func(s *Server) http.HandlerFunc { return s.importProjectAssistantSkill })},
	{verb: "skills-activation", handlers: post(func(s *Server) http.HandlerFunc { return s.setProjectAssistantSkillActivation })},
	// A skill package name legitimately contains slashes, so it is the greedy
	// tail of its own verb rather than part of the verb.
	{verb: "skill-export", tailVars: []string{"packageName"}, greedyTail: true, handlers: get(func(s *Server) http.HandlerFunc { return s.exportProjectAssistantSkill })},
	{verb: "skill", tailVars: []string{"packageName"}, greedyTail: true, handlers: map[string]func(*Server) http.HandlerFunc{
		http.MethodGet:    func(s *Server) http.HandlerFunc { return s.getProjectAssistantSkillDetail },
		http.MethodPut:    func(s *Server) http.HandlerFunc { return s.updateProjectAssistantSkill },
		http.MethodDelete: func(s *Server) http.HandlerFunc { return s.deleteProjectAssistantSkill },
	}},

	// Conversations. Listing them and starting one are project verbs, because
	// neither addresses a Session that exists yet; everything that acts on one
	// hangs off the Session (sessionVerbs).
	{verb: "sessions", handlers: get(func(s *Server) http.HandlerFunc { return s.listProjectAssistantThreads })},
	{verb: "create-session", handlers: post(func(s *Server) http.HandlerFunc { return s.createProjectAssistantThread })},
	// Repair: a thread whose Session projection is missing cannot be addressed
	// as sessions/{thread} at all, because gate 1 has nothing to read. Only a
	// caller who can already see the Project knows which project the thread
	// belongs to, so adopting it is a verb on the PROJECT — authorized against
	// the object that actually confers the right — and the Session is created
	// as that caller. The portal calls it once, automatically, when a session
	// verb answers 404.
	{verb: "adopt-session", tailVars: []string{"thread"}, handlers: post(func(s *Server) http.HandlerFunc { return s.adoptProjectAssistantSession })},
}

// sessionVerbs are the verbs on a Session — one assistant conversation. The
// Session's name is the thread ID, and the project it belongs to is read off
// the gated object rather than the URL.
var sessionVerbs = []verbRoute{
	// "edit" and "discard", not "update" and "delete": the standard Kubernetes
	// verbs are reserved and the hub refuses a CatalogEntry that declares one
	// as a data-plane verb (apis/providers/v1alpha1/dataplane.go).
	{verb: "edit", handlers: post(func(s *Server) http.HandlerFunc { return s.patchProjectAssistantThread })},
	{verb: "discard", handlers: post(func(s *Server) http.HandlerFunc { return s.deleteProjectAssistantThread })},
	{verb: "items", handlers: get(func(s *Server) http.HandlerFunc { return s.listProjectAssistantThreadItems })},
	{verb: "events", handlers: get(func(s *Server) http.HandlerFunc { return s.streamProjectAssistantThreadEvents })},
	{verb: "turn", handlers: post(func(s *Server) http.HandlerFunc { return s.startProjectAssistantThreadTurn })},
	{verb: "review", handlers: post(func(s *Server) http.HandlerFunc { return s.startProjectAssistantThreadReview })},
	{verb: "active-turn", handlers: get(func(s *Server) http.HandlerFunc { return s.activeProjectAssistantThreadTurn })},
	{verb: "turn-status", tailVars: []string{"turn"}, handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectAssistantThreadTurn })},
	{verb: "steer", tailVars: []string{"turn"}, handlers: post(func(s *Server) http.HandlerFunc { return s.steerProjectAssistantThreadTurn })},
	{verb: "interrupt", tailVars: []string{"turn"}, handlers: post(func(s *Server) http.HandlerFunc { return s.interruptProjectAssistantThreadTurn })},
	{verb: "continue", tailVars: []string{"turn"}, handlers: post(func(s *Server) http.HandlerFunc { return s.continueProjectAssistantThreadTurn })},
	// Approval and free-text input are two different decisions a user makes
	// about a paused turn, and they were two routes onto one handler. They
	// stay two verbs so a caller can be granted one without the other.
	{verb: "approval", tailVars: []string{"turn"}, handlers: post(func(s *Server) http.HandlerFunc { return s.respondProjectAssistantThreadTurn })},
	{verb: "input", tailVars: []string{"turn"}, handlers: post(func(s *Server) http.HandlerFunc { return s.respondProjectAssistantThreadTurn })},
}

// studioVerbs are the workspace-wide verbs. The Studio is the per-workspace
// singleton, so it is the object that makes "create a project in this
// workspace" addressable and therefore authorizable — the old collection
// routes (/api/projects, /api/projects/plan, …) had no object at all.
var studioVerbs = []verbRoute{
	{verb: "create-project", handlers: post(func(s *Server) http.HandlerFunc { return s.createProject })},
	{verb: "create-project-stream", handlers: post(func(s *Server) http.HandlerFunc { return s.createProjectStream })},
	{verb: "create-readiness", handlers: get(func(s *Server) http.HandlerFunc { return s.getProjectCreateReadiness })},
	{verb: "plan", handlers: post(func(s *Server) http.HandlerFunc { return s.planProject })},
	{verb: "development-templates", handlers: get(func(s *Server) http.HandlerFunc { return s.listDevelopmentTemplates })},
	{verb: "import-repositories", handlers: get(func(s *Server) http.HandlerFunc { return s.listImportRepositories })},
	{verb: "discover-models", handlers: post(func(s *Server) http.HandlerFunc { return s.discoverProjectLLMModels })},
	{verb: "test-model", handlers: post(func(s *Server) http.HandlerFunc { return s.testProjectLLMConnection })},
}

// verbIndex is the (resource, verb) lookup the dispatcher uses.
var verbIndex = buildVerbIndex()

func buildVerbIndex() map[string]map[string]verbRoute {
	index := map[string]map[string]verbRoute{
		projectsGVR.Resource: {},
		sessionsGVR.Resource: {},
		studiosGVR.Resource:  {},
	}
	for _, route := range projectVerbs {
		index[projectsGVR.Resource][route.verb] = route
	}
	for _, route := range sessionVerbs {
		index[sessionsGVR.Resource][route.verb] = route
	}
	for _, route := range studioVerbs {
		index[studiosGVR.Resource][route.verb] = route
	}
	return index
}

func lookupVerb(resource, verb string) (verbRoute, bool) {
	byVerb, ok := verbIndex[resource]
	if !ok {
		return verbRoute{}, false
	}
	route, ok := byVerb[verb]
	return route, ok
}
