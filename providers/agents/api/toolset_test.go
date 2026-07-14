// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"fmt"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/tools"
)

// fakeCR implements tools.CRAccess for the toolset-expansion test; only
// GetToolset is exercised.
type fakeCR struct {
	toolsets map[string]*agentsv1alpha1.Toolset
}

func (f fakeCR) GetAgent(context.Context, string) (*agentsv1alpha1.Agent, error) { return nil, nil }
func (f fakeCR) CreateSchedule(context.Context, *agentsv1alpha1.Schedule) error  { return nil }
func (f fakeCR) ListSchedules(context.Context) ([]agentsv1alpha1.Schedule, error) {
	return nil, nil
}
func (f fakeCR) ListConnections(context.Context) ([]agentsv1alpha1.Connection, error) {
	return nil, nil
}
func (f fakeCR) GetConnection(context.Context, string) (*agentsv1alpha1.Connection, error) {
	return nil, nil
}
func (f fakeCR) GetToolset(_ context.Context, name string) (*agentsv1alpha1.Toolset, error) {
	if ts, ok := f.toolsets[name]; ok {
		return ts, nil
	}
	return nil, fmt.Errorf("toolset %q not found", name)
}

func mkToolset(name string, families, conns, approval []string) *agentsv1alpha1.Toolset {
	return &agentsv1alpha1.Toolset{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       agentsv1alpha1.ToolsetSpec{Families: families, Connections: conns, RequireApproval: approval},
	}
}

func TestExpandToolsets(t *testing.T) {
	s := &Server{}
	cr := fakeCR{toolsets: map[string]*agentsv1alpha1.Toolset{
		"dev":  mkToolset("dev", []string{"web", "github"}, []string{"gh-acme"}, []string{"github:*"}),
		"ops":  mkToolset("ops", []string{"edges"}, []string{"acme-mcp"}, nil),
		"dupe": mkToolset("dupe", []string{"web"}, []string{"gh-acme"}, nil), // overlaps dev
	}}
	deps := tools.Deps{CR: cr}

	t.Run("no toolsets is a no-op", func(t *testing.T) {
		g := agentsv1alpha1.ToolGrant{Families: []string{"core"}}
		out := s.expandToolsets(context.Background(), deps, g)
		if !slices.Equal(out.Families, []string{"core"}) {
			t.Fatalf("families=%v, want unchanged", out.Families)
		}
	})

	t.Run("merges families/connections/approval, de-duped", func(t *testing.T) {
		g := agentsv1alpha1.ToolGrant{
			Families:        []string{"core", "web"},
			Connections:     []string{"gh-acme"},
			Toolsets:        []string{"dev", "ops", "dupe"},
			RequireApproval: []string{"edges:*"},
		}
		out := s.expandToolsets(context.Background(), deps, g)
		wantFam := []string{"core", "web", "github", "edges"}
		if !slices.Equal(out.Families, wantFam) {
			t.Fatalf("families=%v, want %v", out.Families, wantFam)
		}
		wantConns := []string{"gh-acme", "acme-mcp"}
		if !slices.Equal(out.Connections, wantConns) {
			t.Fatalf("connections=%v, want %v", out.Connections, wantConns)
		}
		wantAppr := []string{"edges:*", "github:*"}
		if !slices.Equal(out.RequireApproval, wantAppr) {
			t.Fatalf("approval=%v, want %v", out.RequireApproval, wantAppr)
		}
	})

	t.Run("missing toolset is skipped, not fatal", func(t *testing.T) {
		g := agentsv1alpha1.ToolGrant{Families: []string{"core"}, Toolsets: []string{"nope", "dev"}}
		out := s.expandToolsets(context.Background(), deps, g)
		if !slices.Contains(out.Families, "github") {
			t.Fatalf("expected dev's families merged despite missing 'nope'; got %v", out.Families)
		}
	})
}
