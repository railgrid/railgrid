// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

// scheduleCR is an in-memory CRAccess exercising the self-scheduling tools.
type scheduleCR struct {
	items   map[string]*agentsv1alpha1.Schedule
	deleted []string
	updated []*agentsv1alpha1.Schedule
}

func newScheduleCR(items ...*agentsv1alpha1.Schedule) *scheduleCR {
	cr := &scheduleCR{items: map[string]*agentsv1alpha1.Schedule{}}
	for _, s := range items {
		cr.items[s.Name] = s
	}
	return cr
}

var scheduleResource = schema.GroupResource{Group: "agents.faros.sh", Resource: "schedules"}

func (c *scheduleCR) GetAgent(context.Context, string) (*agentsv1alpha1.Agent, error) {
	return nil, nil
}

func (c *scheduleCR) CreateSchedule(_ context.Context, s *agentsv1alpha1.Schedule) error {
	if _, ok := c.items[s.Name]; ok {
		return apierrors.NewAlreadyExists(scheduleResource, s.Name)
	}
	c.items[s.Name] = s
	return nil
}

func (c *scheduleCR) GetSchedule(_ context.Context, name string) (*agentsv1alpha1.Schedule, error) {
	s, ok := c.items[name]
	if !ok {
		return nil, apierrors.NewNotFound(scheduleResource, name)
	}
	return s.DeepCopy(), nil
}

func (c *scheduleCR) UpdateSchedule(_ context.Context, s *agentsv1alpha1.Schedule) error {
	if _, ok := c.items[s.Name]; !ok {
		return apierrors.NewNotFound(scheduleResource, s.Name)
	}
	c.items[s.Name] = s
	c.updated = append(c.updated, s)
	return nil
}

func (c *scheduleCR) DeleteSchedule(_ context.Context, name string) error {
	if _, ok := c.items[name]; !ok {
		return apierrors.NewNotFound(scheduleResource, name)
	}
	delete(c.items, name)
	c.deleted = append(c.deleted, name)
	return nil
}

func (c *scheduleCR) ListSchedules(context.Context) ([]agentsv1alpha1.Schedule, error) {
	out := make([]agentsv1alpha1.Schedule, 0, len(c.items))
	for _, s := range c.items {
		out = append(out, *s)
	}
	return out, nil
}

func (c *scheduleCR) ListConnections(context.Context) ([]agentsv1alpha1.Connection, error) {
	return nil, nil
}

func (c *scheduleCR) GetConnection(context.Context, string) (*agentsv1alpha1.Connection, error) {
	return nil, nil
}

func (c *scheduleCR) GetToolset(context.Context, string) (*agentsv1alpha1.Toolset, error) {
	return nil, nil
}

func cronSchedule(name, agentRef, expr string) *agentsv1alpha1.Schedule {
	return &agentsv1alpha1.Schedule{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: agentsv1alpha1.ScheduleSpec{
			AgentRef: agentRef, Type: agentsv1alpha1.ScheduleTypeCron,
			Schedule: expr, Task: "post the news",
		},
	}
}

// coreTool finds one core tool by name, and fails the test if the family stopped
// shipping it.
func coreTool(t *testing.T, d Deps, name string) func(context.Context, string) (string, error) {
	t.Helper()
	for _, tool := range Core(d) {
		if tool.Name == name {
			return tool.Exec
		}
	}
	t.Fatalf("core family has no %q tool", name)
	return nil
}

func scheduleDeps(cr *scheduleCR) Deps {
	return Deps{
		Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "faros"}},
		CR:    cr,
	}
}

// TestScheduleUpdateRetimesCron is the case that failed in production: an agent
// asked to move its daily-news cron to 09:00 could only create and list, so it
// gave up. Updating in place must retime the existing schedule.
func TestScheduleUpdateRetimesCron(t *testing.T) {
	cr := newScheduleCR(cronSchedule("daily-news", "faros", "30 * * * *"))
	exec := coreTool(t, scheduleDeps(cr), "schedule_update")

	out, err := exec(t.Context(), `{"name":"daily-news","schedule":"0 9 * * *","timeZone":"Europe/Vilnius"}`)
	if err != nil {
		t.Fatalf("schedule_update: %v", err)
	}
	if !strings.Contains(out, "0 9 * * *") {
		t.Errorf("result should report the new cron, got %q", out)
	}
	got := cr.items["daily-news"]
	if got.Spec.Schedule != "0 9 * * *" || got.Spec.TimeZone != "Europe/Vilnius" {
		t.Fatalf("schedule not retimed: %+v", got.Spec)
	}
	if got.Spec.Task != "post the news" {
		t.Errorf("untouched fields must survive, task became %q", got.Spec.Task)
	}
}

