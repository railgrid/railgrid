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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func projectAssistantSandboxTargetFromTemplate(info projectTemplateInfo, name string) projectDevelopmentSyncTargetInfo {
	return projectDevelopmentSyncTargetInfo{
		EnvironmentName:    projectAssistantRunSandboxEnvironment,
		BindingName:        projectAssistantRunSandboxBinding,
		Provider:           infraDataPlaneProvider,
		ResourceName:       name,
		Resource:           projectAssistantRunSandboxResource,
		Kind:               projectAssistantRunSandboxKind,
		APIVersion:         projectAssistantRunSandboxAPIVersion,
		Components:         info.Components,
		PreviewAccessModes: nil,
	}
}

func (s *Server) projectAssistantRunSandboxClient() projectAssistantSandboxClient {
	if s != nil && s.runSandboxClientFactory != nil {
		return s.runSandboxClientFactory(s)
	}
	return projectAssistantDataPlaneSandboxClient{server: s}
}

func (s *Server) setupProjectAssistantRunSandbox(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState, checkpoint *projectAssistantSandboxCheckpoint) (*projectAssistantRunSandbox, func(), error) {
	if s != nil && s.runSandboxSetupFactory != nil {
		return s.runSandboxSetupFactory(ctx, req, runState, checkpoint)
	}
	if checkpoint != nil {
		return s.attachProjectAssistantRunSandbox(ctx, req, runState, checkpoint)
	}
	return s.ensureProjectAssistantRunSandbox(ctx, req, runState)
}

func (s *Server) ensureProjectAssistantRunSandbox(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) (*projectAssistantRunSandbox, func(), error) {
	eligibility := s.ResolveCodingSandboxEligibility(ctx, req.Identity, req.WorkspaceScope)
	if !eligibility.Eligible {
		return nil, func() {}, nil
	}
	if s == nil || req.Client == nil || req.Project == nil || req.Workspace == nil {
		return nil, nil, errors.New("run sandbox requires project client, project, and workspace store")
	}
	runID := projectAssistantRunID(req)
	if runID == "" {
		return nil, nil, errors.New("run sandbox requires a durable assistant run ID")
	}
	name := projectAssistantRunSandboxName(req.WorkspaceScope, req.Project, runID)
	manager := s.projectAssistantSandboxManager()
	release, err := manager.acquire(projectAssistantRunSandboxTenantKey(req.Identity, req.WorkspaceScope), name, runID)
	if err != nil {
		return nil, nil, err
	}
	rollback := func() { release() }
	templateName := strings.TrimSpace(getenv(projectAssistantRunSandboxTemplateEnv))
	if templateName == "" {
		templateName = projectAssistantRunSandboxDefaultTemplate
	}
	if templateName != projectAssistantRunSandboxDefaultTemplate {
		rollback()
		return nil, nil, fmt.Errorf("run sandbox requires template %q", projectAssistantRunSandboxDefaultTemplate)
	}
	info, err := fetchProjectTemplate(ctx, req.Client, templateName)
	if err != nil {
		rollback()
		return nil, nil, fmt.Errorf("resolve run sandbox template %q: %w", templateName, err)
	}
	if len(info.Components) == 0 {
		rollback()
		return nil, nil, fmt.Errorf("run sandbox template %q has no development components", templateName)
	}
	if err := s.enforceProjectAssistantRunSandboxQuota(ctx, req.Client, name); err != nil {
		rollback()
		return nil, nil, err
	}
	createdInstance, err := s.ensureProjectAssistantRunSandboxInstance(ctx, req.Client, req.Project, name, templateName)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	// A reused project cache is not ours to delete until its durable claim has
	// succeeded. A newly created instance is safe to remove on an early claim
	// failure; an existing cache may belong to another run.
	cleanupInstance := createdInstance
	defer func() {
		if cleanupInstance {
			_ = req.Client.Resource(runSandboxInstancesResource, "").Delete(context.Background(), name, metav1.DeleteOptions{})
		}
	}()
	hardExpiry, err := s.claimProjectAssistantRunSandboxInstance(ctx, req.Client, req.MessageScope, name, runID)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	manager.setExpiry(projectAssistantRunSandboxTenantKey(req.Identity, req.WorkspaceScope), name, runID, hardExpiry)
	cleanupInstance = true
	target := projectAssistantSandboxTargetFromTemplate(info, name)
	component, ok := target.Components["workspace"]
	if !ok || path.Clean(strings.TrimSpace(component.WorkspacePath)) != "." {
		rollback()
		return nil, nil, fmt.Errorf("run sandbox template %q does not declare the workspace component", templateName)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, projectAssistantRunSandboxReadyTimeout)
	defer readyCancel()
	if err := s.waitForProjectAssistantRunSandboxInstanceReady(readyCtx, req.Client, target); err != nil {
		rollback()
		return nil, nil, fmt.Errorf("wait for run sandbox instance %q: %w", name, err)
	}
	snapshot, err := s.projectWorkspaceSyncFiles(readyCtx, req.WorkspaceScope)
	if err != nil {
		rollback()
		return nil, nil, fmt.Errorf("snapshot run sandbox source: %w", err)
	}
	client := s.projectAssistantRunSandboxClient()
	seedDigest := projectSandboxSyncDigest(snapshot.Files)
	var remoteRevision uint64
	var remoteDigest string
	if err := retryProjectAssistantRunSandboxSeed(readyCtx, projectAssistantRunSandboxReadyTimeout, projectAssistantRunSandboxReadyPoll, func(seedCtx context.Context) error {
		var reconcileErr error
		remoteRevision, remoteDigest, reconcileErr = reconcileProjectAssistantRunSandboxSource(seedCtx, client, req.Identity, target.dataPlaneRefFor("workspace"), snapshot.Files, seedDigest)
		return reconcileErr
	}); err != nil {
		rollback()
		return nil, nil, fmt.Errorf("seed run sandbox: %w", err)
	}
	// The worker checkpoint is the durable remote baseline.  It survives
	// coordinator restarts on the sandbox workspace volume and lets /diff
	// return before-digests while App Studio reads complete after-bytes before
	// applying an atomic FileStore transaction.
	baseline, err := client.Workspace(readyCtx, req.Identity, target.dataPlaneRefFor("workspace"), projectAssistantSandboxWorkspaceRequest{
		Action: "checkpoint", CheckpointAction: "create",
		SourceRevision: remoteRevision, SourceDigest: remoteDigest,
	})
	if err != nil {
		rollback()
		return nil, nil, fmt.Errorf("create run sandbox baseline: %w", err)
	}
	if strings.TrimSpace(baseline.CheckpointID) == "" {
		rollback()
		return nil, nil, fmt.Errorf("%w: run sandbox baseline checkpoint ID is empty", errProjectAssistantRunSandboxConflict)
	}
	if baseline.SourceRevision != remoteRevision || !sandboxDigestEqual(baseline.SourceDigest, remoteDigest) {
		rollback()
		return nil, nil, fmt.Errorf("%w: run sandbox baseline does not match the reconciled remote source fence", errProjectAssistantRunSandboxConflict)
	}
	now := time.Now().UTC()
	sandbox := &projectAssistantRunSandbox{
		server: s, client: client, id: req.Identity,
		project: req.Project.DeepCopy(), scope: req.WorkspaceScope, target: target,
		instance: projectAssistantSandboxInstance{APIVersion: target.APIVersion, Kind: target.Kind, Resource: target.Resource, Name: target.ResourceName},
		runState: runState,
		metadata: projectAssistantRunSandboxMetadata{
			Version: 3, Status: "active", RunID: runID,
			OrgUUID: req.WorkspaceScope.OrgUUID, WorkspaceUUID: req.WorkspaceScope.WorkspaceUUID,
			ProjectName: req.WorkspaceScope.ProjectName, ProjectUID: req.WorkspaceScope.ProjectUID,
			Template:            templateName,
			ProviderExportPath:  eligibility.ProviderExportPath,
			TransportGeneration: eligibility.TransportGeneration,
			Instance:            projectAssistantSandboxInstance{APIVersion: target.APIVersion, Kind: target.Kind, Resource: target.Resource, Name: target.ResourceName},
			SourceRevision:      snapshot.SourceRevision,
			SourceDigest:        seedDigest,
			RemoteRevision:      remoteRevision,
			RemoteDigest:        remoteDigest,
			RemoteCheckpointID:  baseline.CheckpointID,
			CheckpointRevision:  remoteRevision,
			CheckpointDigest:    remoteDigest,
			CacheGeneration:     runID,
			CreatedAt:           now, LastActivityAt: now,
			IdleExpiresAt: minProjectAssistantRunSandboxExpiry(now.Add(projectAssistantRunSandboxIdleTTL), hardExpiry), HardExpiresAt: hardExpiry,
		},
	}
	if runState != nil {
		runState.SetSandbox(sandbox)
	}
	cleanupInstance = false
	return sandbox, release, nil
}

