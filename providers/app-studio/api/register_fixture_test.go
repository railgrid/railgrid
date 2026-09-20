/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

// Register is the RETIRED /api/* route table, kept as a TEST FIXTURE only.
//
// The provider does not serve these paths any more: main.go builds its surface
// with provider-sdk/serve, which has no /api/* route class, and every verb is
// reached through DataPlane() on the grammar instead
// (dataplane_routes.go, dataplane_table.go).
//
// The handler tests below it are not route tests — they drive one handler with
// a request and assert what it does — so re-pointing two hundred URLs at the
// new grammar would have changed what those tests type without changing what
// they check, and would have coupled every one of them to the gate fixtures.
// The routing itself is covered where it belongs: TestDataPlaneConformance
// drives the real DataPlane handler through the contract suite, and
// TestDataPlaneVerbTableServesEveryHandler proves this fixture and the served
// table name the same handlers, so a verb added to one and not the other
// fails here.
//
// It lives in a _test.go file so a non-test build cannot reach it.

import (
	"net/http"

	"github.com/gorilla/mux"
)

func (s *Server) Register(r *mux.Router) {
	r.HandleFunc("/metrics", projectAssistantMetricsHandler).Methods(http.MethodGet)
	r.HandleFunc("/api/projects", s.listProjects).Methods(http.MethodGet)
	r.HandleFunc("/api/projects", s.createProject).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/stream", s.createProjectStream).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/create-readiness", s.getProjectCreateReadiness).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/plan", s.planProject).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/development-templates", s.listDevelopmentTemplates).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/import-repositories", s.listImportRepositories).Methods(http.MethodGet)
	// The registry itself is Studio spec, and each credential is its own
	// Secret; the portal reads and writes both with the kube client, as the
	// caller — who always was the writer, since these handlers only ever
	// acted on the caller's behalf. What stays are the two things a browser
	// cannot do, because both need the key server-side and neither returns
	// anything secret: reach a model provider to test a credential, and ask
	// it what models it serves.
	r.HandleFunc("/api/projects/llm-settings/models/discover", s.discoverProjectLLMModels).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/llm-settings/test", s.testProjectLLMConnection).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}", s.getProject).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/repository", s.putProjectRepository).Methods(http.MethodPut)
	r.HandleFunc("/api/projects/{project}/thumbnail", s.getProjectThumbnail).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/attachments", s.listProjectAssistantAttachments).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/attachments", s.createProjectAssistantAttachment).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/attachments/{attachment}", s.getProjectAssistantAttachment).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/attachments/{attachment}", s.deleteProjectAssistantAttachment).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/threads", s.listProjectAssistantThreads).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills", s.getProjectAssistantSkills).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/detail", s.getProjectAssistantSkillDetailByID).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project", s.createProjectAssistantSkill).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/import", s.importProjectAssistantSkill).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/skills/activation", s.setProjectAssistantSkillActivation).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}/export", s.exportProjectAssistantSkill).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}", s.getProjectAssistantSkillDetail).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}", s.updateProjectAssistantSkill).Methods(http.MethodPut)
	r.HandleFunc("/api/projects/{project}/assistant/skills/project/{packageName:.*}", s.deleteProjectAssistantSkill).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/threads", s.createProjectAssistantThread).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}", s.patchProjectAssistantThread).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}", s.deleteProjectAssistantThread).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/items", s.listProjectAssistantThreadItems).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/events", s.streamProjectAssistantThreadEvents).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns", s.startProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/reviews", s.startProjectAssistantThreadReview).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/active", s.activeProjectAssistantThreadTurn).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}", s.getProjectAssistantThreadTurn).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/steer", s.steerProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/interrupt", s.interruptProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/continue", s.continueProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/approval", s.respondProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/assistant/threads/{thread}/turns/{turn}/input", s.respondProjectAssistantThreadTurn).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/template", s.putProjectTemplate).Methods(http.MethodPut)
	r.HandleFunc("/api/projects/{project}/integrations", s.listProjectIntegrations).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/integrations", s.addProjectIntegration).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/integrations/{integration}", s.patchProjectIntegration).Methods(http.MethodPatch)
	r.HandleFunc("/api/projects/{project}/integrations/{integration}", s.removeProjectIntegration).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/integrations/{integration}/invoke", s.invokeProjectIntegration).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/integrations/{integration}/invoke/{action}", s.invokeProjectIntegration).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/integrations/{integration}/actions", s.invokeProjectIntegration).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/integrations/{integration}/actions/{action}", s.invokeProjectIntegration).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/promotion", s.getProjectPromotion).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/releases", s.getProjectReleases).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/checkpoints", s.getProjectCheckpoints).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/promote", s.promoteProjectHandler).Methods(http.MethodPost)
	// Preview visibility is the development-side counterpart of publishing and
	// lives in the same project settings surface.
	r.HandleFunc("/api/projects/{project}/preview", s.getProjectPreviewAccess).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/preview", s.setProjectPreviewAccess).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview", s.resetProjectPreviewAccess).Methods(http.MethodDelete)
	// Preview grants mirror the publishing ones exactly — same shapes, same
	// member/invite semantics — because both delegate to the shared handlers.
	r.HandleFunc("/api/projects/{project}/preview/grants", s.listProjectPreviewGrants).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/preview/grants", s.createProjectPreviewGrant).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview/grants/{grant}", s.revokeProjectPreviewGrant).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/publishing", s.getProjectPublishing).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/publishing", s.publishProject).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/publishing", s.unpublishProject).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/publishing/members", s.listProjectPublishingMembers).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/publishing/grants", s.listProjectPublishingGrants).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/publishing/grants", s.createProjectPublishingGrant).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/publishing/grants/{grant}", s.revokeProjectPublishingGrant).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/hydrate-workspace", s.hydrateProjectWorkspace).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/restore-workspace", s.restoreProjectWorkspace).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/scaffold", s.reseedProjectScaffold).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/sync-development", s.syncProjectDevelopment).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/restart-development", s.restartProjectDevelopment).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/development-logs", s.logsProjectDevelopment).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/development-status", s.statusProjectDevelopment).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/files", s.listProjectFiles).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/files/content", s.readProjectFile).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/files/content", s.writeProjectFile).Methods(http.MethodPut)
	r.HandleFunc("/api/projects/{project}/files/content", s.deleteProjectFile).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/files/raw", s.readProjectFileRaw).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/files/upload", s.uploadProjectFiles).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/authorize-development-preview", s.authorizeProjectDevelopmentPreview).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview-bridge/sessions", s.createProjectPreviewBridgeSession).Methods(http.MethodPost)
	r.HandleFunc("/api/projects/{project}/preview-bridge/sessions/{session}", s.deleteProjectPreviewBridgeSession).Methods(http.MethodDelete)
	r.HandleFunc("/api/projects/{project}/assistant/approval-mode", s.getProjectAssistantApprovalMode).Methods(http.MethodGet)
	r.HandleFunc("/api/projects/{project}/assistant/approval-mode", s.patchProjectAssistantApprovalMode).Methods(http.MethodPatch)
}