// TestScheduleUpdateSuspendResume asserts suspend round-trips both ways —
// suspend=false must be applied, not read as "absent".
func TestScheduleUpdateSuspendResume(t *testing.T) {
	cr := newScheduleCR(cronSchedule("daily-news", "faros", "0 9 * * *"))
	exec := coreTool(t, scheduleDeps(cr), "schedule_update")

	if _, err := exec(t.Context(), `{"name":"daily-news","suspend":true}`); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !cr.items["daily-news"].Spec.Suspend {
		t.Fatal("schedule should be suspended")
	}
	if _, err := exec(t.Context(), `{"name":"daily-news","suspend":false}`); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if cr.items["daily-news"].Spec.Suspend {
		t.Fatal("schedule should be resumed")
	}
}

// TestScheduleUpdateRejectsEmptyPatch keeps a no-op call from reporting success.
func TestScheduleUpdateRejectsEmptyPatch(t *testing.T) {
	cr := newScheduleCR(cronSchedule("daily-news", "faros", "0 9 * * *"))
	exec := coreTool(t, scheduleDeps(cr), "schedule_update")

	if _, err := exec(t.Context(), `{"name":"daily-news"}`); err == nil {
		t.Fatal("expected an error when no field is patched")
	}
	if len(cr.updated) != 0 {
		t.Fatalf("empty patch must not write, got %d updates", len(cr.updated))
	}
}

// TestScheduleToolsRejectOtherAgents asserts the self-scheduling tools stay
// self-scoped: schedules are cluster-scoped in the tenant workspace, so an agent
// must not retime or delete a sibling agent's schedule.
func TestScheduleToolsRejectOtherAgents(t *testing.T) {
	cr := newScheduleCR(cronSchedule("their-news", "other-agent", "0 9 * * *"))
	d := scheduleDeps(cr)

	if _, err := coreTool(t, d, "schedule_update")(t.Context(), `{"name":"their-news","suspend":true}`); err == nil {
		t.Fatal("expected schedule_update to refuse another agent's schedule")
	}
	if _, err := coreTool(t, d, "schedule_delete")(t.Context(), `{"name":"their-news"}`); err == nil {
		t.Fatal("expected schedule_delete to refuse another agent's schedule")
	}
	if len(cr.deleted) != 0 || len(cr.updated) != 0 {
		t.Fatal("another agent's schedule must be untouched")
	}
}

func TestScheduleDelete(t *testing.T) {
	cr := newScheduleCR(cronSchedule("daily-news", "faros", "0 9 * * *"))
	d := scheduleDeps(cr)

	if _, err := coreTool(t, d, "schedule_delete")(t.Context(), `{"name":"daily-news"}`); err != nil {
		t.Fatalf("schedule_delete: %v", err)
	}
	if _, ok := cr.items["daily-news"]; ok {
		t.Fatal("schedule should be gone")
	}
	// Unknown names point back at schedules_list rather than leaking a raw 404.
	_, err := coreTool(t, d, "schedule_update")(t.Context(), `{"name":"daily-news","suspend":true}`)
	if err == nil || !strings.Contains(err.Error(), "schedules_list") {
		t.Fatalf("expected a helpful not-found error, got %v", err)
	}
}

// TestScheduleCreateDuplicateSuggestsUpdate: the model's next move after a name
// collision should be schedule_update, so the error has to say so.
func TestScheduleCreateDuplicateSuggestsUpdate(t *testing.T) {
	cr := newScheduleCR(cronSchedule("daily-news", "faros", "30 * * * *"))
	exec := coreTool(t, scheduleDeps(cr), "schedule_create")

	_, err := exec(t.Context(), `{"name":"daily-news","type":"cron","schedule":"0 9 * * *","task":"post the news"}`)
	if err == nil {
		t.Fatal("expected a duplicate-name error")
	}
	if !strings.Contains(err.Error(), "schedule_update") {
		t.Errorf("error should point at schedule_update, got %q", err)
	}
}
