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

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectViewWithThumbnailCapturesLatestSuccessfulCommit(t *testing.T) {
	imageData := testProjectThumbnailPNG(t, 1280, 720)
	inspector := &fakeProjectAssistantPreviewInspector{result: projectAssistantPreviewInspectionResult{
		Status: "succeeded",
		Screenshot: &projectAssistantPreviewInspectionScreenshot{
			MIMEType: "image/png", Base64: base64.StdEncoding.EncodeToString(imageData),
		},
	}}
	thumbnailContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	memory := store.NewMemoryStore()
	s := &Server{
		store:                   memory,
		previewInspector:        inspector,
		projectThumbnailContext: thumbnailContext,
		projectThumbnailCurrentness: func(context.Context, identity, *aiv1alpha1.Project, uint64) error {
			return nil
		},
		previewInspectionResolveURL: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "https://preview.example/", nil
		},
	}
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "app", UID: types.UID("uid")}}
	id := identity{orgUUID: "org", workspaceUUID: "workspace"}
	now := time.Now().UTC()
	view := ProjectView{SourceRevision: 7, Repository: &ProjectRepositoryView{Commits: []ProjectRepositoryCommitView{
		{Name: "commit-newest", Phase: "Succeeded", CommitSHA: "newest", CreatedAt: now},
		{Name: "commit-older", Phase: "Succeeded", CommitSHA: "older", CreatedAt: now.Add(-time.Hour)},
	}}}
	view = s.projectViewWithThumbnail(context.Background(), view, id, project)
	if view.Thumbnail == nil || !view.Thumbnail.Refreshing || view.Thumbnail.Available {
		t.Fatalf("initial thumbnail view = %#v", view.Thumbnail)
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		thumbnail, err := memory.GetProjectThumbnail(context.Background(), scope)
		if err == nil {
			config, err := png.DecodeConfig(bytes.NewReader(thumbnail.Data))
			if err != nil || thumbnail.CommitSHA != "newest" || thumbnail.CommitOrder != "commit-newest" || thumbnail.Variant != projectThumbnailVariant || config.Width != 640 || config.Height != 360 {
				t.Fatalf("thumbnail = %#v", thumbnail)
			}
			ready := s.projectViewWithThumbnail(context.Background(), view, id, project).Thumbnail
			if ready == nil || !ready.Available || ready.Refreshing || ready.CommitSHA != "newest" {
				t.Fatalf("ready thumbnail view = %#v", ready)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("thumbnail capture did not finish")
}

func TestLatestSuccessfulProjectCommitSkipsIncompleteHistory(t *testing.T) {
	now := time.Now().UTC()
	got := latestSuccessfulProjectCommit(&ProjectRepositoryView{Commits: []ProjectRepositoryCommitView{
		{Name: "running", Phase: "Running", CommitSHA: "newest", CreatedAt: now.Add(time.Hour)},
		{Name: "missing-sha", Phase: "Succeeded", CreatedAt: now.Add(time.Minute)},
		{Name: "commit-a", Phase: "Succeeded", CommitSHA: "older", CreatedAt: now},
		{Name: "commit-z", Phase: "Succeeded", CommitSHA: "current", CreatedAt: now},
	}})
	if got.SHA != "current" || got.Order != "commit-z" || !got.CreatedAt.Equal(now) {
		t.Fatalf("latest successful commit = %#v", got)
	}
}

func TestNormalizeProjectThumbnailPNGConstrainsCardDimensionsWithoutUpscaling(t *testing.T) {
	tests := []struct {
		name                  string
		width, height         int
		wantWidth, wantHeight int
	}{
		{name: "large landscape", width: 1280, height: 720, wantWidth: 640, wantHeight: 360},
		{name: "large portrait", width: 800, height: 1200, wantWidth: 240, wantHeight: 360},
		{name: "small", width: 320, height: 180, wantWidth: 320, wantHeight: 180},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeProjectThumbnailPNG(testProjectThumbnailPNG(t, tt.width, tt.height))
			if err != nil {
				t.Fatal(err)
			}
			config, err := png.DecodeConfig(bytes.NewReader(got))
			if err != nil {
				t.Fatal(err)
			}
			if config.Width != tt.wantWidth || config.Height != tt.wantHeight {
				t.Fatalf("normalized dimensions = %dx%d, want %dx%d", config.Width, config.Height, tt.wantWidth, tt.wantHeight)
			}
			if len(got) > projectThumbnailOutputMaxBytes {
				t.Fatalf("normalized image is %d bytes", len(got))
			}
		})
	}
}

func TestCaptureProjectThumbnailRespectsForeignReplicaClaim(t *testing.T) {
	memory := store.NewMemoryStore()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "app", UID: types.UID("uid")}}
	id := identity{orgUUID: "org", workspaceUUID: "workspace"}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	if _, held, err := memory.TryClaimReplica(context.Background(), store.ReplicaClaim{
		Key: store.ThumbnailClaimKey(scope), Kind: store.ReplicaClaimKindThumbnail,
		ScopeKey: store.ReplicaClaimScopeKey(scope), OwnerReplica: "foreign-replica",
	}, projectThumbnailClaimTTL); err != nil || !held {
		t.Fatalf("seed foreign claim: held=%v err=%v", held, err)
	}
	inspector := &fakeProjectAssistantPreviewInspector{}
	s := &Server{store: memory, previewInspector: inspector}
	request := &projectThumbnailCaptureRequest{
		id: id, project: project, commitSHA: "commit", commitCreatedAt: time.Now().UTC(), commitOrder: "commit-name",
	}
	err := s.captureProjectThumbnail(context.Background(), projectThumbnailCaptureKey(id, project), request)
	if !errors.Is(err, errProjectThumbnailClaimHeld) {
		t.Fatalf("capture error = %v", err)
	}
	if inspector.calls != 0 {
		t.Fatalf("foreign claim still invoked inspector %d times", inspector.calls)
	}
}

