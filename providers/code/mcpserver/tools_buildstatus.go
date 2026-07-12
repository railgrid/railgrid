/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
)

var repositoryBuildStatusesGVR = codev1alpha1.SchemeGroupVersion.WithResource("repositorybuildstatuses")

type buildStatusInput struct {
	RepositoryRef    string `json:"repositoryRef" jsonschema:"Name of the managed Repository CR whose build to inspect"`
	WorkflowFileName string `json:"workflowFileName" jsonschema:"Workflow file name to inspect, e.g. kedge-app-studio-build.yml"`
	Ref              string `json:"ref,omitempty" jsonschema:"Commit SHA to inspect; defaults to the most recent run"`
	MaxLogLines      int    `json:"maxLogLines,omitempty" jsonschema:"Max failure-log lines per failed job (default 200)"`
}

type buildStatusJobOutput struct {
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	FailureLog string `json:"failureLog,omitempty"`
}

type buildStatusOutput struct {
	RepositoryRef string                 `json:"repositoryRef"`
	Found         bool                   `json:"found"`
	RunID         int64                  `json:"runID,omitempty"`
	HTMLURL       string                 `json:"htmlURL,omitempty"`
	HeadSHA       string                 `json:"headSHA,omitempty"`
	Status        string                 `json:"status,omitempty"`
	Conclusion    string                 `json:"conclusion,omitempty"`
	Jobs          []buildStatusJobOutput `json:"jobs,omitempty"`
}

type rebuildInput struct {
	RepositoryRef    string `json:"repositoryRef" jsonschema:"Name of the managed Repository CR to re-run the build for"`
	WorkflowFileName string `json:"workflowFileName" jsonschema:"Workflow file name to re-run, e.g. kedge-app-studio-build.yml"`
	Ref              string `json:"ref,omitempty" jsonschema:"Branch to re-run on; defaults to the repository default branch"`
}

type rebuildOutput struct {
	RepositoryRef string `json:"repositoryRef"`
	Dispatched    bool   `json:"dispatched"`
}

// registerBuildStatusTools wires the build-doctor read/retry tools: each
// creates a transient RepositoryBuildStatus CR AS THE CALLER; the controller
// queries the host's Actions API with the resolved credential and writes the
// result to status, which the tool returns (then reclaims the CR).
func registerBuildStatusTools(srv *mcp.Server, deps Deps, ident identity) {
	yes := true
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "build_status",
		Title:       "Inspect a repository's CI build run",
		Description: "Read the latest run of a repository's build workflow (optionally for a specific commit): the run status and conclusion, each job's outcome, and a log tail for any failed job. Use it to diagnose why a build failed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in buildStatusInput) (*mcp.CallToolResult, buildStatusOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, buildStatusOutput{}, err
		}
		return buildStatus(ctx, dyn, in)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "rebuild",
		Title:       "Re-run a repository's CI build",
		Description: "Trigger the repository's build workflow to run again without a code change (workflow_dispatch). Use it to retry a flaky or failed build.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in rebuildInput) (*mcp.CallToolResult, rebuildOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, rebuildOutput{}, err
		}
		return rebuildWorkflow(ctx, dyn, in)
	})
}

func buildStatus(ctx context.Context, dyn dynamic.Interface, in buildStatusInput) (*mcp.CallToolResult, buildStatusOutput, error) {
	in.RepositoryRef = strings.TrimSpace(in.RepositoryRef)
	in.WorkflowFileName = strings.TrimSpace(in.WorkflowFileName)
	if in.RepositoryRef == "" || in.WorkflowFileName == "" {
		return nil, buildStatusOutput{}, fmt.Errorf("repositoryRef and workflowFileName are required")
	}
	spec := map[string]any{
		"repositoryRef":    in.RepositoryRef,
		"workflowFileName": in.WorkflowFileName,
		"action":           string(codev1alpha1.RepositoryBuildStatusActionStatus),
	}
	putIf(spec, "ref", in.Ref)
	if in.MaxLogLines > 0 {
		spec["maxLogLines"] = in.MaxLogLines
	}
	obj, err := runBuildStatusRequest(ctx, dyn, in.RepositoryRef, spec)
	if err != nil {
		return nil, buildStatusOutput{}, err
	}
	out := buildStatusOutput{RepositoryRef: in.RepositoryRef}
	run, found, _ := unstructured.NestedMap(obj.Object, "status", "run")
	if found {
		out.Found, _, _ = unstructured.NestedBool(run, "found")
		out.HTMLURL, _, _ = unstructured.NestedString(run, "htmlURL")
		out.HeadSHA, _, _ = unstructured.NestedString(run, "headSHA")
		out.Status, _, _ = unstructured.NestedString(run, "status")
		out.Conclusion, _, _ = unstructured.NestedString(run, "conclusion")
		if id, ok, _ := unstructured.NestedInt64(run, "runID"); ok {
			out.RunID = id
		}
		jobs, _, _ := unstructured.NestedSlice(run, "jobs")
		for _, j := range jobs {
			jm, ok := j.(map[string]any)
			if !ok {
				continue
			}
			job := buildStatusJobOutput{}
			job.Name, _, _ = unstructured.NestedString(jm, "name")
			job.Status, _, _ = unstructured.NestedString(jm, "status")
			job.Conclusion, _, _ = unstructured.NestedString(jm, "conclusion")
			job.FailureLog, _, _ = unstructured.NestedString(jm, "failureLog")
			out.Jobs = append(out.Jobs, job)
		}
	}
	return nil, out, nil
}

