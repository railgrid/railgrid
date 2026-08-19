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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strings"
	"time"

	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectThumbnailCaptureTimeout = 40 * time.Second
	projectThumbnailRetryDelay     = 2 * time.Second
	projectThumbnailRetryCooldown  = time.Minute
	projectThumbnailInputMaxBytes  = 4 << 20
	projectThumbnailOutputMaxBytes = 1 << 20
	projectThumbnailWidth          = 640
	projectThumbnailHeight         = 360
	projectThumbnailMaxPixels      = 16 << 20
	projectThumbnailWorkerCount    = 2
	projectThumbnailQueueSize      = 32
	projectThumbnailClaimTTL       = time.Minute
	projectThumbnailVariant        = "card-640x360-v1"
)

var errProjectThumbnailClaimHeld = errors.New("project thumbnail capture is owned by another replica")

type ProjectThumbnailView struct {
	Available  bool   `json:"available"`
	Refreshing bool   `json:"refreshing,omitempty"`
	CommitSHA  string `json:"commitSHA,omitempty"`
	Revision   string `json:"revision,omitempty"`
}

type projectThumbnailCaptureRequest struct {
	id              identity
	project         *aiv1alpha1.Project
	commitSHA       string
	commitCreatedAt time.Time
	commitOrder     string
	sourceRevision  uint64
}

type projectThumbnailCommit struct {
	SHA       string
	CreatedAt time.Time
	Order     string
}

func projectThumbnailRequestMatches(left, right *projectThumbnailCaptureRequest) bool {
	return left != nil && right != nil &&
		left.commitSHA == right.commitSHA &&
		left.commitCreatedAt.Equal(right.commitCreatedAt) &&
		left.commitOrder == right.commitOrder &&
		left.sourceRevision == right.sourceRevision
}

func projectThumbnailCaptureKey(id identity, project *aiv1alpha1.Project) string {
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	return fmt.Sprintf("%s/%s/%s/%s", scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID)
}

func (s *Server) projectThumbnailStore() (store.ProjectThumbnailStore, bool) {
	thumbnailStore, ok := s.store.(store.ProjectThumbnailStore)
	return thumbnailStore, ok
}

func latestSuccessfulProjectCommit(repository *ProjectRepositoryView) projectThumbnailCommit {
	if repository == nil {
		return projectThumbnailCommit{}
	}
	var latest projectThumbnailCommit
	for _, commit := range repository.Commits {
		sha := strings.TrimSpace(commit.CommitSHA)
		order := strings.TrimSpace(commit.Name)
		if !strings.EqualFold(strings.TrimSpace(commit.Phase), "Succeeded") || sha == "" || commit.CreatedAt.IsZero() || order == "" {
			continue
		}
		if latest.SHA == "" || commit.CreatedAt.After(latest.CreatedAt) || (commit.CreatedAt.Equal(latest.CreatedAt) && order > latest.Order) {
			latest = projectThumbnailCommit{SHA: sha, CreatedAt: commit.CreatedAt, Order: order}
		}
	}
	return latest
}

// projectViewWithThumbnail reads the current derived image and schedules a
// refresh when repository history shows a newer successful commit. Scheduling
// happens only inside an authenticated project request because private preview
// capture needs the caller's one-use browser session handoff.
func (s *Server) projectViewWithThumbnail(ctx context.Context, view ProjectView, id identity, project *aiv1alpha1.Project) ProjectView {
	thumbnailStore, ok := s.projectThumbnailStore()
	if !ok || project == nil {
		return view
	}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	thumbnail, err := thumbnailStore.GetProjectThumbnailMetadata(ctx, scope)
	if err == nil {
		view.Thumbnail = &ProjectThumbnailView{
			Available: true,
			CommitSHA: thumbnail.CommitSHA,
			Revision:  thumbnail.SHA256,
		}
	} else if !errors.Is(err, store.ErrProjectThumbnailNotFound) {
		klog.V(1).Infof("read project thumbnail for %s: %v", project.Name, err)
		return view
	}
	latestCommit := latestSuccessfulProjectCommit(view.Repository)
	if latestCommit.SHA == "" || (view.Thumbnail != nil && view.Thumbnail.CommitSHA == latestCommit.SHA && thumbnail.Variant == projectThumbnailVariant) {
		return view
	}
	if view.Thumbnail == nil {
		view.Thumbnail = &ProjectThumbnailView{}
	}
	view.Thumbnail.Refreshing = s.scheduleProjectThumbnailCapture(id, project, latestCommit, view.SourceRevision)
	return view
}

