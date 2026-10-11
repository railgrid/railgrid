/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

type outOfOrderSandboxWorkspaceClient struct {
	readStarted chan projectAssistantSandboxWorkspaceRequest
	releaseRead chan struct{}
}

type sequencedSandboxWorkspaceClient struct {
	requests  []projectAssistantSandboxWorkspaceRequest
	responses []projectAssistantSandboxWorkspaceResponse
}

func (c *sequencedSandboxWorkspaceClient) Workspace(_ context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	c.requests = append(c.requests, request)
	if len(c.responses) == 0 {
		return projectAssistantSandboxWorkspaceResponse{}, errors.New("unexpected workspace request")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (*sequencedSandboxWorkspaceClient) Exec(context.Context, identity, dataPlaneRef, projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	return projectSandboxExecResponse{}, nil
}

func (c *outOfOrderSandboxWorkspaceClient) Workspace(ctx context.Context, _ identity, _ dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	switch request.Action {
	case "read":
		select {
		case c.readStarted <- request:
		case <-ctx.Done():
			return projectAssistantSandboxWorkspaceResponse{}, ctx.Err()
		}
		select {
		case <-c.releaseRead:
			return projectAssistantSandboxWorkspaceResponse{SourceRevision: 7, SourceDigest: "remote-v7"}, nil
		case <-ctx.Done():
			return projectAssistantSandboxWorkspaceResponse{}, ctx.Err()
		}
	case "replace":
		return projectAssistantSandboxWorkspaceResponse{SourceRevision: 8, SourceDigest: "remote-v8"}, nil
	case "seed":
		return projectAssistantSandboxWorkspaceResponse{SourceRevision: 1, SourceDigest: "seed-v1"}, nil
	default:
		return projectAssistantSandboxWorkspaceResponse{}, errors.New("unexpected workspace action")
	}
}

func (*outOfOrderSandboxWorkspaceClient) Exec(context.Context, identity, dataPlaneRef, projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	return projectSandboxExecResponse{}, nil
}

func TestProjectAssistantRunSandboxRejectsDelayedRemoteFenceRollback(t *testing.T) {
	client := &outOfOrderSandboxWorkspaceClient{
		readStarted: make(chan projectAssistantSandboxWorkspaceRequest, 1),
		releaseRead: make(chan struct{}),
	}
	sandbox := &projectAssistantRunSandbox{
		client: client,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: 100, SourceDigest: "local-v100",
			RemoteRevision: 7, RemoteDigest: "remote-v7", RemoteCheckpointID: "checkpoint-v7",
			HardExpiresAt: time.Now().Add(time.Hour),
		},
	}

	readResult := make(chan error, 1)
	go func() {
		_, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "read", Path: "main.go"})
		readResult <- err
	}()

	select {
	case readRequest := <-client.readStarted:
		if readRequest.SourceRevision != 7 || readRequest.SourceDigest != "remote-v7" {
			t.Fatalf("read used source fence revision %d digest %q; want remote revision 7 and remote digest", readRequest.SourceRevision, readRequest.SourceDigest)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed read did not start")
	}

	if _, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "replace", Path: "main.go", Content: "new\n"}); err != nil {
		t.Fatalf("newer mutation request: %v", err)
	}
	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 8 || got.RemoteDigest != "remote-v8" {
		t.Fatalf("newer mutation fence = revision %d digest %q, want remote revision 8", got.RemoteRevision, got.RemoteDigest)
	}

	close(client.releaseRead)
	select {
	case err := <-readResult:
		if !errors.Is(err, errProjectAssistantRunSandboxConflict) {
			t.Fatalf("delayed read error = %v, want stale-fence conflict", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed read did not finish")
	}

	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 8 || got.RemoteDigest != "remote-v8" || got.RemoteCheckpointID != "checkpoint-v7" {
		t.Fatalf("delayed read rolled back remote metadata: revision %d digest %q checkpoint %q", got.RemoteRevision, got.RemoteDigest, got.RemoteCheckpointID)
	}
}

func TestProjectAssistantRunSandboxSeedCanStartNewRemoteFenceDomain(t *testing.T) {
	client := &outOfOrderSandboxWorkspaceClient{}
	sandbox := &projectAssistantRunSandbox{
		client: client,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: 100, SourceDigest: "local-v100",
			RemoteRevision: 8, RemoteDigest: "remote-v8", RemoteCheckpointID: "checkpoint-v8",
			HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
	if _, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "seed"}); err != nil {
		t.Fatalf("seed into a new remote domain: %v", err)
	}
	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 1 || got.RemoteDigest != "seed-v1" || got.RemoteCheckpointID != "" {
		t.Fatalf("seeded remote fence = revision %d digest %q checkpoint %q, want new revision 1 and no old checkpoint", got.RemoteRevision, got.RemoteDigest, got.RemoteCheckpointID)
	}
}

