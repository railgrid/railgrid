// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"fmt"
	"net/url"
)

// githubCI observes the exact current commit, paginating every check run. It
// never treats an empty result or a missing status context as a passing suite.
func observeCI(ctx context.Context, api Client, repo, sha string) (summary, state string, err error) {
	count, pending, failed, skipped := 0, 0, 0, 0
	for page := 1; page <= 20; page++ {
		var response struct {
			Total  int `json:"total_count"`
			Checks []struct {
				Head       string `json:"head_sha"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		if err = api.Do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", repo, sha, page), nil, &response); err != nil {
			return "", "", err
		}
		for _, c := range response.Checks {
			if c.Head != sha {
				return "", "", fmt.Errorf("GitHub returned checks for a different commit")
			}
			count++
			if c.Status != "completed" {
				pending++
				continue
			}
			switch c.Conclusion {
			case "success":
			case "skipped", "neutral":
				skipped++
			default:
				failed++
			}
		}
		if count >= response.Total {
			break
		}
		if page == 20 || len(response.Checks) == 0 {
			return "", "", fmt.Errorf("check history exceeds the observation limit")
		}
	}
	var status struct {
		State string `json:"state"`
		Total int    `json:"total_count"`
		SHA   string `json:"sha"`
	}
	if err = api.Do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s/status", repo, sha), nil, &status); err != nil {
		return "", "", err
	}
	if status.SHA != sha {
		return "", "", fmt.Errorf("GitHub returned status for a different commit")
	}
	// Each source is reported with its own counts; only the combined totals
	// decide the state.
	statusPending, statusFailed := 0, 0
	if status.Total > 0 {
		switch status.State {
		case "failure", "error":
			statusFailed = 1
		case "success":
		default:
			statusPending = 1
		}
	}
	workflows, workflowPending, workflowFailed, workflowErr := observeWorkflows(ctx, api, repo, sha)
	if workflowErr != nil {
		return "", "", workflowErr
	}
	totalPending := pending + statusPending + workflowPending
	totalFailed := failed + statusFailed + workflowFailed
	statusState := "none"
	if status.Total > 0 {
		statusState = status.State
	}
	summary = fmt.Sprintf("CI observation on `%s`: %d check runs (%d pending, %d failed, %d skipped/neutral); %d commit-status contexts (combined state %s). This observes available CI, not a merge approval or an assertion about unseen policy.", sha, count, pending, failed, skipped, status.Total, statusState)
	summary += fmt.Sprintf(" %d current workflow runs (%d pending, %d failed).", workflows, workflowPending, workflowFailed)
	switch {
	case totalFailed > 0:
		return summary, "failed", nil
	case totalPending > 0:
		return summary, "pending", nil
	case count+status.Total+workflows == 0:
		return summary + " No CI evidence exists yet.", "unknown", nil
	default:
		return summary, "passed", nil
	}
}

// observeWorkflows includes aggregate workflow failures that may have no failed
// check run (for example, a missing final job). Only the newest run for each
// workflow/event/branch at this exact head participates; superseded runs do not
// permanently poison a successful replacement. An API error is never a pass.
func observeWorkflows(ctx context.Context, api Client, repo, sha string) (count, pending, failed int, err error) {
	type workflow struct {
		ID         int64  `json:"id"`
		WorkflowID int64  `json:"workflow_id"`
		Attempt    int    `json:"run_attempt"`
		Head       string `json:"head_sha"`
		Branch     string `json:"head_branch"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	type identity struct {
		workflow      int64
		event, branch string
	}
	latest := map[identity]workflow{}
	seen := 0
	for page := 1; page <= 10; page++ {
		var response struct {
			Total int        `json:"total_count"`
			Runs  []workflow `json:"workflow_runs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs?head_sha=%s&per_page=100&page=%d", repo, url.QueryEscape(sha), page)
		if err = api.Do(ctx, "GET", path, nil, &response); err != nil {
			return 0, 0, 0, err
		}
		for _, run := range response.Runs {
			if run.Head != sha || run.ID <= 0 || run.WorkflowID <= 0 || run.Event == "" {
				return 0, 0, 0, fmt.Errorf("GitHub returned invalid workflow identity or a different commit")
			}
			key := identity{run.WorkflowID, run.Event, run.Branch}
			old, found := latest[key]
			if !found || run.ID > old.ID || (run.ID == old.ID && run.Attempt > old.Attempt) {
				latest[key] = run
			}
		}
		seen += len(response.Runs)
		if seen >= response.Total {
			break
		}
		if page == 10 || len(response.Runs) == 0 {
			return 0, 0, 0, fmt.Errorf("workflow history exceeds the observation limit")
		}
	}
	for _, run := range latest {
		count++
		if run.Status != "completed" {
			pending++
			continue
		}
		switch run.Conclusion {
		case "success", "skipped", "neutral":
		default:
			failed++
		}
	}
	return count, pending, failed, nil
}
