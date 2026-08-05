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
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

// spawnDeps wires the spawn family over recording stubs.
func spawnDeps(onSpawn func(SpawnRequest) (string, error), onJoin func([]string, int) (string, error)) Deps {
	return Deps{
		Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "researcher"}},
		Spawn: func(_ context.Context, req SpawnRequest) (string, error) { return onSpawn(req) },
		Join: func(_ context.Context, ids []string, timeout int) (string, error) {
			return onJoin(ids, timeout)
		},
		SpawnPolicy: SpawnPolicy{
			Families: []string{"core", "web", "mcp"}, DefaultFamilies: []string{"core", "web"},
			MaxPerRun: 10, MaxConcurrent: 4, DefaultToolTurns: 8, MaxToolTurns: 16,
		},
	}
}

func spawnTool(t *testing.T, d Deps, name string) func(context.Context, string) (string, error) {
	t.Helper()
	for _, tool := range Spawn(d) {
		if tool.Name == name {
			return tool.Exec
		}
	}
	t.Fatalf("spawn family has no %q tool", name)
	return nil
}

func TestSpawnFamilyAbsentWithoutClosures(t *testing.T) {
	if got := Spawn(Deps{}); len(got) != 0 {
		t.Fatalf("spawn family must be empty when the api layer injected nothing; got %d tools", len(got))
	}
	// Half-wired is still absent: join without spawn is useless and vice versa.
	half := Deps{Spawn: func(context.Context, SpawnRequest) (string, error) { return "", nil }}
	if got := Spawn(half); len(got) != 0 {
		t.Fatalf("spawn family must require both closures; got %d tools", len(got))
	}
}

func TestSpawnToolParsesArgs(t *testing.T) {
	var got SpawnRequest
	exec := spawnTool(t, spawnDeps(
		func(req SpawnRequest) (string, error) { got = req; return "t1", nil },
		func([]string, int) (string, error) { return "", nil },
	), "spawn")

	out, err := exec(context.Background(), `{"task":"check the docs","instructions":"be brief","tools":["web","mcp"],"maxToolTurns":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "check the docs" || got.Instructions != "be brief" || got.MaxToolTurns != 5 {
		t.Fatalf("parsed request = %+v", got)
	}
	if !slices.Equal(got.Families, []string{"web", "mcp"}) {
		t.Fatalf("families = %v", got.Families)
	}
	// The model needs the id back to join on it.
	if !strings.Contains(out, "t1") {
		t.Fatalf("spawn should report the task id, got %q", out)
	}
}

func TestSpawnToolRejectsBlankTask(t *testing.T) {
	exec := spawnTool(t, spawnDeps(
		func(SpawnRequest) (string, error) { t.Fatal("must not reach the coordinator"); return "", nil },
		func([]string, int) (string, error) { return "", nil },
	), "spawn")
	if _, err := exec(context.Background(), `{"task":"   "}`); err == nil {
		t.Fatal("expected a blank task to be refused before spawning")
	}
}

// Models emit array arguments inconsistently; the tool must survive the shapes
// they actually produce rather than only the schema-perfect one.
func TestSpawnToolToleratesModelArrayShapes(t *testing.T) {
	tests := []struct {
		name string
		args string
		want []string
	}{
		{"json array", `{"task":"t","tools":["web","mcp"]}`, []string{"web", "mcp"}},
		{"bare string", `{"task":"t","tools":"web"}`, []string{"web"}},
		{"comma separated string", `{"task":"t","tools":"web, mcp"}`, []string{"web", "mcp"}},
		{"absent", `{"task":"t"}`, nil},
		{"empty array", `{"task":"t","tools":[]}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got SpawnRequest
			exec := spawnTool(t, spawnDeps(
				func(req SpawnRequest) (string, error) { got = req; return "t1", nil },
				func([]string, int) (string, error) { return "", nil },
			), "spawn")
			if _, err := exec(context.Background(), tc.args); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Families, tc.want) {
				t.Fatalf("families = %v, want %v", got.Families, tc.want)
			}
		})
	}
}

func TestJoinToolParsesArgs(t *testing.T) {
	var gotIDs []string
	var gotTimeout int
	deps := spawnDeps(
		func(SpawnRequest) (string, error) { return "t1", nil },
		func(ids []string, timeout int) (string, error) {
			gotIDs, gotTimeout = ids, timeout
			return "results", nil
		},
	)
	exec := spawnTool(t, deps, "join")

	out, err := exec(context.Background(), `{"taskIds":["t1","t2"],"timeoutSeconds":60}`)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotIDs, []string{"t1", "t2"}) {
		t.Fatalf("ids = %v", gotIDs)
	}
	if gotTimeout != 60 {
		t.Fatalf("timeout = %d, want 60", gotTimeout)
	}
	if out != "results" {
		t.Fatalf("join output = %q, want the coordinator's text verbatim", out)
	}

	t.Run("no arguments means collect everything outstanding", func(t *testing.T) {
		gotIDs, gotTimeout = []string{"stale"}, -1
		if _, err := exec(context.Background(), `{}`); err != nil {
			t.Fatal(err)
		}
		if len(gotIDs) != 0 {
			t.Fatalf("ids = %v, want empty so the coordinator collects all outstanding", gotIDs)
		}
		if gotTimeout != 0 {
			t.Fatalf("timeout = %d, want 0 so the coordinator applies its default", gotTimeout)
		}
	})
}

// The spawn tool's description carries the limits the coordinator will actually
// enforce; a model told "up to 10" and refused at 3 wastes a turn discovering it.
func TestSpawnToolDescriptionStatesLimits(t *testing.T) {
	d := spawnDeps(
		func(SpawnRequest) (string, error) { return "t1", nil },
		func([]string, int) (string, error) { return "", nil },
	)
	d.SpawnPolicy.MaxPerRun, d.SpawnPolicy.MaxConcurrent = 3, 2

	var desc string
	for _, tool := range Spawn(d) {
		if tool.Name == "spawn" {
			desc = tool.Desc
		}
	}
	if !strings.Contains(desc, "3 workers per run") {
		t.Fatalf("description should state the per-run limit, got: %s", desc)
	}
	if !strings.Contains(desc, "2 at once") {
		t.Fatalf("description should state the concurrency limit, got: %s", desc)
	}
	// It also has to argue for itself. A model holding both spawn and web_search
	// will otherwise take the cheaper-looking path — a sequence of searches in one
	// turn — which is what happened in practice.
	if !strings.Contains(desc, "PREFER THIS") {
		t.Fatalf("description should say when to prefer a fan-out, got: %s", desc)
	}
	if !strings.Contains(desc, "single narrow lookup") {
		t.Fatalf("description should also say when NOT to fan out, got: %s", desc)
	}
}
