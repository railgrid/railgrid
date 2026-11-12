// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"fmt"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

// checkDelegationBudget reads the existing execution lineage. A checkpoint
// recovery rebuilds tools but must not reset the execution's delegation cap.
func (s *Server) checkDelegationBudget(ctx context.Context, scope store.Scope, parent string) error {
	if parent == "" {
		return fmt.Errorf("delegation requires its parent Run identity")
	}
	scope.AgentName = "" // delegates run as other Agents in this same workspace
	page, err := s.store.QueryRuns(ctx, scope, store.RunQuery{ParentRunID: parent, Trigger: agentsv1alpha1.RunTriggerDelegation, Limit: store.MaxDelegatedRuns})
	if err != nil {
		return fmt.Errorf("reading durable delegation budget: %w", err)
	}
	if len(page.Items) >= store.MaxDelegatedRuns {
		return store.ErrDelegationLimit
	}
	return nil
}
