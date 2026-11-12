// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestCIIncludesLatestWorkflowAggregate(t *testing.T) {
	head := strings.Repeat("a", 40)
	run := func(id, workflow int, event, state, conclusion string) map[string]any {
		return map[string]any{"id": id, "workflow_id": workflow, "head_sha": head, "head_branch": "topic", "event": event, "status": state, "conclusion": conclusion}
	}
	for _, tc := range []struct {
		name    string
		runs    []any
		want    string
		wantErr bool
	}{
		{name: "missing summary workflow failure", runs: []any{run(2, 1, "push", "completed", "failure")}, want: "failed"},
		{name: "pending workflow with green jobs", runs: []any{run(2, 1, "push", "in_progress", "")}, want: "pending"},
		{name: "successful replacement", runs: []any{run(3, 1, "push", "completed", "success"), run(2, 1, "push", "completed", "cancelled")}, want: "passed"},
		{name: "push failure survives successful PR workflow", runs: []any{run(3, 1, "pull_request", "completed", "success"), run(2, 1, "push", "completed", "failure")}, want: "failed"},
		{name: "workflow API inaccessible", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Client{HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var out any
				switch {
				case strings.HasSuffix(r.URL.Path, "/check-runs"):
					out = map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"head_sha": head, "status": "completed", "conclusion": "success"}}}
				case strings.HasSuffix(r.URL.Path, "/status"):
					out = map[string]any{"sha": head, "total_count": 0}
				case strings.HasSuffix(r.URL.Path, "/actions/runs"):
					if r.URL.Query().Get("head_sha") != head {
						t.Fatal("workflow request not pinned")
					}
					if tc.wantErr {
						return nil, fmt.Errorf("actions read denied")
					}
					out = map[string]any{"total_count": len(tc.runs), "workflow_runs": tc.runs}
				default:
					t.Fatalf("unexpected URL %s", r.URL)
				}
				raw, _ := json.Marshal(out)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
			})}}
			_, state, err := observeCI(context.Background(), c, "railgrid/example", head)
			if (err != nil) != tc.wantErr || (!tc.wantErr && state != tc.want) {
				t.Fatalf("state=%s err=%v", state, err)
			}
		})
	}
}

func TestWorkflowObservationPaginationAndHeadFence(t *testing.T) {
	head := strings.Repeat("a", 40)
	for _, wrongHead := range []bool{false, true} {
		c := Client{HTTP: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			current := head
			if wrongHead {
				current = strings.Repeat("b", 40)
			}
			conclusion := "success"
			if page == 2 {
				conclusion = "failure"
			}
			raw, _ := json.Marshal(map[string]any{"total_count": 2, "workflow_runs": []any{map[string]any{"id": page, "workflow_id": page, "event": "push", "head_sha": current, "status": "completed", "conclusion": conclusion}}})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
		})}}
		count, _, failed, err := observeWorkflows(context.Background(), c, "railgrid/example", head)
		if wrongHead {
			if err == nil {
				t.Fatal("accepted foreign head")
			}
		} else if err != nil || count != 2 || failed != 1 {
			t.Fatalf("count=%d failed=%d err=%v", count, failed, err)
		}
	}
}