func (s *Server) attachProjectAssistantRunSandbox(
	ctx context.Context,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	checkpoint *projectAssistantSandboxCheckpoint,
) (*projectAssistantRunSandbox, func(), error) {
	eligibility := s.ResolveCodingSandboxEligibility(ctx, req.Identity, req.WorkspaceScope)
	if !eligibility.Eligible {
		return nil, func() {}, nil
	}
	// Checkpoints created before run sandboxes were enabled intentionally have
	// no sandbox metadata. Resume them on the legacy execution path instead of
	// turning a rollout into an incompatibility for already-suspended runs.
	if checkpoint == nil {
		return nil, func() {}, nil
	}
	if s == nil || req.Client == nil || req.Project == nil {
		return nil, nil, errors.New("resuming a run sandbox requires project client, project, and checkpoint metadata")
	}
	metadata := checkpoint.Metadata
	if strings.TrimSpace(metadata.ProviderExportPath) == "" || strings.TrimSpace(metadata.TransportGeneration) == "" {
		return nil, nil, fmt.Errorf("%w: checkpoint does not contain provider transport identity", errProjectAssistantRunSandboxConflict)
	}
	if metadata.ProviderExportPath != eligibility.ProviderExportPath || metadata.TransportGeneration != eligibility.TransportGeneration {
		return nil, nil, fmt.Errorf("%w: coding sandbox provider export or transport generation changed", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.RunID) == "" || metadata.RunID != projectAssistantRunID(req) {
		return nil, nil, fmt.Errorf("%w: checkpoint run identity does not match", errProjectAssistantRunSandboxConflict)
	}
	if metadata.OrgUUID != req.WorkspaceScope.OrgUUID || metadata.WorkspaceUUID != req.WorkspaceScope.WorkspaceUUID || metadata.ProjectUID != req.WorkspaceScope.ProjectUID {
		return nil, nil, fmt.Errorf("%w: checkpoint tenant or project identity does not match", errProjectAssistantRunSandboxConflict)
	}
	if metadata.HardExpiresAt.IsZero() || time.Now().UTC().After(metadata.HardExpiresAt) {
		return nil, nil, fmt.Errorf("%w: sandbox hard lifetime has expired", errProjectAssistantRunSandboxConflict)
	}
	if !metadata.IdleExpiresAt.IsZero() && time.Now().UTC().After(metadata.IdleExpiresAt) {
		return nil, nil, fmt.Errorf("%w: sandbox idle lifetime has expired", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.RemoteCheckpointID) == "" {
		return nil, nil, fmt.Errorf("%w: checkpoint does not contain a durable remote workspace baseline", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.CacheGeneration) == "" {
		return nil, nil, fmt.Errorf("%w: checkpoint does not contain a cache generation fence", errProjectAssistantRunSandboxConflict)
	}
	templateName := strings.TrimSpace(metadata.Template)
	if templateName != projectAssistantRunSandboxDefaultTemplate {
		return nil, nil, fmt.Errorf("%w: checkpoint template must be %q", errProjectAssistantRunSandboxConflict, projectAssistantRunSandboxDefaultTemplate)
	}
	info, err := fetchProjectTemplate(ctx, req.Client, templateName)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve checkpoint sandbox template %q: %w", templateName, err)
	}
	target := projectAssistantSandboxTargetFromTemplate(info, metadata.Instance.Name)
	if target.ResourceName == "" {
		return nil, nil, fmt.Errorf("%w: checkpoint instance name is empty", errProjectAssistantRunSandboxConflict)
	}
	manager := s.projectAssistantSandboxManager()
	release, err := manager.acquire(projectAssistantRunSandboxTenantKey(req.Identity, req.WorkspaceScope), target.ResourceName, metadata.RunID)
	if err != nil {
		return nil, nil, err
	}
	rollback := func() { release() }
	component, ok := target.Components["workspace"]
	if !ok || path.Clean(strings.TrimSpace(component.WorkspacePath)) != "." {
		rollback()
		return nil, nil, fmt.Errorf("%w: checkpoint template does not declare the workspace component", errProjectAssistantRunSandboxConflict)
	}
	if err := s.enforceProjectAssistantRunSandboxQuota(ctx, req.Client, target.ResourceName); err != nil {
		rollback()
		return nil, nil, err
	}
	createdInstance, err := s.ensureProjectAssistantRunSandboxInstance(ctx, req.Client, req.Project, target.ResourceName, templateName)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	cleanupInstance := createdInstance
	defer func() {
		if cleanupInstance {
			_ = req.Client.Resource(runSandboxInstancesResource, "").Delete(context.Background(), target.ResourceName, metav1.DeleteOptions{})
		}
	}()
	instance, err := req.Client.Resource(runSandboxInstancesResource, "").Get(ctx, target.ResourceName, metav1.GetOptions{})
	if err != nil {
		rollback()
		return nil, nil, fmt.Errorf("get checkpoint run sandbox instance %q: %w", target.ResourceName, err)
	}
	annotations := instance.GetAnnotations()
	if strings.TrimSpace(annotations[projectAssistantRunSandboxCacheGeneration]) != metadata.CacheGeneration || strings.TrimSpace(annotations[projectAssistantRunSandboxClaimOwner]) != metadata.RunID {
		rollback()
		return nil, nil, fmt.Errorf("%w: checkpoint cache generation or claim is no longer current", errProjectAssistantRunSandboxConflict)
	}
	hardExpiry, err := s.claimProjectAssistantRunSandboxInstance(ctx, req.Client, req.MessageScope, target.ResourceName, metadata.RunID)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	manager.setExpiry(projectAssistantRunSandboxTenantKey(req.Identity, req.WorkspaceScope), target.ResourceName, metadata.RunID, hardExpiry)
	cleanupInstance = true
	if err := s.waitForProjectAssistantRunSandboxInstanceReady(ctx, req.Client, target); err != nil {
		rollback()
		return nil, nil, fmt.Errorf("wait for checkpoint run sandbox instance %q: %w", target.ResourceName, err)
	}
	sandbox := &projectAssistantRunSandbox{
		server: s, client: s.projectAssistantRunSandboxClient(), id: req.Identity,
		project: req.Project.DeepCopy(), scope: req.WorkspaceScope, target: target,
		instance: projectAssistantSandboxInstance{APIVersion: target.APIVersion, Kind: target.Kind, Resource: target.Resource, Name: target.ResourceName}, runState: runState, metadata: metadata,
	}
	sandbox.metadata.HardExpiresAt = hardExpiry
	sandbox.metadata.IdleExpiresAt = minProjectAssistantRunSandboxExpiry(time.Now().UTC().Add(projectAssistantRunSandboxIdleTTL), hardExpiry)
	sandbox.metadata.LastActivityAt = time.Now().UTC()
	if runState != nil {
		runState.SetSandbox(sandbox)
	}
	cleanupInstance = false
	return sandbox, release, nil
}

type projectAssistantRunSandboxInstanceStatusGetter func(context.Context) (*unstructured.Unstructured, error)

// projectAssistantRunSandboxInstanceReadiness mirrors the fields consumed by
// Infrastructure's data-plane resolver. An ordinary Instance may exist before
// its development overlay publishes these references, so refs are a readiness
// fence rather than an advisory status.
func projectAssistantRunSandboxInstanceReadiness(obj *unstructured.Unstructured, components map[string]projectTemplateComponent) (ready, terminal bool, reason string) {
	if obj == nil {
		return false, false, "instance has not been observed"
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if strings.EqualFold(strings.TrimSpace(phase), "failed") || strings.EqualFold(strings.TrimSpace(phase), "error") {
		message, _, _ := unstructured.NestedString(obj.Object, "status", "message")
		if strings.TrimSpace(message) == "" {
			message = "instance reported a failed phase"
		}
		return false, true, message
	}

	rawGeneration, found, err := unstructured.NestedFieldNoCopy(obj.Object, "metadata", "generation")
	if err != nil || !found {
		return false, false, "metadata.generation is not positive"
	}
	generation, ok := projectAssistantRunSandboxObservedGenerationValue(rawGeneration)
	if !ok || generation <= 0 {
		return false, false, "metadata.generation is not positive"
	}
	statusObserved, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "observedGeneration")
	if err != nil || !found {
		return false, false, "status.observedGeneration is missing"
	}
	observedGeneration, ok := projectAssistantRunSandboxObservedGenerationValue(statusObserved)
	if !ok || observedGeneration != generation {
		return false, false, "status.observedGeneration does not match metadata.generation"
	}
	if phase != "Ready" {
		return false, false, "status.phase is not Ready"
	}

	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false, false, "status.conditions is missing"
	}
	readyCondition := false
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		if strings.EqualFold(strings.TrimSpace(conditionType), "valid") {
			status, _ := condition["status"].(string)
			if !strings.EqualFold(strings.TrimSpace(status), "true") {
				reasonText, _ := condition["reason"].(string)
				message, _ := condition["message"].(string)
				detail := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(reasonText), strings.TrimSpace(message)}, ": "))
				if detail == "" {
					detail = "instance values are not valid"
				}
				return false, true, detail
			}
		}
		if conditionType != "Ready" {
			continue
		}
		conditionObserved, ok := projectAssistantRunSandboxObservedGenerationValue(condition["observedGeneration"])
		if !ok || conditionObserved != generation {
			return false, false, "status Ready condition is not current"
		}
		status, _ := condition["status"].(string)
		readyCondition = status == "True"
		break
	}
	if !readyCondition {
		return false, false, "status Ready condition is not true"
	}
	runtimeNamespace, _, _ := unstructured.NestedString(obj.Object, "status", "runtimeNamespace")
	if strings.TrimSpace(runtimeNamespace) == "" {
		return false, false, "status.runtimeNamespace is empty"
	}
	networkPhase, found, err := unstructured.NestedString(obj.Object, "status", "farosNetworkPhase")
	if err != nil || !found || networkPhase != "runtime" {
		return false, false, "status.farosNetworkPhase is not runtime"
	}
	secretName, _, _ := unstructured.NestedString(obj.Object, "status", "controlSecretRef", "name")
	if strings.TrimSpace(secretName) == "" {
		return false, false, "status.controlSecretRef.name is empty"
	}
	componentNames := make([]string, 0, len(components))
	for name := range components {
		componentNames = append(componentNames, name)
	}
	sort.Strings(componentNames)
	for _, component := range componentNames {
		serviceName, _, _ := unstructured.NestedString(obj.Object, "status", "components", component, "controlServiceRef", "name")
		if strings.TrimSpace(serviceName) == "" {
			return false, false, fmt.Sprintf("status.components.%s.controlServiceRef.name is empty", component)
		}
		value, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "components", component, "ready")
		if err == nil && found {
			componentReady, ok := value.(bool)
			if !ok || !componentReady {
				return false, false, fmt.Sprintf("status.components.%s.ready is not true", component)
			}
		}
	}
	return true, false, ""
}

