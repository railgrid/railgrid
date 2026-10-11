/*
Copyright 2026 The Railgrid Authors.

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

package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
)

func TestSharedRuntimeExecBlocksStartWhenInstanceIsReplacedAfterSnapshot(t *testing.T) {
	var fixture sharedRuntimeExecFixture
	var starts atomic.Int32
	var replaced atomic.Bool
	fixture = newSharedRuntimeExecFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			if replaced.CompareAndSwap(false, true) {
				setSharedRuntimeExecInstanceUID(t, fixture, "instance-replacement")
			}
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{"base64"}})
		}
		decoded, err := decodeSharedRuntimeExecRequest(request)
		if err != nil {
			return nil, err
		}
		if decoded.Action == "start" {
			starts.Add(1)
		}
		return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{SessionID: "unexpected-session", State: "succeeded", ExitCode: intPointer(0)})
	})
	setSharedRuntimeExecInstanceUID(t, fixture, "instance-original")
	if _, err := fixture.files.PutFile(fixture.baseCtx, fixture.scope, workspace.PutOptions{Path: "api/icon.png", Data: []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff}}); err != nil {
		t.Fatalf("add binary file to exercise the capability probe: %v", err)
	}

	result, err := execProjectAssistantCommand(fixture.runContext("run-instance-replaced"))(fixture.baseCtx, &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test"},
	})
	if err != nil {
		t.Fatalf("shared runtime command: %v", err)
	}
	if result == nil || result.Status != "blocked" {
		t.Fatalf("replaced instance result = %#v, want blocked before START", result)
	}
	if starts.Load() != 0 {
		t.Fatalf("START requests = %d, want no command against the replacement instance", starts.Load())
	}
}

func TestSharedRuntimeExecBlocksWhenInstanceUIDIsMissing(t *testing.T) {
	var starts atomic.Int32
	fixture := newSharedRuntimeExecFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{}})
		}
		decoded, err := decodeSharedRuntimeExecRequest(request)
		if err != nil {
			return nil, err
		}
		if decoded.Action == "start" {
			starts.Add(1)
		}
		return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{SessionID: "unexpected-session", State: "succeeded", ExitCode: intPointer(0)})
	})
	setSharedRuntimeExecInstanceUID(t, fixture, "")

	result, err := execProjectAssistantCommand(fixture.runContext("run-missing-instance-uid"))(fixture.baseCtx, &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test"},
	})
	if err != nil {
		t.Fatalf("shared runtime command: %v", err)
	}
	if result == nil || result.Status != "blocked" {
		t.Fatalf("missing instance UID result = %#v, want blocked", result)
	}
	if starts.Load() != 0 {
		t.Fatalf("START requests = %d, want no command without an immutable instance UID", starts.Load())
	}
}

func TestSharedRuntimeExecRechecksTargetBeforeRetryAfterAmbiguousStart(t *testing.T) {
	var fixture sharedRuntimeExecFixture
	var starts, cancels atomic.Int32
	var startRequest, cancelRequest projectSandboxExecRequest
	var startURL, cancelURL string
	fixture = newSharedRuntimeExecFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, map[string]any{"syncEncodings": []string{}})
		}
		decoded, err := decodeSharedRuntimeExecRequest(request)
		if err != nil {
			return nil, err
		}
		switch decoded.Action {
		case "start":
			if starts.Add(1) == 1 {
				startRequest = decoded
				startURL = request.URL.String()
				setSharedRuntimeExecBackendPath(t, fixture, "services")
				return sharedRuntimeExecHTTPResponse(request, http.StatusServiceUnavailable, map[string]string{"error": "upstream unavailable"})
			}
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, SessionID: "unexpected-session", State: "running"})
		case "cancel":
			cancels.Add(1)
			cancelRequest = decoded
			cancelURL = request.URL.String()
			return sharedRuntimeExecHTTPResponse(request, http.StatusOK, projectSandboxExecResponse{RequestID: decoded.RequestID, State: "cancelled"})
		default:
			return sharedRuntimeExecHTTPResponse(request, http.StatusBadRequest, map[string]string{"error": "unexpected action"})
		}
	})

	result, err := execProjectAssistantCommand(fixture.runContext("run-target-changed-on-retry"))(fixture.baseCtx, &projectAssistantExecCommandInput{
		Component: "backend",
		Argv:      []string{"go", "test"},
	})
	if err != nil {
		t.Fatalf("shared runtime command: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("physical START requests = %d, want only the first ambiguous attempt", starts.Load())
	}
	if cancels.Load() != 1 {
		t.Fatalf("request-ID cancel requests = %d, want cleanup at the original target", cancels.Load())
	}
	if startRequest.RequestID == "" || cancelRequest.RequestID != startRequest.RequestID || cancelRequest.SessionID != "" {
		t.Fatalf("start/cancel identity = %#v / %#v, want request-ID-only cancel for the original START", startRequest, cancelRequest)
	}
	if cancelURL != startURL {
		t.Fatalf("cancel URL = %q, want the original target %q", cancelURL, startURL)
	}
	if result == nil || result.Status != "error" || result.SessionID != "" || result.ExitCode != nil || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("ambiguous start result = %#v, want unconfirmed error without invented session or output", result)
	}
	if result.Summary == "" {
		t.Fatal("ambiguous start result has no explanation")
	}
}

func setSharedRuntimeExecInstanceUID(t *testing.T, fixture sharedRuntimeExecFixture, uid string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.baseCtx, 5*time.Second)
	defer cancel()
	resource := fixture.client.Dynamic().Resource(tenant.InfrastructureInstancesResource.GVR)
	instance, err := resource.Get(ctx, projectTemplateInstanceName(fixture.project), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get development instance: %v", err)
	}
	instance.SetUID(types.UID(uid))
	if _, err := resource.Update(ctx, instance, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update development instance UID: %v", err)
	}
}

func setSharedRuntimeExecBackendPath(t *testing.T, fixture sharedRuntimeExecFixture, workspacePath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.baseCtx, 5*time.Second)
	defer cancel()
	resource := fixture.client.Dynamic().Resource(templateResource.GVR)
	template, err := resource.Get(ctx, "application", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get development Template: %v", err)
	}
	component, found, err := unstructured.NestedMap(template.Object, "spec", "development", "components", "backend")
	if err != nil || !found {
		t.Fatalf("read backend component: found=%t err=%v", found, err)
	}
	component["workspacePath"] = workspacePath
	if err := unstructured.SetNestedMap(template.Object, component, "spec", "development", "components", "backend"); err != nil {
		t.Fatalf("set backend workspace path: %v", err)
	}
	if _, err := resource.Update(ctx, template, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update development Template: %v", err)
	}
}