func TestProjectAssistantRunSandboxMissingAdvancedDigestInvalidatesPreviewFence(t *testing.T) {
	const sourceDigest = "source-v100"
	client := &sequencedSandboxWorkspaceClient{responses: []projectAssistantSandboxWorkspaceResponse{
		{SourceRevision: 9},
		{SourceRevision: 9},
		{SourceRevision: 9, SourceDigest: sourceDigest},
		{},
	}}
	sandbox := &projectAssistantRunSandbox{
		client: client,
		metadata: projectAssistantRunSandboxMetadata{
			Status: "active", SourceRevision: 100, SourceDigest: sourceDigest,
			RemoteRevision: 8, RemoteDigest: sourceDigest,
			HardExpiresAt: time.Now().Add(time.Hour),
		},
	}
	fence := &projectAssistantPreviewSourceFence{
		server: &Server{}, sandbox: sandbox, revision: 100, digest: sourceDigest,
	}
	if err := fence.validate(context.Background()); err != nil {
		t.Fatalf("initial source fence should validate: %v", err)
	}

	if _, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "read"}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("newer response without digest error = %v, want source-fence conflict", err)
	}
	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 9 || got.RemoteDigest != "" {
		t.Fatalf("missing-digest response retained stale remote fence: revision %d digest %q", got.RemoteRevision, got.RemoteDigest)
	}
	if err := fence.validate(context.Background()); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("captured preview fence certified missing remote digest: %v", err)
	}

	if _, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "read"}); err != nil {
		t.Fatalf("same-revision response without digest: %v", err)
	}
	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 9 || got.RemoteDigest != "" {
		t.Fatalf("empty same-revision response restored stale digest: revision %d digest %q", got.RemoteRevision, got.RemoteDigest)
	}
	if got := client.requests[1]; got.SourceRevision != 9 || got.SourceDigest != "" {
		t.Fatalf("request after invalidation reused stale fence: revision %d digest %q", got.SourceRevision, got.SourceDigest)
	}

	if _, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "read"}); err != nil {
		t.Fatalf("valid same-revision response: %v", err)
	}
	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 9 || got.RemoteDigest != sourceDigest {
		t.Fatalf("valid response did not restore remote fence: revision %d digest %q", got.RemoteRevision, got.RemoteDigest)
	}
	if err := fence.validate(context.Background()); err != nil {
		t.Fatalf("captured source fence should validate after matching digest recovery: %v", err)
	}

	if _, err := sandbox.request(context.Background(), projectAssistantSandboxWorkspaceRequest{Action: "read"}); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("unversioned response error = %v, want source-fence conflict", err)
	}
	if got := sandbox.metadataSnapshot(); got.RemoteRevision != 9 || got.RemoteDigest != "" {
		t.Fatalf("unversioned response retained a certifiable remote digest: revision %d digest %q", got.RemoteRevision, got.RemoteDigest)
	}
	if err := fence.validate(context.Background()); !errors.Is(err, errProjectAssistantRunSandboxConflict) {
		t.Fatalf("captured source fence certified an unversioned response: %v", err)
	}
}