type blockingProjectThumbnailInspector struct {
	mu      sync.Mutex
	active  int
	peak    int
	started chan struct{}
	release chan struct{}
	result  projectAssistantPreviewInspectionResult
}

func (f *blockingProjectThumbnailInspector) Health(context.Context) error { return nil }

func (f *blockingProjectThumbnailInspector) Inspect(ctx context.Context, _ projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.peak {
		f.peak = f.active
	}
	f.mu.Unlock()
	f.started <- struct{}{}
	select {
	case <-ctx.Done():
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		return projectAssistantPreviewInspectionResult{}, ctx.Err()
	case <-f.release:
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		return f.result, nil
	}
}

func TestProjectThumbnailCaptureUsesBoundedWorkerPool(t *testing.T) {
	imageData := testProjectThumbnailPNG(t, 640, 360)
	inspector := &blockingProjectThumbnailInspector{
		started: make(chan struct{}, 8), release: make(chan struct{}),
		result: projectAssistantPreviewInspectionResult{
			Status: "succeeded",
			Screenshot: &projectAssistantPreviewInspectionScreenshot{
				MIMEType: "image/png", Base64: base64.StdEncoding.EncodeToString(imageData),
			},
		},
	}
	thumbnailContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	memory := store.NewMemoryStore()
	s := &Server{
		store: memory, previewInspector: inspector, projectThumbnailContext: thumbnailContext,
		projectThumbnailCurrentness: func(context.Context, identity, *aiv1alpha1.Project, uint64) error {
			return nil
		},
		previewInspectionResolveURL: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "https://preview.example/", nil
		},
	}
	now := time.Now().UTC()
	for index := 0; index < 4; index++ {
		project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-%d", index), UID: types.UID(fmt.Sprintf("uid-%d", index))}}
		if !s.scheduleProjectThumbnailCapture(identity{orgUUID: "org", workspaceUUID: "workspace"}, project, projectThumbnailCommit{
			SHA: fmt.Sprintf("sha-%d", index), CreatedAt: now.Add(time.Duration(index) * time.Second), Order: fmt.Sprintf("commit-%d", index),
		}, uint64(index+1)) {
			t.Fatalf("capture %d was not scheduled", index)
		}
	}
	for index := 0; index < projectThumbnailWorkerCount; index++ {
		select {
		case <-inspector.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-inspector.started:
		t.Fatal("more than two browser captures ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	inspector.mu.Lock()
	peak := inspector.peak
	inspector.mu.Unlock()
	if peak != projectThumbnailWorkerCount {
		t.Fatalf("peak browser captures = %d, want %d", peak, projectThumbnailWorkerCount)
	}
	close(inspector.release)
}

func TestCaptureProjectThumbnailRejectsScreenshotWhenRuntimeRevisionTurnsStale(t *testing.T) {
	imageData := testProjectThumbnailPNG(t, 640, 360)
	inspector := &fakeProjectAssistantPreviewInspector{result: projectAssistantPreviewInspectionResult{
		Status: "succeeded",
		Screenshot: &projectAssistantPreviewInspectionScreenshot{
			MIMEType: "image/png", Base64: base64.StdEncoding.EncodeToString(imageData),
		},
	}}
	memory := store.NewMemoryStore()
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "app", UID: types.UID("uid")}}
	id := identity{orgUUID: "org", workspaceUUID: "workspace"}
	ctx, cancel := context.WithCancel(context.Background())
	checks := 0
	s := &Server{
		store: memory, previewInspector: inspector,
		previewInspectionResolveURL: func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
			return "https://preview.example/", nil
		},
		projectThumbnailCurrentness: func(context.Context, identity, *aiv1alpha1.Project, uint64) error {
			checks++
			if checks == 1 {
				return nil
			}
			cancel()
			return errors.New("runtime revision changed")
		},
	}
	request := &projectThumbnailCaptureRequest{
		id: id, project: project, commitSHA: "commit", commitCreatedAt: time.Now().UTC(), commitOrder: "commit-name", sourceRevision: 7,
	}
	key := projectThumbnailCaptureKey(id, project)
	s.projectThumbnailCaptures = map[string]*projectThumbnailCaptureRequest{key: request}
	if err := s.captureProjectThumbnail(ctx, key, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture error = %v, want cancellation after stale evidence", err)
	}
	if _, err := memory.GetProjectThumbnail(context.Background(), projectMessageScope(id.orgUUID, id.workspaceUUID, project)); !errors.Is(err, store.ErrProjectThumbnailNotFound) {
		t.Fatalf("stale screenshot was persisted: %v", err)
	}
	if inspector.calls != 1 || checks != 2 {
		t.Fatalf("inspector calls/currentness checks = %d/%d, want 1/2", inspector.calls, checks)
	}
}

func testProjectThumbnailPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: uint8((x + y) % 239), A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
