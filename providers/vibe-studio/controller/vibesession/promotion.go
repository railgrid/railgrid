// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package vibesession

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/sessionlog"
	"github.com/faroshq/provider-vibe-studio/store"
)

// productionEnvironment is the environment name the promote API writes.
const productionEnvironment = vibev1alpha1.ProductionEnvironment

// mirrorPromotion converges the session's production checkpoint from the
// Project's live status. Promotion itself needs no controller — the Project
// reconciler creates the production instance from the spec the promote API
// wrote — so all this loop owes the user is an honest report of where that
// instance got to.
func (r *Reconciler) mirrorPromotion(ctx context.Context, c client.Client, sess *vibev1alpha1.Session, scope store.Scope, state session.SessionState) error {
	if state.ProjectName == "" {
		return nil
	}
	var p vibev1alpha1.Project
	if err := c.Get(ctx, types.NamespacedName{Name: state.ProjectName}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	cp, ok := promotionCheckpoint(&p)
	if !ok {
		return nil
	}
	return sessionlog.SetCheckpoint(ctx, r.Store, scope, sess.Name, cp)
}

// promotionCheckpoint reports the production checkpoint a Project implies, and
// whether the Project has been promoted at all. Pure.
func promotionCheckpoint(p *vibev1alpha1.Project) (session.Checkpoint, bool) {
	var revision string
	promoted := false
	for _, env := range p.Spec.Environments {
		if env.Name == productionEnvironment {
			promoted, revision = true, env.Revision
		}
	}
	if !promoted {
		return session.Checkpoint{}, false
	}
	cp := session.Checkpoint{
		Name:   session.CheckpointProduction,
		State:  session.CheckpointPending,
		Reason: "creating the production deployment",
	}
	if revision != "" {
		cp.Reason = "deploying " + shortRevision(revision)
	}
	for _, env := range p.Status.Environments {
		if env.Name != productionEnvironment {
			continue
		}
		url := ""
		for _, b := range env.Bindings {
			if b.URL != "" {
				url = b.URL
			}
			if reason := b.Outputs["error"]; reason != "" {
				cp.State, cp.Reason = session.CheckpointError, reason
				return cp, true
			}
		}
		if env.Phase == "Ready" {
			cp.State = session.CheckpointDone
			cp.Reason = "live"
			if revision != "" {
				cp.Reason = "live on " + shortRevision(revision)
			}
			if url != "" {
				cp.Reason += " — " + url
			}
		}
	}
	return cp, true
}

// shortRevision abbreviates a commit SHA the way git does.
func shortRevision(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