func rebuildWorkflow(ctx context.Context, dyn dynamic.Interface, in rebuildInput) (*mcp.CallToolResult, rebuildOutput, error) {
	in.RepositoryRef = strings.TrimSpace(in.RepositoryRef)
	in.WorkflowFileName = strings.TrimSpace(in.WorkflowFileName)
	if in.RepositoryRef == "" || in.WorkflowFileName == "" {
		return nil, rebuildOutput{}, fmt.Errorf("repositoryRef and workflowFileName are required")
	}
	spec := map[string]any{
		"repositoryRef":    in.RepositoryRef,
		"workflowFileName": in.WorkflowFileName,
		"action":           string(codev1alpha1.RepositoryBuildStatusActionRerun),
	}
	putIf(spec, "ref", in.Ref)
	obj, err := runBuildStatusRequest(ctx, dyn, in.RepositoryRef, spec)
	if err != nil {
		return nil, rebuildOutput{}, err
	}
	dispatched, _, _ := unstructured.NestedBool(obj.Object, "status", "dispatched")
	return nil, rebuildOutput{RepositoryRef: in.RepositoryRef, Dispatched: dispatched}, nil
}

// runBuildStatusRequest creates the transient CR, waits for a terminal phase,
// returns the completed object, and reclaims the CR.
func runBuildStatusRequest(ctx context.Context, dyn dynamic.Interface, repositoryRef string, spec map[string]any) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "RepositoryBuildStatus",
		"metadata": map[string]any{
			"name":   checkoutObjectName(repositoryRef, time.Now()),
			"labels": map[string]any{codev1alpha1.LabelRepository: repositoryRef},
		},
		"spec": spec,
	}}
	created, err := dyn.Resource(repositoryBuildStatusesGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("create RepositoryBuildStatus: the RepositoryBuildStatus API is not available in this workspace; re-register the Code provider: %w", err)
		}
		return nil, fmt.Errorf("create RepositoryBuildStatus: %w", err)
	}
	defer func() {
		_ = dyn.Resource(repositoryBuildStatusesGVR).Delete(context.WithoutCancel(ctx), created.GetName(), metav1.DeleteOptions{})
	}()

	waited, err := waitRepositoryBuildStatus(ctx, dyn, created.GetName(), 75*time.Second)
	if err != nil {
		return nil, err
	}
	if waited == nil {
		return nil, fmt.Errorf("RepositoryBuildStatus %q did not complete in time", created.GetName())
	}
	phase, _, _ := unstructured.NestedString(waited.Object, "status", "phase")
	if phase != string(codev1alpha1.RepositoryBuildStatusPhaseSucceeded) {
		return nil, fmt.Errorf("RepositoryBuildStatus %q failed: %s", created.GetName(), repositoryCommitConditionMessage(waited))
	}
	return waited, nil
}

func waitRepositoryBuildStatus(ctx context.Context, dyn dynamic.Interface, name string, timeout time.Duration) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		obj, err := dyn.Resource(repositoryBuildStatusesGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			return nil, fmt.Errorf("get RepositoryBuildStatus %q: %w", name, err)
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if phase == string(codev1alpha1.RepositoryBuildStatusPhaseSucceeded) || phase == string(codev1alpha1.RepositoryBuildStatusPhaseFailed) {
			return obj, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-ticker.C:
		}
	}
}