// projectAssistantRunSandboxObservedGenerationValue accepts the numeric forms
// that Kubernetes clients can produce for unstructured status fields. This
// mirrors Infrastructure's dataplane readiness predicate without importing
// the standalone provider module.
func projectAssistantRunSandboxObservedGenerationValue(value any) (int64, bool) {
	switch value := value.(type) {
	case int64:
		return value, true
	case int32:
		return int64(value), true
	case int:
		return int64(value), true
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint:
		return int64(value), true
	case float64:
		if value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

func waitForProjectAssistantRunSandboxInstanceReady(ctx context.Context, timeout, poll time.Duration, components map[string]projectTemplateComponent, get projectAssistantRunSandboxInstanceStatusGetter) error {
	if get == nil {
		return errors.New("run sandbox readiness getter is not configured")
	}
	if timeout <= 0 {
		timeout = projectAssistantRunSandboxReadyTimeout
	}
	if poll <= 0 {
		poll = projectAssistantRunSandboxReadyPoll
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	lastReason := "instance is not ready"
	for {
		obj, err := get(waitCtx)
		if err == nil {
			ready, terminal, reason := projectAssistantRunSandboxInstanceReadiness(obj, components)
			if ready {
				return nil
			}
			if terminal {
				return fmt.Errorf("instance is not ready: %s", reason)
			}
			if strings.TrimSpace(reason) != "" {
				lastReason = reason
			}
		} else if apierrors.IsNotFound(err) {
			lastReason = "instance has not been observed"
		} else {
			if ctx.Err() != nil {
				return fmt.Errorf("wait for run sandbox instance: %w", ctx.Err())
			}
			if waitCtx.Err() != nil {
				return fmt.Errorf("instance did not become ready within %s: %s", timeout, lastReason)
			}
			return fmt.Errorf("get run sandbox instance status: %w", err)
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return fmt.Errorf("wait for run sandbox instance: %w", ctx.Err())
			}
			return fmt.Errorf("instance did not become ready within %s: %s", timeout, lastReason)
		case <-ticker.C:
		}
	}
}

func (s *Server) waitForProjectAssistantRunSandboxInstanceReady(ctx context.Context, c *asclient.Client, target projectDevelopmentSyncTargetInfo) error {
	if c == nil {
		return errors.New("project client is not configured")
	}
	return waitForProjectAssistantRunSandboxInstanceReady(ctx, projectAssistantRunSandboxReadyTimeout, projectAssistantRunSandboxReadyPoll, target.Components, func(getCtx context.Context) (*unstructured.Unstructured, error) {
		return c.Resource(runSandboxInstancesResource, "").Get(getCtx, target.ResourceName, metav1.GetOptions{})
	})
}

// reconcileProjectAssistantRunSandboxSource keeps the FileStore revision and
// the worker's manifest revision in separate monotonic domains. A warm worker
// whose digest already matches needs no write; otherwise the worker advances
// its own current revision while applying the complete authoritative snapshot.
func reconcileProjectAssistantRunSandboxSource(
	ctx context.Context,
	client projectAssistantSandboxClient,
	id identity,
	ref dataPlaneRef,
	files []projectSandboxSyncFile,
	localDigest string,
) (uint64, string, error) {
	if client == nil {
		return 0, "", errors.New("run sandbox client is not configured")
	}
	listed, err := client.Workspace(ctx, id, ref, projectAssistantSandboxWorkspaceRequest{Action: "list", Path: ".", Limit: workspace.MaxListLimit})
	if err != nil {
		return 0, "", err
	}
	if listed.SourceRevision > 0 && strings.TrimSpace(listed.SourceDigest) != "" && sandboxDigestEqual(listed.SourceDigest, localDigest) {
		return listed.SourceRevision, sandboxSourceDigest(listed.SourceDigest), nil
	}
	seeded, err := client.Workspace(ctx, id, ref, projectAssistantSandboxWorkspaceRequest{Action: "seed", Files: append([]projectSandboxSyncFile(nil), files...)})
	if err != nil {
		return 0, "", err
	}
	if seeded.SourceRevision == 0 || strings.TrimSpace(seeded.SourceDigest) == "" {
		return 0, "", fmt.Errorf("%w: run sandbox seed returned no remote source fence", errProjectAssistantRunSandboxConflict)
	}
	if !sandboxDigestEqual(seeded.SourceDigest, localDigest) {
		return 0, "", fmt.Errorf("%w: run sandbox seed digest does not match the authoritative FileStore snapshot", errProjectAssistantRunSandboxConflict)
	}
	return seeded.SourceRevision, sandboxSourceDigest(seeded.SourceDigest), nil
}

func projectAssistantRunSandboxSeedRetryable(err error) bool {
	var statusErr *projectDevelopmentSyncHTTPError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.status {
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	case http.StatusConflict:
		// A 409 is only a provisioning race when the worker explicitly says
		// its runtime routing state is not ready. Revision/content conflicts
		// must fail closed and never be retried by setup.
		detail := strings.ToLower(statusErr.detail)
		return strings.Contains(detail, "not ready") ||
			strings.Contains(detail, "runtime namespace") ||
			strings.Contains(detail, "controlserviceref") ||
			strings.Contains(detail, "control service")
	default:
		return false
	}
}

// retryProjectAssistantRunSandboxSeed closes the small gap between an
// Instance publishing its status refs and the component Service accepting
// traffic. Only explicit transient upstream statuses are retried; auth,
// validation, malformed payload, and other errors fail immediately.
func retryProjectAssistantRunSandboxSeed(ctx context.Context, timeout, poll time.Duration, seed func(context.Context) error) error {
	if seed == nil {
		return errors.New("run sandbox seed function is not configured")
	}
	if timeout <= 0 {
		timeout = projectAssistantRunSandboxReadyTimeout
	}
	if poll <= 0 {
		poll = projectAssistantRunSandboxReadyPoll
	}
	seedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var lastErr error
	for {
		err := seed(seedCtx)
		if err == nil {
			return nil
		}
		if !projectAssistantRunSandboxSeedRetryable(err) {
			return err
		}
		lastErr = err
		select {
		case <-seedCtx.Done():
			if ctx.Err() != nil {
				return fmt.Errorf("seed run sandbox: %w", ctx.Err())
			}
			return fmt.Errorf("run sandbox seed did not become reachable within %s: %w", timeout, lastErr)
		case <-ticker.C:
		}
	}
}

func (b *projectAssistantRunSandbox) close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.metadata.Status = "closed"
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	return b.deleteInstance(ctx)
}

func (b *projectAssistantRunSandbox) deleteInstance(ctx context.Context) error {
	if b == nil || b.server == nil || (b.server.gql == nil && b.server.projectClientFor == nil) {
		return nil
	}
	client, err := b.server.clientFor(b.id)
	if err != nil {
		return err
	}
	resource := client.Resource(runSandboxInstancesResource, "")
	instance, err := resource.Get(ctx, b.instance.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	owner := strings.TrimSpace(instance.GetAnnotations()[projectAssistantRunSandboxClaimOwner])
	metadata := b.metadataSnapshot()
	if owner != "" && owner != strings.TrimSpace(metadata.RunID) {
		return fmt.Errorf("%w: refuse to delete run sandbox claimed by another run", errProjectAssistantRunSandboxConflict)
	}
	// The provider GraphQL delete path currently does not preserve Kubernetes
	// DeleteOptions preconditions. Ownership is therefore fenced by the durable
	// claim annotation plus App Studio's single-writer manager, not by pretending
	// a resource-version precondition reaches Infrastructure.
	err = resource.Delete(ctx, b.instance.Name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// cleanupInterruptedProjectAssistantRunSandbox is the explicit Stop path for a
// permission/input checkpoint. A suspended worker has uncertain in-flight
// effects, so it is never retained as a warm cache. The durable run ID,
// tenant/project identity, ownership label, claim owner, and cache generation
// all have to agree before an Instance is deleted; malformed or foreign
// metadata fails closed. The process-local lease is released only for the
// exact tenant/cache/run tuple, even when the remote cleanup cannot proceed.
func (s *Server) cleanupInterruptedProjectAssistantRunSandbox(ctx context.Context, id identity, scope store.Scope, run store.AssistantRun) error {
	if s == nil || strings.TrimSpace(run.ID) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tenantKey := projectAssistantRunSandboxTenantKey(id, workspace.Scope{
		OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID,
	})
	manager := s.projectAssistantSandboxManager()
	defer func() {
		// If a malformed checkpoint prevents deriving the exact cache name,
		// release any local lease owned by this exact run in this tenant. No
		// other run can be released by this owner-checked operation.
		manager.releaseRun(tenantKey, run.ID)
	}()

	var checkpoint projectAssistantCheckpointState
	var sandboxCheckpoint *projectAssistantSandboxCheckpoint
	if len(run.Checkpoint) > 0 {
		if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
			return fmt.Errorf("%w: decode interrupted sandbox checkpoint: %v", errProjectAssistantRunSandboxConflict, err)
		}
		sandboxCheckpoint = checkpoint.Sandbox
	}
	if sandboxCheckpoint == nil {
		return nil
	}
	metadata := projectAssistantRunSandboxMetadata{}
	metadata = sandboxCheckpoint.Metadata
	if strings.TrimSpace(metadata.RunID) == "" || strings.TrimSpace(metadata.RunID) != strings.TrimSpace(run.ID) {
		return fmt.Errorf("%w: interrupted sandbox run identity does not match", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.OrgUUID) == "" || strings.TrimSpace(metadata.OrgUUID) != strings.TrimSpace(scope.OrgUUID) {
		return fmt.Errorf("%w: interrupted sandbox organization does not match", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.WorkspaceUUID) == "" || strings.TrimSpace(metadata.WorkspaceUUID) != strings.TrimSpace(scope.WorkspaceUUID) {
		return fmt.Errorf("%w: interrupted sandbox workspace does not match", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.ProjectName) == "" || strings.TrimSpace(metadata.ProjectName) != strings.TrimSpace(scope.ProjectName) {
		return fmt.Errorf("%w: interrupted sandbox project does not match", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(metadata.ProjectUID) == "" || strings.TrimSpace(metadata.ProjectUID) != strings.TrimSpace(scope.ProjectUID) {
		return fmt.Errorf("%w: interrupted sandbox project UID does not match", errProjectAssistantRunSandboxConflict)
	}
	if strings.TrimSpace(id.orgUUID) == "" || strings.TrimSpace(id.orgUUID) != strings.TrimSpace(scope.OrgUUID) ||
		strings.TrimSpace(id.workspaceUUID) == "" || strings.TrimSpace(id.workspaceUUID) != strings.TrimSpace(scope.WorkspaceUUID) {
		return fmt.Errorf("%w: interrupted sandbox caller scope does not match", errProjectAssistantRunSandboxConflict)
	}

	wsScope := workspace.Scope{
		OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
	}
	derivedName := projectAssistantRunSandboxName(wsScope, nil, run.ID)
	name := strings.TrimSpace(metadata.Instance.Name)
	if name == "" || name != derivedName {
		return fmt.Errorf("%w: interrupted sandbox instance name does not match project identity", errProjectAssistantRunSandboxConflict)
	}
	generation := strings.TrimSpace(metadata.CacheGeneration)
	if generation == "" || generation != strings.TrimSpace(run.ID) {
		return fmt.Errorf("%w: interrupted sandbox cache generation does not match run", errProjectAssistantRunSandboxConflict)
	}
	manager.releaseExact(tenantKey, name, run.ID)
	if s.gql == nil && s.projectClientFor == nil {
		return errors.New("project client is not configured for interrupted sandbox cleanup")
	}
	client, err := s.clientFor(id)
	if err != nil {
		return fmt.Errorf("create interrupted sandbox cleanup client: %w", err)
	}
	// Stop may be initiated by a browser request that disconnects immediately
	// after the durable run is marked interrupted. Remote cleanup is therefore
	// independent of that request while remaining bounded like other data-plane
	// operations. The same context covers GET, DELETE, and deletion polling;
	// detaching only the final wait would leave a claimed Instance behind when
	// the request context is canceled during the initial lookup or delete.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dataPlaneCallTimeout)
	defer cancel()
	resource := client.Resource(runSandboxInstancesResource, "")
	instance, err := resource.Get(cleanupCtx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get interrupted sandbox instance %q: %w", name, err)
	}
	annotations := instance.GetAnnotations()
	if annotations[projectAssistantRunSandboxLabel] != "true" {
		return fmt.Errorf("%w: interrupted sandbox instance %q is not App Studio-owned", errProjectAssistantRunSandboxConflict, name)
	}
	if strings.TrimSpace(annotations[projectAssistantRunSandboxClaimOwner]) != run.ID ||
		strings.TrimSpace(annotations[projectAssistantRunSandboxCacheGeneration]) != generation {
		return fmt.Errorf("%w: interrupted sandbox instance %q is not owned by run %q", errProjectAssistantRunSandboxConflict, name, run.ID)
	}
	if err := resource.Delete(cleanupCtx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete interrupted sandbox instance %q: %w", name, err)
	}
	if err := waitForProjectAssistantRunSandboxInstanceDeleted(cleanupCtx, client, name); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("wait for interrupted sandbox instance %q deletion: %w", name, err)
	}
	return nil
}

// retain marks a successful terminal as an unclaimed project cache. The
// workspace volume and remote process survive, while a subsequent run must
// claim the same Instance and perform a fresh authoritative sync/baseline.
func (b *projectAssistantRunSandbox) retain(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	now := time.Now().UTC()
	b.closed = true
	b.metadata.Status = projectAssistantRunSandboxCacheStateCached
	b.metadata.LastActivityAt = now
	b.metadata.IdleExpiresAt = minProjectAssistantRunSandboxExpiry(now.Add(projectAssistantRunSandboxIdleTTL), b.metadata.HardExpiresAt)
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	if b.server == nil || (b.server.gql == nil && b.server.projectClientFor == nil) {
		return nil
	}
	client, err := b.server.clientFor(b.id)
	if err != nil {
		return err
	}
	resource := client.Resource(runSandboxInstancesResource, "")
	instance, err := resource.Get(ctx, b.instance.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	metadata := b.metadataSnapshot()
	annotations := instance.GetAnnotations()
	if strings.TrimSpace(annotations[projectAssistantRunSandboxClaimOwner]) != strings.TrimSpace(metadata.RunID) {
		return fmt.Errorf("%w: successful run no longer owns sandbox claim", errProjectAssistantRunSandboxConflict)
	}
	copy := make(map[string]string, len(annotations)+4)
	for key, value := range annotations {
		copy[key] = value
	}
	delete(copy, projectAssistantRunSandboxClaimOwner)
	delete(copy, projectAssistantRunSandboxClaimExpiry)
	copy[projectAssistantRunSandboxCacheState] = projectAssistantRunSandboxCacheStateCached
	copy[projectAssistantRunSandboxCacheGeneration] = metadata.CacheGeneration
	copy[projectAssistantRunSandboxLastActivity] = metadata.LastActivityAt.Format(time.RFC3339Nano)
	copy[projectAssistantRunSandboxIdleExpiry] = metadata.IdleExpiresAt.Format(time.RFC3339Nano)
	instance.SetAnnotations(copy)
	if _, err := resource.Update(ctx, instance, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("retain run sandbox cache: %w", err)
	}
	return nil
}

func finishProjectAssistantRunSandbox(ctx context.Context, sandbox *projectAssistantRunSandbox, release func(), runErr error, cacheSafe bool) error {
	if sandbox == nil {
		if release != nil {
			release()
		}
		return runErr
	}
	var permissionErr *projectAssistantPermissionRequiredError
	var inputErr *projectAssistantInputRequiredError
	if errors.As(runErr, &permissionErr) || errors.As(runErr, &inputErr) {
		// A suspended run keeps its instance and manager lease.  Its checkpoint
		// carries enough metadata for the resume path to reattach.
		return runErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The worker context is canceled for an interrupted run. Retention/cleanup
	// must not inherit that cancellation: the remote exec cancel has already
	// been sent on its detached bounded context, and the Instance lifecycle
	// transition must still reach Infrastructure.
	closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), dataPlaneCallTimeout)
	defer cancelClose()
	var closeErr error
	if cacheSafe {
		closeErr = sandbox.retain(closeCtx)
		if closeErr != nil {
			// A failed retention update must not leave an untracked live worker.
			// deleteInstance re-checks ownership before deleting, so a concurrent
			// claimant cannot be torn down by this fallback.
			deleteErr := sandbox.deleteInstance(closeCtx)
			if deleteErr != nil {
				closeErr = errors.Join(closeErr, deleteErr)
			}
		}
	} else {
		closeErr = sandbox.close(closeCtx)
	}
	if release != nil {
		release()
	}
	if closeErr != nil && runErr != nil {
		return errors.Join(runErr, fmt.Errorf("close run sandbox: %w", closeErr))
	}
	if closeErr != nil {
		return fmt.Errorf("close run sandbox: %w", closeErr)
	}
	return runErr
}

// projectAssistantRunSandboxSetupGuard owns a sandbox from acquisition until
// the assistant turn reaches its single terminal finish path. Setup performs
// several fallible operations (skills, plan hydration, audit, and budget
// configuration); a direct return from any of them must not leak an Instance
// or the per-tenant lease. Once finish is called, the guard is inert so the
// deferred setup cleanup cannot close or release a sandbox twice.
type projectAssistantRunSandboxSetupGuard struct {
	sandbox *projectAssistantRunSandbox
	release func()
	done    bool
}

func newProjectAssistantRunSandboxSetupGuard(sandbox *projectAssistantRunSandbox, release func()) *projectAssistantRunSandboxSetupGuard {
	if sandbox == nil && release == nil {
		return nil
	}
	return &projectAssistantRunSandboxSetupGuard{sandbox: sandbox, release: release}
}

func (g *projectAssistantRunSandboxSetupGuard) cleanupSetup() {
	if g == nil || g.done {
		return
	}
	g.done = true
	_ = finishProjectAssistantRunSandbox(context.Background(), g.sandbox, g.release, errors.New("assistant run setup failed"), false)
}

func (g *projectAssistantRunSandboxSetupGuard) finish(ctx context.Context, runErr error, cacheSafe bool) error {
	if g == nil || g.done {
		return runErr
	}
	g.done = true
	return finishProjectAssistantRunSandbox(ctx, g.sandbox, g.release, runErr, cacheSafe)
}

func projectAssistantRunSandboxSuspended(runErr error) bool {
	var permissionErr *projectAssistantPermissionRequiredError
	var inputErr *projectAssistantInputRequiredError
	return errors.As(runErr, &permissionErr) || errors.As(runErr, &inputErr)
}

func projectAssistantSandboxChanges(changes []projectAssistantSandboxWorkspaceChange) ([]workspace.ManagedFileChange, error) {
	if len(changes) > projectAssistantRunSandboxMaxChanges {
		return nil, fmt.Errorf("%w: checkpoint contains too many files", errProjectAssistantRunSandboxConflict)
	}
	var bytes int
	out := make([]workspace.ManagedFileChange, 0, len(changes))
	seen := map[string]struct{}{}
	for _, change := range changes {
		path, err := workspace.CleanProjectPath(change.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid checkpoint path", errProjectAssistantRunSandboxConflict)
		}
		if _, ok := seen[path]; ok {
			return nil, fmt.Errorf("%w: duplicate checkpoint path %q", errProjectAssistantRunSandboxConflict, path)
		}
		seen[path] = struct{}{}
		bytes += len([]byte(change.Content))
		if bytes > projectAssistantRunSandboxMaxChangeBytes {
			return nil, fmt.Errorf("%w: checkpoint content is too large", errProjectAssistantRunSandboxConflict)
		}
		op := workspace.ManagedFileOperation(change.Operation)
		switch op {
		case workspace.ManagedFileCreate, workspace.ManagedFileReplace, workspace.ManagedFileDelete:
		default:
			return nil, fmt.Errorf("%w: unsupported checkpoint operation %q", errProjectAssistantRunSandboxConflict, change.Operation)
		}
		out = append(out, workspace.ManagedFileChange{Path: path, Operation: op, Content: change.Content, ExpectedVersion: change.ExpectedVersion})
	}
	return out, nil
}

func projectAssistantSandboxChangesJSON(changes []workspace.ManagedFileChange) []projectAssistantSandboxWorkspaceChange {
	out := make([]projectAssistantSandboxWorkspaceChange, 0, len(changes))
	for _, change := range changes {
		out = append(out, projectAssistantSandboxWorkspaceChange{Path: change.Path, Operation: string(change.Operation), Content: change.Content, ExpectedVersion: change.ExpectedVersion})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// checkpoint atomically applies only worker-returned bounded changes.  The
// worker must provide expected versions from the seed; any local source drift
// or stale expected version rejects the whole transaction.
func (b *projectAssistantRunSandbox) checkpointForTerminalSettlement(ctx context.Context, req projectAssistantRunRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() == nil {
		return b.checkpoint(ctx, req)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		// An actual deadline is a settlement failure, not a user interruption.
		// Preserve the expired context so the caller deletes the uncertain cache.
		return b.checkpoint(ctx, req)
	}
	// A user interruption cancels the run context before the bounded executor
	// returns its terminal canceled result. The command has already settled at
	// this point, so use an independent bounded context to preserve any proven
	// workspace changes and retain a healthy warm cache. A real checkpoint or
	// fence failure still fails closed and deletes the Instance.
	checkpointCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dataPlaneCallTimeout)
	defer cancel()
	return b.checkpoint(checkpointCtx, req)
}

func (b *projectAssistantRunSandbox) checkpoint(ctx context.Context, req projectAssistantRunRequest) error {
	if b == nil || req.Workspace == nil {
		return fmt.Errorf("%w: project workspace store is not configured", errProjectAssistantRunSandboxConflict)
	}
	if b.runState != nil && !projectAssistantTurnProfileAllowsMutation(b.runState.TurnProfile()) {
		return nil
	}
	meta := b.metadataSnapshot()
	localRevision, err := req.Workspace.SourceRevision(ctx, req.WorkspaceScope)
	if err != nil {
		return err
	}
	if localRevision != meta.SourceRevision {
		return fmt.Errorf("%w: source revision changed from %d to %d", errProjectAssistantRunSandboxConflict, meta.SourceRevision, localRevision)
	}
	var localSnapshot projectWorkspaceSyncSnapshot
	if b.server != nil {
		localSnapshot, err = b.server.projectWorkspaceSyncFiles(ctx, req.WorkspaceScope)
		if err != nil {
			return err
		}
		if expected := strings.TrimSpace(meta.SourceDigest); expected != "" {
			observed := projectSandboxSyncDigest(localSnapshot.Files)
			if observed != expected {
				return fmt.Errorf("%w: source digest changed", errProjectAssistantRunSandboxConflict)
			}
		}
	}
	if strings.TrimSpace(meta.RemoteCheckpointID) == "" {
		return fmt.Errorf("%w: remote workspace baseline checkpoint is missing", errProjectAssistantRunSandboxConflict)
	}
	response, err := b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "checkpoint", CheckpointID: meta.RemoteCheckpointID})
	if err != nil {
		return err
	}
	changes, err := projectAssistantSandboxChanges(response.Changes)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		remote := b.metadataSnapshot()
		b.mu.Lock()
		b.metadata.CheckpointRevision = remote.RemoteRevision
		b.metadata.CheckpointDigest = remote.RemoteDigest
		b.mu.Unlock()
		if b.runState != nil {
			b.runState.SetSandboxMetadata(b.metadataSnapshot())
		}
		return nil
	}
	if _, err := req.Workspace.ApplyManagedTransaction(ctx, req.WorkspaceScope, changes); err != nil {
		return fmt.Errorf("%w: apply checkpoint: %v", errProjectAssistantRunSandboxConflict, err)
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	if _, err := req.Workspace.AddUncommittedPaths(ctx, req.WorkspaceScope, paths); err != nil {
		return fmt.Errorf("%w: persist checkpoint dirty paths: %v", errProjectAssistantRunSandboxConflict, err)
	}
	newRevision, err := req.Workspace.SourceRevision(ctx, req.WorkspaceScope)
	if err != nil {
		return err
	}
	newDigest := ""
	if b.server != nil {
		// The source fence is the complete FileStore snapshot, not the digest of
		// only the changed paths. This remains comparable with the next full
		// seed/checkpoint and with the component-root worker digest.
		updated, snapshotErr := b.server.projectWorkspaceSyncFiles(ctx, req.WorkspaceScope)
		if snapshotErr != nil {
			return snapshotErr
		}
		newDigest = projectSandboxSyncDigest(updated.Files)
	} else {
		newDigest, err = req.Workspace.WorkspaceDigest(ctx, req.WorkspaceScope, paths)
		if err != nil {
			return err
		}
	}
	// Advance the worker's durable baseline only after the FileStore
	// transaction succeeds. If this call fails, the old metadata remains a
	// deliberate fail-closed fence rather than pretending the two stores agree.
	baseline, err := b.request(ctx, projectAssistantSandboxWorkspaceRequest{Action: "checkpoint", CheckpointAction: "create"})
	if err != nil {
		return fmt.Errorf("%w: advance remote workspace baseline: %v", errProjectAssistantRunSandboxConflict, err)
	}
	if strings.TrimSpace(baseline.CheckpointID) == "" {
		return fmt.Errorf("%w: remote workspace baseline returned no checkpoint ID", errProjectAssistantRunSandboxConflict)
	}
	if baseline.SourceRevision == 0 || strings.TrimSpace(baseline.SourceDigest) == "" {
		return fmt.Errorf("%w: remote workspace baseline returned no source fence", errProjectAssistantRunSandboxConflict)
	}
	b.mu.Lock()
	b.metadata.SourceRevision = newRevision
	b.metadata.SourceDigest = newDigest
	b.metadata.RemoteRevision = baseline.SourceRevision
	b.metadata.RemoteDigest = baseline.SourceDigest
	b.metadata.CheckpointRevision = baseline.SourceRevision
	b.metadata.CheckpointDigest = baseline.SourceDigest
	b.metadata.RemoteCheckpointID = baseline.CheckpointID
	b.mu.Unlock()
	if b.runState != nil {
		b.runState.SetSandboxMetadata(b.metadataSnapshot())
	}
	// FileStore writeback is authoritative and must not depend on a hosted
	// Project development environment. A template-less project can still use
	// the per-run universal sandbox; in that case there is no legacy preview
	// target to synchronize after the checkpoint.
	// req.Project is the shared, current project snapshot. b.project captures
	// only the state at sandbox creation and can be stale after this same turn
	// binds a hosted development template.
	if b.server != nil && projectAssistantDevelopmentTemplateBound(req.Project) {
		if b.runState != nil {
			syncRevision := b.runState.BeginDevelopmentSyncForCurrentMutation()
			if !b.server.scheduleDevelopmentSyncAfterMutationWithCompletion(
				b.id,
				req.Project,
				projectActionWorkspaceSync,
				func(syncErr error) { b.runState.CompleteDevelopmentSync(syncRevision, syncErr) },
			) {
				b.runState.CompleteDevelopmentSync(syncRevision, errors.New("workspace synchronization was not scheduled after sandbox checkpoint"))
			}
		} else {
			b.server.scheduleDevelopmentSyncAfterMutationWithCompletion(b.id, req.Project, projectActionWorkspaceSync, nil)
		}
	}
	return nil
}
