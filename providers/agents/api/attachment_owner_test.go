// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

func TestAttachmentOwnerSurvivesPlainFollowupCheckpoint(t *testing.T) {
	const canonical = "system:authenticated:alice"
	owner, refs, skills, resourceOwner, resources := checkpointAgentInputReferences(taskRun{
		Trigger: agentsv1alpha1.RunTriggerChat, AttachmentOwner: canonical,
	})
	if owner != canonical || len(refs) != 0 || len(skills) != 0 || resourceOwner != "" || len(resources) != 0 {
		t.Fatalf("checkpoint input references = owner %q refs %+v skills %+v resourceOwner %q resources %+v", owner, refs, skills, resourceOwner, resources)
	}

	checkpoint := runCheckpoint{AttachmentOwner: owner}
	resumedOwner, resumedRefs, _, _, _, err := resumeAgentInputReferences(checkpoint, "browser-user-uuid", canonical, false)
	if err != nil {
		t.Fatalf("resume with matching canonical caller: %v", err)
	}
	if resumedOwner != canonical || len(resumedRefs) != 0 {
		t.Fatalf("resumed owner=%q refs=%+v", resumedOwner, resumedRefs)
	}
	if _, _, _, _, _, err := resumeAgentInputReferences(checkpoint, "browser-user-uuid", "system:authenticated:bob", false); err == nil {
		t.Fatal("resume accepted a different canonical caller")
	}
}
