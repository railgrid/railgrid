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
	"testing"
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestRegisteredTeamPolicy(t *testing.T) {
	ctx := context.Background()
	conn := api.Connection{ObjectMeta: metav1.ObjectMeta{Name: "main", UID: "current"}}
	team := api.Team{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/" + api.Version, Kind: "Team"}, ObjectMeta: metav1.ObjectMeta{Name: "engineering", UID: "team-uid"}, Spec: api.TeamSpec{Connection: "main", ConnectionUID: "current", TeamID: "eng"}}
	for _, tc := range []struct {
		name    string
		alter   func(*api.Team)
		allowed bool
	}{
		{"registered", func(*api.Team) {}, true},
		{"old connection", func(t *api.Team) { t.Spec.ConnectionUID = "old" }, false},
		{"another connection", func(t *api.Team) { t.Spec.Connection = "other" }, false},
		{"deleting", func(t *api.Team) { now := metav1.Now(); t.DeletionTimestamp = &now }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := team.DeepCopy()
			tc.alter(registered)
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Teams: "TeamList"}, object(t, registered))
			e := Engine{Client: client}
			effective, err := e.effectivePolicy(ctx, conn)
			if err != nil {
				t.Fatal(err)
			}
			if Allowed(effective, "eng") != tc.allowed || Allowed(effective, "unregistered") {
				t.Fatal("registration policy not enforced")
			}
			if err := client.Resource(Teams).Delete(ctx, registered.Name, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			effective, err = e.effectivePolicy(ctx, conn)
			if err != nil {
				t.Fatal(err)
			}
			if Allowed(effective, "eng") {
				t.Fatal("removed Team still authorizes operations")
			}
		})
	}

}

func TestTeamProbeRejectsReplacedConnection(t *testing.T) {
	e, _ := setup(t)
	team := api.Team{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/" + api.Version, Kind: "Team"}, ObjectMeta: metav1.ObjectMeta{Name: "eng"}, Spec: api.TeamSpec{Connection: "linear", ConnectionUID: "replaced", TeamID: "allowed"}}
	u, err := e.Client.Resource(Teams).Create(context.Background(), object(t, &team), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ProbeTeam(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	var got api.Team
	if err = Decode(u, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Ready || got.Status.Message == "" || got.Status.CheckedAt == nil || time.Since(got.Status.CheckedAt.Time) > time.Minute {
		t.Fatalf("unexpected status: %+v", got.Status)
	}
}

func TestNoRegistrationsDenyIssueCreation(t *testing.T) {
	e, op := setup(t)
	ctx := context.Background()
	if err := e.Client.Resource(Teams).Delete(ctx, "allowed", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(ctx, op); err != nil {
		t.Fatal(err)
	}
	var result api.Operation
	if err := Decode(op, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status.Phase != "Failed" || result.Status.Message != "teamID required and must be allowed by connection" {
		t.Fatalf("unregistered team was not denied: %+v", result.Status)
	}
}
