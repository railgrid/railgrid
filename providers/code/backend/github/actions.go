/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package github

import (
	"context"
	"io"
	"net/http"
	"strings"

	gogithub "github.com/google/go-github/v66/github"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

const (
	// workflowJobLogMaxBytes caps how much of a job log we download before
	// tailing, so a runaway log never blows up memory.
	workflowJobLogMaxBytes = 1 << 20 // 1 MiB
	// workflowJobLogDefaultLines is the default failure-log tail size.
	workflowJobLogDefaultLines = 200
)

// LatestWorkflowRun implements backend.WorkflowRunReader: it returns the most
// recent run of the named workflow (optionally pinned to a commit), each job's
// status/conclusion, and a log tail for any failed job.
func (b *Backend) LatestWorkflowRun(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, query backend.WorkflowRunQuery) (backend.WorkflowRunStatus, error) {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return backend.WorkflowRunStatus{}, err
	}
	org := owner(conn, repo)

	runOpts := &gogithub.ListWorkflowRunsOptions{ListOptions: gogithub.ListOptions{PerPage: 20}}
	if query.HeadSHA != "" {
		runOpts.HeadSHA = query.HeadSHA
	}
	runs, resp, err := c.Actions.ListWorkflowRunsByFileName(ctx, org, repo.Spec.Name, query.WorkflowFileName, runOpts)
	if err != nil {
		return backend.WorkflowRunStatus{}, classify(resp, err)
	}
	if runs.GetTotalCount() == 0 || len(runs.WorkflowRuns) == 0 {
		return backend.WorkflowRunStatus{Found: false}, nil
	}
	run := runs.WorkflowRuns[0] // the API returns most-recent first
	out := backend.WorkflowRunStatus{
		Found:      true,
		RunID:      run.GetID(),
		HTMLURL:    run.GetHTMLURL(),
		HeadSHA:    run.GetHeadSHA(),
		Status:     run.GetStatus(),
		Conclusion: run.GetConclusion(),
	}

	jobs, resp, err := c.Actions.ListWorkflowJobs(ctx, org, repo.Spec.Name, run.GetID(), &gogithub.ListWorkflowJobsOptions{ListOptions: gogithub.ListOptions{PerPage: 100}})
	if err != nil {
		// Jobs are best-effort detail; the run status itself is still useful.
		return out, nil
	}
	for _, j := range jobs.Jobs {
		js := backend.WorkflowJobStatus{
			Name:       j.GetName(),
			Status:     j.GetStatus(),
			Conclusion: j.GetConclusion(),
		}
		if j.GetConclusion() == "failure" {
			js.FailureLog = jobLogTail(ctx, c, org, repo.Spec.Name, j.GetID(), query.MaxLogLines)
		}
		out.Jobs = append(out.Jobs, js)
	}
	return out, nil
}

// DispatchWorkflow implements backend.WorkflowDispatcher: it fires a
// workflow_dispatch event so the workflow re-runs on ref without a code change.
func (b *Backend) DispatchWorkflow(ctx context.Context, conn *codev1alpha1.Connection, cred backend.Credential, repo *codev1alpha1.Repository, workflowFileName, ref string) error {
	c, err := b.client(ctx, cred, conn.Spec.BaseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ref) == "" {
		ref = "main"
	}
	_, err = c.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner(conn, repo), repo.Spec.Name, workflowFileName, gogithub.CreateWorkflowDispatchEventRequest{Ref: ref})
	if err != nil {
		return classify(nil, err)
	}
	return nil
}

// jobLogTail downloads a job's logs (bounded) and returns the last maxLines,
// best-effort — a log fetch failure yields "".
func jobLogTail(ctx context.Context, c *gogithub.Client, org, repo string, jobID int64, maxLines int) string {
	if maxLines <= 0 {
		maxLines = workflowJobLogDefaultLines
	}
	logURL, _, err := c.Actions.GetWorkflowJobLogs(ctx, org, repo, jobID, 3)
	if err != nil || logURL == nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
	if err != nil {
		return ""
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, workflowJobLogMaxBytes))
	if err != nil {
		return ""
	}
	return tailLines(string(body), maxLines)
}

// tailLines returns the last n non-empty-trimmed lines of s (trailing newline
// tolerated).
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