func (s *Server) scheduleProjectThumbnailCapture(id identity, project *aiv1alpha1.Project, commit projectThumbnailCommit, sourceRevision uint64) bool {
	if s == nil || project == nil || strings.TrimSpace(commit.SHA) == "" || commit.CreatedAt.IsZero() || strings.TrimSpace(commit.Order) == "" || sourceRevision == 0 {
		return false
	}
	keyString := projectThumbnailCaptureKey(id, project)
	request := &projectThumbnailCaptureRequest{
		id: id, project: project.DeepCopy(), commitSHA: strings.TrimSpace(commit.SHA),
		commitCreatedAt: commit.CreatedAt, commitOrder: strings.TrimSpace(commit.Order),
		sourceRevision: sourceRevision,
	}
	s.mu.Lock()
	if s.projectThumbnailCaptures == nil {
		s.projectThumbnailCaptures = map[string]*projectThumbnailCaptureRequest{}
	}
	if retryAfter := s.projectThumbnailFailures[keyString]; retryAfter.After(time.Now()) {
		s.mu.Unlock()
		return false
	}
	_, running := s.projectThumbnailCaptures[keyString]
	s.projectThumbnailCaptures[keyString] = request
	ctx := s.projectThumbnailContext
	if ctx == nil {
		ctx = context.Background()
	}
	if running {
		s.mu.Unlock()
		return true
	}
	if s.projectThumbnailQueue == nil {
		s.projectThumbnailQueue = make(chan string, projectThumbnailQueueSize)
	}
	startWorkers := !s.projectThumbnailWorkersUp
	s.projectThumbnailWorkersUp = true
	queue := s.projectThumbnailQueue
	select {
	case queue <- keyString:
	default:
		delete(s.projectThumbnailCaptures, keyString)
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	if startWorkers {
		for range projectThumbnailWorkerCount {
			go s.projectThumbnailWorker(ctx, queue)
		}
	}
	return true
}

func (s *Server) projectThumbnailWorker(ctx context.Context, queue <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-queue:
			s.runProjectThumbnailCapture(ctx, key)
		}
	}
}

func (s *Server) forgetProjectThumbnailCapture(id identity, project *aiv1alpha1.Project) {
	if s == nil || project == nil {
		return
	}
	key := projectThumbnailCaptureKey(id, project)
	s.mu.Lock()
	delete(s.projectThumbnailCaptures, key)
	delete(s.projectThumbnailFailures, key)
	s.mu.Unlock()
}

func (s *Server) runProjectThumbnailCapture(parent context.Context, key string) {
	for {
		s.mu.Lock()
		request := s.projectThumbnailCaptures[key]
		s.mu.Unlock()
		if request == nil {
			return
		}
		err := s.captureProjectThumbnail(parent, key, request)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errProjectThumbnailClaimHeld) {
			klog.V(1).Infof("capture project thumbnail for %s: %v", request.project.Name, err)
		}
		s.mu.Lock()
		current := s.projectThumbnailCaptures[key]
		if projectThumbnailRequestMatches(current, request) {
			delete(s.projectThumbnailCaptures, key)
			if err == nil {
				delete(s.projectThumbnailFailures, key)
			} else if !errors.Is(err, context.Canceled) && !errors.Is(err, errProjectThumbnailClaimHeld) {
				if s.projectThumbnailFailures == nil {
					s.projectThumbnailFailures = map[string]time.Time{}
				}
				s.projectThumbnailFailures[key] = time.Now().Add(projectThumbnailRetryCooldown)
			}
			current = nil
		}
		s.mu.Unlock()
		if current == nil {
			return
		}
	}
}

