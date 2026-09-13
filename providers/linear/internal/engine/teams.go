// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"errors"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var Teams = schema.GroupVersionResource{Group: api.GroupName, Version: api.Version, Resource: "teams"}

// TeamAccess contains the upstream team IDs granted by current registrations.
type TeamAccess map[string]bool

// effectivePolicy reads current registration intent, not cached readiness. A
// deleted Team immediately stops authorizing new dispatches. Upstream requests
// still enforce credential access; replacing a Connection never adopts Teams.

func (e Engine) effectivePolicy(ctx context.Context, conn api.Connection) (TeamAccess, error) {
	access := TeamAccess{}
	cursor := ""
	for {
		list, err := e.Client.Resource(Teams).List(ctx, metav1.ListOptions{Limit: 100, Continue: cursor})
		if err != nil {
			return access, errors.New("registered team access unavailable")
		}
		for _, u := range list.Items {
			var team api.Team
			if Decode(&u, &team) != nil {
				return access, errors.New("invalid team registration")
			}
			if team.DeletionTimestamp == nil && team.Spec.Connection == conn.Name && team.Spec.ConnectionUID == string(conn.UID) {
				access[team.Spec.TeamID] = true
			}
		}
		next := list.GetContinue()
		if next == "" {
			break
		}
		if next == cursor {
			return access, errors.New("team pagination did not advance")
		}
		cursor = next
	}
	return access, nil
}

func (e Engine) ProbeTeam(ctx context.Context, u *unstructured.Unstructured) error {
	var team api.Team
	if err := Decode(u, &team); err != nil {
		return err
	}
	if team.DeletionTimestamp != nil {
		return nil
	}
	if team.Status.CheckedAt != nil && e.now().Sub(team.Status.CheckedAt.Time) < 60*1e9 {
		return nil
	}
	status := api.TeamStatus{}
	conn, key, err := e.Connection(ctx, team.Spec.Connection)
	if err == nil && string(conn.UID) != team.Spec.ConnectionUID {
		err = errors.New("connection was replaced")
	}
	if err == nil {
		upstream, readErr := e.API(key).Team(ctx, team.Spec.TeamID)
		err = readErr
		if err == nil {
			status.Ready = true
			status.Name = upstream.Name
			status.Key = upstream.Key
		}
	}
	if err != nil {
		status.Message = "Team unavailable. Check the Connection and this credential's access in Linear."
	}
	now := metav1.NewTime(e.now())
	status.CheckedAt = &now
	value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&status)
	if err != nil {
		return err
	}
	u.Object["status"] = value
	_, err = e.Client.Resource(Teams).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}