func (s *Server) captureProjectThumbnail(parent context.Context, key string, request *projectThumbnailCaptureRequest) error {
	thumbnailStore, ok := s.projectThumbnailStore()
	if !ok {
		return errors.New("project thumbnail store is unavailable")
	}
	scope := projectMessageScope(request.id.orgUUID, request.id.workspaceUUID, request.project)
	owner := s.projectThumbnailClaimOwner()
	claimCtx, claimCancel := context.WithTimeout(parent, 5*time.Second)
	_, held, err := s.store.TryClaimReplica(claimCtx, store.ReplicaClaim{
		Key: store.ThumbnailClaimKey(scope), Kind: store.ReplicaClaimKindThumbnail,
		ScopeKey: store.ReplicaClaimScopeKey(scope), OwnerReplica: owner, Detail: request.commitSHA,
	}, projectThumbnailClaimTTL)
	claimCancel()
	if err != nil {
		return fmt.Errorf("claim project thumbnail capture: %w", err)
	}
	if !held {
		return errProjectThumbnailClaimHeld
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if releaseErr := s.store.ReleaseReplicaClaim(releaseCtx, store.ThumbnailClaimKey(scope), owner); releaseErr != nil {
			klog.V(1).Infof("release project thumbnail claim for %s: %v", request.project.Name, releaseErr)
		}
	}()

	ctx, cancel := context.WithTimeout(parent, projectThumbnailCaptureTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if currentErr := s.requireProjectThumbnailCurrentRevision(ctx, request); currentErr != nil {
			lastErr = currentErr
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(projectThumbnailRetryDelay):
				}
			}
			continue
		}
		result, inspectErr := s.inspectProjectDevelopmentPreviewResult(ctx, projectAssistantToolCallRequest{
			Identity: request.id,
			Project:  request.project,
			Arguments: map[string]any{
				"path": "/",
			},
		}, true)
		if inspectErr == nil && result.Screenshot != nil && result.ScreenshotStatus == projectAssistantPreviewScreenshotCaptured {
			if currentErr := s.requireProjectThumbnailCurrentRevision(ctx, request); currentErr != nil {
				lastErr = currentErr
				if attempt < 2 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(projectThumbnailRetryDelay):
					}
				}
				continue
			}
			data, decodeErr := base64.StdEncoding.DecodeString(result.Screenshot.Base64)
			if decodeErr != nil {
				return fmt.Errorf("decode preview screenshot: %w", decodeErr)
			}
			if len(data) == 0 || len(data) > projectThumbnailInputMaxBytes {
				return fmt.Errorf("preview screenshot size %d is outside the supported range", len(data))
			}
			data, normalizeErr := normalizeProjectThumbnailPNG(data)
			if normalizeErr != nil {
				return normalizeErr
			}
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			s.mu.Lock()
			current := s.projectThumbnailCaptures[key]
			isCurrent := projectThumbnailRequestMatches(current, request)
			s.mu.Unlock()
			if !isCurrent {
				return nil
			}
			return thumbnailStore.PutProjectThumbnail(ctx, scope, store.ProjectThumbnail{
				CommitSHA: request.commitSHA, CommitCreatedAt: request.commitCreatedAt, CommitOrder: request.commitOrder,
				Variant: projectThumbnailVariant, ContentType: "image/png", SHA256: digest, Data: data, CapturedAt: time.Now().UTC(),
			})
		}
		if inspectErr != nil {
			lastErr = inspectErr
		} else {
			lastErr = fmt.Errorf("preview screenshot was not captured: %s", firstNonEmpty(result.Summary, result.ScreenshotStatus))
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(projectThumbnailRetryDelay):
			}
		}
	}
	return lastErr
}

func (s *Server) requireProjectThumbnailCurrentRevision(ctx context.Context, request *projectThumbnailCaptureRequest) error {
	if s.projectThumbnailCurrentness != nil {
		return s.projectThumbnailCurrentness(ctx, request.id, request.project, request.sourceRevision)
	}
	if s.workspaces == nil {
		return errors.New("project workspace store is unavailable")
	}
	scope := projectWorkspaceScope(request.id, request.project)
	current, err := s.workspaces.SourceRevision(ctx, scope)
	if err != nil {
		return fmt.Errorf("read current workspace revision: %w", err)
	}
	if current != request.sourceRevision {
		return fmt.Errorf("workspace revision changed from %d to %d before thumbnail capture", request.sourceRevision, current)
	}
	dirty, err := s.workspaces.UncommittedPaths(ctx, scope)
	if err != nil {
		return fmt.Errorf("read workspace commit state: %w", err)
	}
	if len(dirty) > 0 {
		return errors.New("workspace has uncommitted changes; preview does not exactly represent the latest successful commit")
	}
	c, err := s.clientFor(request.id)
	if err != nil {
		return err
	}
	target, err := s.projectDevelopmentTarget(ctx, c, request.project, request.id)
	if err != nil {
		return err
	}
	components := target.sortedComponents()
	if len(components) == 0 {
		components = []string{""}
	}
	for _, component := range components {
		body, status, getErr := s.dataPlaneGet(ctx, request.id, target.dataPlaneRefFor(component), dataPlaneVerbProcess, 16<<10)
		if getErr != nil {
			return getErr
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("component %s process status returned %d", firstNonEmpty(component, "default"), status)
		}
		var process projectAssistantProcessStatus
		if err := json.Unmarshal(body, &process); err != nil {
			return fmt.Errorf("decode component %s process status: %w", firstNonEmpty(component, "default"), err)
		}
		if process.SourceRevision != request.sourceRevision {
			return fmt.Errorf("component %s is serving workspace revision %d, want %d", firstNonEmpty(component, "default"), process.SourceRevision, request.sourceRevision)
		}
	}
	return nil
}

func (s *Server) projectThumbnailClaimOwner() string {
	if routing := s.routing(); routing != nil && strings.TrimSpace(routing.id) != "" {
		return routing.id
	}
	return defaultReplicaIdentity()
}

func normalizeProjectThumbnailPNG(data []byte) ([]byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode preview screenshot config: %w", err)
	}
	if format != "png" && format != "jpeg" {
		return nil, fmt.Errorf("unsupported preview screenshot format %q", format)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > projectThumbnailMaxPixels {
		return nil, fmt.Errorf("preview screenshot dimensions %dx%d are outside the supported range", config.Width, config.Height)
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode preview screenshot: %w", err)
	}
	scale := math.Min(1, math.Min(float64(projectThumbnailWidth)/float64(config.Width), float64(projectThumbnailHeight)/float64(config.Height)))
	width := max(1, int(math.Round(float64(config.Width)*scale)))
	height := max(1, int(math.Round(float64(config.Height)*scale)))
	resized := resizeProjectThumbnailBilinear(source, width, height)
	var out bytes.Buffer
	if err := png.Encode(&out, resized); err != nil {
		return nil, fmt.Errorf("encode project thumbnail png: %w", err)
	}
	if out.Len() == 0 || out.Len() > projectThumbnailOutputMaxBytes {
		return nil, fmt.Errorf("normalized project thumbnail size %d is outside the supported range", out.Len())
	}
	return out.Bytes(), nil
}

func resizeProjectThumbnailBilinear(source image.Image, width, height int) *image.RGBA {
	bounds := source.Bounds()
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	if bounds.Dx() == width && bounds.Dy() == height && bounds.Min.X == 0 && bounds.Min.Y == 0 {
		for y := range height {
			for x := range width {
				destination.Set(x, y, source.At(x, y))
			}
		}
		return destination
	}
	for y := range height {
		sy := (float64(y)+0.5)*float64(bounds.Dy())/float64(height) + float64(bounds.Min.Y) - 0.5
		sy = math.Max(float64(bounds.Min.Y), math.Min(float64(bounds.Max.Y-1), sy))
		y0 := int(math.Floor(sy))
		y1 := min(bounds.Max.Y-1, y0+1)
		fy := sy - float64(y0)
		for x := range width {
			sx := (float64(x)+0.5)*float64(bounds.Dx())/float64(width) + float64(bounds.Min.X) - 0.5
			sx = math.Max(float64(bounds.Min.X), math.Min(float64(bounds.Max.X-1), sx))
			x0 := int(math.Floor(sx))
			x1 := min(bounds.Max.X-1, x0+1)
			fx := sx - float64(x0)
			destination.SetRGBA(x, y, bilinearProjectThumbnailColor(source, x0, y0, x1, y1, fx, fy))
		}
	}
	return destination
}

func bilinearProjectThumbnailColor(source image.Image, x0, y0, x1, y1 int, fx, fy float64) color.RGBA {
	r00, g00, b00, a00 := source.At(x0, y0).RGBA()
	r10, g10, b10, a10 := source.At(x1, y0).RGBA()
	r01, g01, b01, a01 := source.At(x0, y1).RGBA()
	r11, g11, b11, a11 := source.At(x1, y1).RGBA()
	interpolate := func(v00, v10, v01, v11 uint32) uint8 {
		top := float64(v00)*(1-fx) + float64(v10)*fx
		bottom := float64(v01)*(1-fx) + float64(v11)*fx
		return uint8(math.Round((top*(1-fy) + bottom*fy) / 257))
	}
	return color.RGBA{
		R: interpolate(r00, r10, r01, r11), G: interpolate(g00, g10, g01, g11),
		B: interpolate(b00, b10, b01, b11), A: interpolate(a00, a10, a01, a11),
	}
}

func (s *Server) getProjectThumbnail(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	thumbnailStore, ok := s.projectThumbnailStore()
	if !ok {
		writeStatus(w, http.StatusNotFound, "NotFound", "project thumbnail is not available")
		return
	}
	thumbnail, err := thumbnailStore.GetProjectThumbnail(r.Context(), projectMessageScope(id.orgUUID, id.workspaceUUID, project))
	if errors.Is(err, store.ErrProjectThumbnailNotFound) {
		writeStatus(w, http.StatusNotFound, "NotFound", "project thumbnail is not available")
		return
	}
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	etag := `"` + thumbnail.SHA256 + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", thumbnail.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(thumbnail.Data)))
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", thumbnail.CapturedAt.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(thumbnail.Data)
}
