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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/dataplane"
)

const (
	// automaticProviderActionGrantedBy is deliberately a server-owned audit
	// identity. It distinguishes this temporary compatibility path from the
	// user-consent grant path, which records the authenticated caller.
	automaticProviderActionGrantedBy        = "server:automatic-provider-actions"
	automaticProviderIntegrationPrefix      = "auto-"
	automaticProviderIntegrationEnvironment = projectIntegrationDefaultEnvironment
)

var automaticIntegrationAliasPartRE = regexp.MustCompile(`[^a-z0-9_-]+`)

// automaticProviderCatalogResource groups all eligible actions that are bound
// to one provider GVR/kind. A single project binding is materialized per
// provider-owned object, with every eligible action for that object's exact
// resource reference in its allow-list.
type automaticProviderCatalogResource struct {
	provider   string
	apiVersion string
	kind       string
	resource   string
	gvr        schema.GroupVersionResource
	actions    []automaticProviderCatalogAction
}

type automaticProviderCatalogAction struct {
	name         string
	version      string
	schemaDigest string
	catalog      providerCatalogAction
}

type automaticIntegrationTarget struct {
	provider        string
	ref             *aiv1alpha1.ProjectProviderResourceReference
	uid             string
	resourceVersion string
	actions         []aiv1alpha1.ProjectProviderActionSpec
	catalogActions  []providerCatalogAction
}

type automaticIntegrationDiscovery struct {
	targets             []automaticIntegrationTarget
	failedResourceTypes map[string]struct{}
	catalogUnavailable  bool
	status              integrationDiscoveryStatus
}

type automaticIntegrationTimeInterval struct {
	started time.Time
	ended   time.Time
}

type automaticIntegrationResourceMetrics struct {
	metadataRequests                     int
	metadataFailures                     int
	metadataItems                        int
	metadataHTTPDuration                 time.Duration
	metadataHTTPIntervals                []automaticIntegrationTimeInterval
	parentReviewCalls                    int
	parentReviewAllows                   int
	parentReviewDenials                  int
	parentReviewErrors                   int
	parentReviewDuration                 time.Duration
	parentReviewIntervals                []automaticIntegrationTimeInterval
	parentReviewServiceDurations         []time.Duration
	parentReviewWaits                    projectAssistantRateLimiterWaitSummary
	actionReviewCalls                    int
	actionReviewAllows                   int
	actionReviewDenials                  int
	actionReviewErrors                   int
	actionReviewDuration                 time.Duration
	actionReviewIntervals                []automaticIntegrationTimeInterval
	actionReviewServiceDurations         []time.Duration
	actionReviewWaits                    projectAssistantRateLimiterWaitSummary
	actionReviewDuplicateVersionsSkipped int
	reviewIntervals                      []automaticIntegrationTimeInterval
	canceled                             bool
}

type automaticIntegrationDiscoveryMetrics struct {
	catalogDuration                      time.Duration
	identityDuration                     time.Duration
	resourceWorkDuration                 time.Duration
	resourceTypes                        int
	catalogActionVersions                int
	actionSubresourceCoordinates         int
	metadataRequests                     int
	metadataFailures                     int
	metadataItems                        int
	metadataHTTPDuration                 time.Duration
	metadataHTTPWall                     time.Duration
	parentReviewCalls                    int
	parentReviewAllows                   int
	parentReviewDenials                  int
	parentReviewErrors                   int
	parentReviewDuration                 time.Duration
	parentReviewWall                     time.Duration
	parentReviewService                  automaticIntegrationServiceDurationSummary
	parentReviewWaits                    projectAssistantRateLimiterWaitSummary
	actionReviewCalls                    int
	actionReviewAllows                   int
	actionReviewDenials                  int
	actionReviewErrors                   int
	actionReviewDuration                 time.Duration
	actionReviewWall                     time.Duration
	actionReviewService                  automaticIntegrationServiceDurationSummary
	actionReviewWaits                    projectAssistantRateLimiterWaitSummary
	reviewDuration                       time.Duration
	reviewWall                           time.Duration
	actionReviewDuplicateVersionsSkipped int
	canceledResources                    int
}

const automaticIntegrationUpdateAttempts = 3

// materializeAutomaticProjectIntegrations temporarily removes the requirement
// for a user-created integration grant before an assistant turn. It is
// intentionally best-effort for catalog and resource-list failures: an
// actionless turn must remain usable when a provider is unavailable. Once a
// target has been discovered, project persistence and runtime reconciliation
// are strict so the assistant never receives a stale Project after a reported
// successful materialization.
//
// TODO(least-privilege): restore explicit user consent and least-privilege
// action grants after the temporary automatic-access rollout is retired.
func (s *Server) materializeAutomaticProjectIntegrations(ctx context.Context, c *asclient.Client, id identity, project *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	if s == nil || c == nil || project == nil {
		return project, nil
	}
	discovery := s.discoverAutomaticProjectIntegrations(ctx, c, id, project)
	updated, err := materializeDiscoveredAutomaticProjectIntegrations(ctx, c, project, discovery.targets)
	if err != nil || updated == nil || reflect.DeepEqual(project.Spec, updated.Spec) {
		return updated, err
	}
	if _, err := s.projectIdentityToken(ctx, id, updated); err != nil {
		return nil, fmt.Errorf("refresh Project identity after automatic integration discovery: %w", err)
	}
	return updated, nil
}

func (s *Server) discoverAutomaticProjectIntegrations(ctx context.Context, c *asclient.Client, id identity, project *aiv1alpha1.Project) automaticIntegrationDiscovery {
	started := time.Now()
	metrics := automaticIntegrationDiscoveryMetrics{}
	discovery := automaticIntegrationDiscovery{
		failedResourceTypes: map[string]struct{}{},
		status:              integrationDiscoveryStatus{State: "available", Issues: []integrationDiscoveryIssue{}},
	}
	defer func() {
		klog.FromContext(ctx).Info("App Studio integration discovery completed",
			"resourceTypes", metrics.resourceTypes,
			"catalogActionVersions", metrics.catalogActionVersions,
			"actionSubresourceCoordinates", metrics.actionSubresourceCoordinates,
			"metadataRequests", metrics.metadataRequests,
			"metadataFailures", metrics.metadataFailures,
			"metadataItems", metrics.metadataItems,
			"metadataHTTPDuration", metrics.metadataHTTPDuration,
			"metadataHTTPWall", metrics.metadataHTTPWall,
			"parentReviewCalls", metrics.parentReviewCalls,
			"parentReviewAllows", metrics.parentReviewAllows,
			"parentReviewDenials", metrics.parentReviewDenials,
			"parentReviewErrors", metrics.parentReviewErrors,
			"parentReviewDuration", metrics.parentReviewDuration,
			"parentReviewWall", metrics.parentReviewWall,
			"parentReviewServiceCalls", metrics.parentReviewService.calls,
			"parentReviewServiceP50", metrics.parentReviewService.p50,
			"parentReviewServiceP95", metrics.parentReviewService.p95,
			"parentReviewServiceMax", metrics.parentReviewService.max,
			"parentReviewRateLimiterWaitCalls", metrics.parentReviewWaits.calls,
			"parentReviewRateLimiterWaitDuration", metrics.parentReviewWaits.total,
			"parentReviewRateLimiterWaitDelayed", metrics.parentReviewWaits.delayed,
			"reviewRateLimiterWaitDelayedThreshold", projectAssistantRateLimiterDelayedThreshold,
			"parentReviewRateLimiterWaitMax", metrics.parentReviewWaits.max,
			"actionReviewCalls", metrics.actionReviewCalls,
			"actionReviewAllows", metrics.actionReviewAllows,
			"actionReviewDenials", metrics.actionReviewDenials,
			"actionReviewErrors", metrics.actionReviewErrors,
			"actionReviewDuration", metrics.actionReviewDuration,
			"actionReviewWall", metrics.actionReviewWall,
			"actionReviewServiceCalls", metrics.actionReviewService.calls,
			"actionReviewServiceP50", metrics.actionReviewService.p50,
			"actionReviewServiceP95", metrics.actionReviewService.p95,
			"actionReviewServiceMax", metrics.actionReviewService.max,
			"actionReviewRateLimiterWaitCalls", metrics.actionReviewWaits.calls,
			"actionReviewRateLimiterWaitDuration", metrics.actionReviewWaits.total,
			"actionReviewRateLimiterWaitDelayed", metrics.actionReviewWaits.delayed,
			"actionReviewRateLimiterWaitMax", metrics.actionReviewWaits.max,
			"authorizationReviewDuration", metrics.reviewDuration,
			"authorizationReviewWall", metrics.reviewWall,
			"duplicateActionVersionReviewsSkipped", metrics.actionReviewDuplicateVersionsSkipped,
			"canceledResources", metrics.canceledResources,
			"catalogDuration", metrics.catalogDuration,
			"identityDuration", metrics.identityDuration,
			"resourceWorkDuration", metrics.resourceWorkDuration,
			"targets", len(discovery.targets),
			"issues", len(discovery.status.Issues),
			"duration", time.Since(started),
		)
	}()
	if s == nil || c == nil {
		discovery.status = unavailableIntegrationDiscovery("discovery_unavailable", "Provider integration discovery is unavailable.", "", "")
		return discovery
	}
	catalogStarted := time.Now()
	catalog, err := s.providerActionCatalogForProject(ctx, id, project)
	metrics.catalogDuration = time.Since(catalogStarted)
	if err != nil {
		discovery.catalogUnavailable = true
		discovery.status = unavailableIntegrationDiscovery("catalog_unavailable", "The provider action catalog could not be loaded.", "", "")
		return discovery
	}
	resources := automaticProviderCatalogResources(catalog)
	metrics.resourceTypes = len(resources)
	for _, resource := range resources {
		metrics.catalogActionVersions += len(resource.actions)
		seen := make(map[string]struct{}, len(resource.actions))
		for _, action := range resource.actions {
			if _, exists := seen[action.name]; exists {
				continue
			}
			seen[action.name] = struct{}{}
			metrics.actionSubresourceCoordinates++
		}
	}
	if len(resources) == 0 {
		return discovery
	}
	identityStarted := time.Now()
	reviewID, err := s.integrationDiscoveryIdentity(ctx, id)
	metrics.identityDuration = time.Since(identityStarted)
	if err != nil {
		discovery.status = unavailableIntegrationDiscovery("authorization_unavailable", "Integration authorization is unavailable.", "", "")
		return discovery
	}
	budget := s.discoveryBudget()
	type result struct {
		discovery automaticIntegrationDiscovery
		success   bool
		metrics   automaticIntegrationResourceMetrics
	}
	results := make([]result, len(resources))
	jobs := make(chan int)
	var workers sync.WaitGroup
	var metadataIntervals, parentReviewIntervals, actionReviewIntervals, reviewIntervals []automaticIntegrationTimeInterval
	var parentReviewServiceDurations, actionReviewServiceDurations []time.Duration
	resourceWorkStarted := time.Now()
	for worker := 0; worker < min(integrationDiscoveryConcurrency, len(resources)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				select {
				case budget.slots <- struct{}{}:
					results[i].discovery, results[i].success, results[i].metrics = s.discoverAutomaticIntegrationResource(ctx, reviewID, resources[i])
					<-budget.slots
				case <-ctx.Done():
					results[i].metrics.canceled = true
					results[i].discovery.status.Issues = []integrationDiscoveryIssue{{Code: "authorization_unavailable", Message: "Integration discovery was canceled.", Provider: resources[i].provider, Resource: resources[i].resource}}
				}
			}
		}()
	}
	for i := range resources {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	targets := make([]automaticIntegrationTarget, 0)
	resourceSuccesses := 0
	// Merge in catalog order, independent of worker scheduling. Each worker
	// owns its result; no shared target maps or grant lists are mutated concurrently.
	for i, result := range results {
		targets = append(targets, result.discovery.targets...)
		discovery.status.Issues = append(discovery.status.Issues, result.discovery.status.Issues...)
		metrics.metadataRequests += result.metrics.metadataRequests
		metrics.metadataFailures += result.metrics.metadataFailures
		metrics.metadataItems += result.metrics.metadataItems
		metrics.metadataHTTPDuration += result.metrics.metadataHTTPDuration
		metrics.parentReviewCalls += result.metrics.parentReviewCalls
		metrics.parentReviewAllows += result.metrics.parentReviewAllows
		metrics.parentReviewDenials += result.metrics.parentReviewDenials
		metrics.parentReviewErrors += result.metrics.parentReviewErrors
		metrics.parentReviewDuration += result.metrics.parentReviewDuration
		metrics.reviewDuration += result.metrics.parentReviewDuration
		metrics.actionReviewCalls += result.metrics.actionReviewCalls
		metrics.actionReviewAllows += result.metrics.actionReviewAllows
		metrics.actionReviewDenials += result.metrics.actionReviewDenials
		metrics.actionReviewErrors += result.metrics.actionReviewErrors
		metrics.actionReviewDuration += result.metrics.actionReviewDuration
		metrics.reviewDuration += result.metrics.actionReviewDuration
		metrics.actionReviewDuplicateVersionsSkipped += result.metrics.actionReviewDuplicateVersionsSkipped
		if result.metrics.canceled {
			metrics.canceledResources++
		}
		metadataIntervals = append(metadataIntervals, result.metrics.metadataHTTPIntervals...)
		parentReviewIntervals = append(parentReviewIntervals, result.metrics.parentReviewIntervals...)
		parentReviewServiceDurations = append(parentReviewServiceDurations, result.metrics.parentReviewServiceDurations...)
		actionReviewIntervals = append(actionReviewIntervals, result.metrics.actionReviewIntervals...)
		actionReviewServiceDurations = append(actionReviewServiceDurations, result.metrics.actionReviewServiceDurations...)
		reviewIntervals = append(reviewIntervals, result.metrics.reviewIntervals...)
		metrics.parentReviewWaits.add(result.metrics.parentReviewWaits)
		metrics.actionReviewWaits.add(result.metrics.actionReviewWaits)
		if result.success {
			resourceSuccesses++
		} else {
			resource := resources[i]
			discovery.failedResourceTypes[automaticProviderCatalogResourceKey(resource.provider, resource.gvr, resource.kind, resource.resource)] = struct{}{}
		}
	}
	metrics.resourceWorkDuration = time.Since(resourceWorkStarted)
	metrics.metadataHTTPWall = automaticIntegrationIntervalUnion(metadataIntervals)
	metrics.parentReviewWall = automaticIntegrationIntervalUnion(parentReviewIntervals)
	metrics.actionReviewWall = automaticIntegrationIntervalUnion(actionReviewIntervals)
	metrics.reviewWall = automaticIntegrationIntervalUnion(reviewIntervals)
	metrics.parentReviewService = automaticIntegrationServiceDurationSummaryFor(parentReviewServiceDurations)
	metrics.actionReviewService = automaticIntegrationServiceDurationSummaryFor(actionReviewServiceDurations)
	sort.Slice(targets, func(i, j int) bool {
		return automaticProviderReferenceKey(targets[i].provider, targets[i].ref) < automaticProviderReferenceKey(targets[j].provider, targets[j].ref)
	})
	discovery.targets = targets
	if len(discovery.status.Issues) > 0 {
		discovery.status.State = "partial"
		if resourceSuccesses == 0 {
			discovery.status.State = "unavailable"
		}
	}
	return discovery
}

// Each resource retains the parent-read check before checking any actions.
// Only an explicit allow is included; denial, cancellation and errors never
// produce a grant. Consent and schema checks remain in the original flow.
func (s *Server) discoverAutomaticIntegrationResource(ctx context.Context, id identity, resource automaticProviderCatalogResource) (automaticIntegrationDiscovery, bool, automaticIntegrationResourceMetrics) {
	discovery := automaticIntegrationDiscovery{failedResourceTypes: map[string]struct{}{}}
	metrics := automaticIntegrationResourceMetrics{}
	targets := make([]automaticIntegrationTarget, 0)
	metrics.metadataRequests = 1
	listStarted := time.Now()
	list, listErr := s.fetchProviderResourceMetadata(ctx, id, resource.provider, resource.apiVersion, resource.kind, resource.resource)
	listEnded := time.Now()
	metrics.metadataHTTPDuration = listEnded.Sub(listStarted)
	metrics.metadataHTTPIntervals = append(metrics.metadataHTTPIntervals, automaticIntegrationTimeInterval{started: listStarted, ended: listEnded})
	if ctx.Err() != nil && listErr == nil {
		listErr = ctx.Err()
	}
	if listErr != nil {
		metrics.metadataFailures = 1
		metrics.canceled = errors.Is(listErr, context.Canceled) || errors.Is(listErr, context.DeadlineExceeded)
		discovery.failedResourceTypes[automaticProviderCatalogResourceKey(resource.provider, resource.gvr, resource.kind, resource.resource)] = struct{}{}
		discovery.addIssue(integrationIssueForResourceError(listErr, resource.provider, resource.resource))
		return discovery, false, metrics
	}
	metrics.metadataItems = len(list.Items)
	if list.Truncated {
		discovery.addIssue(integrationDiscoveryIssue{Code: "results_truncated", Message: "Some provider resources were omitted because the discovery limit was reached.", Provider: resource.provider, Resource: resource.resource})
	}
	for _, object := range list.Items {
		if ctx.Err() != nil {
			metrics.canceled = true
			discovery.failedResourceTypes[automaticProviderCatalogResourceKey(resource.provider, resource.gvr, resource.kind, resource.resource)] = struct{}{}
			discovery.targets = nil
			discovery.addIssue(integrationDiscoveryIssue{Code: "authorization_unavailable", Message: "Integration discovery was canceled.", Provider: resource.provider, Resource: resource.resource})
			return discovery, false, metrics
		}
		name := strings.TrimSpace(object.Metadata.Name)
		if name == "" {
			continue
		}
		ref := &aiv1alpha1.ProjectProviderResourceReference{
			Name: name, APIVersion: resource.apiVersion, Kind: resource.kind, Resource: resource.resource,
		}
		parentStarted := time.Now()
		parentReviewCtx, parentWaitObservation := projectAssistantObserveRateLimiterWaits(ctx)
		parentAllowed, authErr := s.authorizeCaller(parentReviewCtx, id, dataplane.ResourceAttributes{
			Group: resource.gvr.Group, Version: resource.gvr.Version, Resource: resource.resource,
			Name: name, Verb: "get",
		})
		parentEnded := time.Now()
		parentWaits := parentWaitObservation.Snapshot()
		metrics.parentReviewWaits.add(parentWaits)
		metrics.parentReviewCalls++
		metrics.parentReviewDuration += parentEnded.Sub(parentStarted)
		metrics.parentReviewIntervals = append(metrics.parentReviewIntervals, automaticIntegrationTimeInterval{started: parentStarted, ended: parentEnded})
		metrics.parentReviewServiceDurations = append(metrics.parentReviewServiceDurations, automaticIntegrationReviewServiceDuration(parentEnded.Sub(parentStarted), parentWaits))
		metrics.reviewIntervals = append(metrics.reviewIntervals, automaticIntegrationTimeInterval{started: parentStarted, ended: parentEnded})
		if ctx.Err() != nil && authErr == nil {
			authErr = ctx.Err()
			parentAllowed = false
		}
		if authErr != nil {
			metrics.parentReviewErrors++
			discovery.addIssue(integrationDiscoveryIssue{Code: "authorization_unavailable", Message: "Caller access to a discovered provider resource could not be verified.", Provider: resource.provider, Resource: resource.resource})
			if errors.Is(authErr, context.Canceled) || errors.Is(authErr, context.DeadlineExceeded) {
				metrics.canceled = true
				discovery.failedResourceTypes[automaticProviderCatalogResourceKey(resource.provider, resource.gvr, resource.kind, resource.resource)] = struct{}{}
				discovery.targets = nil
				return discovery, false, metrics
			}
			continue
		}
		if !parentAllowed {
			metrics.parentReviewDenials++
			discovery.addIssue(integrationDiscoveryIssue{Code: "resource_denied", Message: "The caller cannot read a discovered provider resource.", Provider: resource.provider, Resource: resource.resource})
			continue
		}
		metrics.parentReviewAllows++
		actions := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(resource.actions))
		catalogActions := make([]providerCatalogAction, 0, len(resource.actions))
		type actionReviewResult struct {
			allowed bool
			err     error
		}
		actionReviews := make(map[string]actionReviewResult, len(resource.actions))
		for _, action := range resource.actions {
			result, reviewed := actionReviews[action.name]
			if reviewed {
				metrics.actionReviewDuplicateVersionsSkipped++
			} else {
				actionStarted := time.Now()
				actionReviewCtx, actionWaitObservation := projectAssistantObserveRateLimiterWaits(ctx)
				result.allowed, result.err = s.authorizeCaller(actionReviewCtx, id, dataplane.ResourceAttributes{
					Group: resource.gvr.Group, Version: resource.gvr.Version, Resource: resource.resource,
					Subresource: action.name, Name: name, Verb: "create",
				})
				actionEnded := time.Now()
				actionWaits := actionWaitObservation.Snapshot()
				metrics.actionReviewWaits.add(actionWaits)
				metrics.actionReviewCalls++
				metrics.actionReviewDuration += actionEnded.Sub(actionStarted)
				metrics.actionReviewIntervals = append(metrics.actionReviewIntervals, automaticIntegrationTimeInterval{started: actionStarted, ended: actionEnded})
				metrics.actionReviewServiceDurations = append(metrics.actionReviewServiceDurations, automaticIntegrationReviewServiceDuration(actionEnded.Sub(actionStarted), actionWaits))
				metrics.reviewIntervals = append(metrics.reviewIntervals, automaticIntegrationTimeInterval{started: actionStarted, ended: actionEnded})
				if ctx.Err() != nil && result.err == nil {
					result.err = ctx.Err()
					result.allowed = false
				}
				if result.err != nil {
					metrics.actionReviewErrors++
				} else if result.allowed {
					metrics.actionReviewAllows++
				} else {
					metrics.actionReviewDenials++
				}
				actionReviews[action.name] = result
			}
			allowed, actionErr := result.allowed, result.err
			if actionErr != nil {
				discovery.addIssue(integrationDiscoveryIssue{Code: "authorization_unavailable", Message: "Caller access to a provider action could not be verified.", Provider: resource.provider, Resource: resource.resource})
				if errors.Is(actionErr, context.Canceled) || errors.Is(actionErr, context.DeadlineExceeded) {
					metrics.canceled = true
					discovery.failedResourceTypes[automaticProviderCatalogResourceKey(resource.provider, resource.gvr, resource.kind, resource.resource)] = struct{}{}
					discovery.targets = nil
					return discovery, false, metrics
				}
				continue
			}
			if !allowed {
				discovery.addIssue(integrationDiscoveryIssue{Code: "action_denied", Message: "The caller is not authorized for one or more provider actions.", Provider: resource.provider, Resource: resource.resource})
				continue
			}
			catalogAction := action.catalog
			if strings.TrimSpace(catalogAction.ID) == "" {
				catalogAction.ID = action.name + "/" + action.version
			}
			catalogActions = append(catalogActions, catalogAction)
			// Listing an action does not grant it. Actions requiring consent stay
			// visible as candidates, where the explicit add flow can collect
			// consentAccepted, but automatic turn-start materialization never
			// grants them on the caller's behalf.
			if !action.catalog.Consent.Required {
				actions = append(actions, aiv1alpha1.ProjectProviderActionSpec{
					Name: action.name, Version: action.version, SchemaDigest: action.schemaDigest,
				})
			}
		}
		if len(catalogActions) == 0 {
			continue
		}
		targets = append(targets, automaticIntegrationTarget{
			provider: resource.provider, ref: ref, uid: object.Metadata.UID,
			resourceVersion: strings.TrimSpace(object.Metadata.ResourceVersion), actions: actions, catalogActions: catalogActions,
		})
	}
	discovery.targets = targets
	return discovery, true, metrics
}

func automaticIntegrationIntervalUnion(intervals []automaticIntegrationTimeInterval) time.Duration {
	valid := make([]automaticIntegrationTimeInterval, 0, len(intervals))
	for _, interval := range intervals {
		if interval.started.IsZero() || interval.ended.Before(interval.started) || interval.ended.Equal(interval.started) {
			continue
		}
		valid = append(valid, interval)
	}
	if len(valid) == 0 {
		return 0
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].started.Before(valid[j].started) })
	started, ended := valid[0].started, valid[0].ended
	var total time.Duration
	for _, interval := range valid[1:] {
		if interval.started.After(ended) {
			total += ended.Sub(started)
			started, ended = interval.started, interval.ended
			continue
		}
		if interval.ended.After(ended) {
			ended = interval.ended
		}
	}
	return total + ended.Sub(started)
}

type automaticIntegrationServiceDurationSummary struct {
	calls int
	p50   time.Duration
	p95   time.Duration
	max   time.Duration
}

// automaticIntegrationReviewServiceDuration estimates time spent in the
// review request itself by removing the observed client-go rate-limiter wait
// from the end-to-end review call. It intentionally retains no request or
// identity data.
func automaticIntegrationReviewServiceDuration(elapsed time.Duration, waits projectAssistantRateLimiterWaitSummary) time.Duration {
	service := elapsed - waits.total
	if service < 0 {
		return 0
	}
	return service
}

func automaticIntegrationServiceDurationSummaryFor(values []time.Duration) automaticIntegrationServiceDurationSummary {
	if len(values) == 0 {
		return automaticIntegrationServiceDurationSummary{}
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return automaticIntegrationServiceDurationSummary{
		calls: len(sorted),
		p50:   sorted[automaticIntegrationNearestRankIndex(len(sorted), 50)],
		p95:   sorted[automaticIntegrationNearestRankIndex(len(sorted), 95)],
		max:   sorted[len(sorted)-1],
	}
}

func automaticIntegrationNearestRankIndex(count, percentile int) int {
	if count <= 1 || percentile <= 0 {
		return 0
	}
	if percentile >= 100 {
		return count - 1
	}
	rank := (percentile*count + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return rank - 1
}

func (d *automaticIntegrationDiscovery) addIssue(issue integrationDiscoveryIssue) {
	if d == nil {
		return
	}
	d.status.Issues = append(d.status.Issues, issue)
}

func unavailableIntegrationDiscovery(code, message, provider, resource string) integrationDiscoveryStatus {
	return integrationDiscoveryStatus{
		State:  "unavailable",
		Issues: []integrationDiscoveryIssue{{Code: code, Message: message, Provider: provider, Resource: resource}},
	}
}

func integrationIssueForResourceError(err error, provider, resource string) integrationDiscoveryIssue {
	issue := integrationDiscoveryIssue{Code: "resource_unavailable", Message: "Provider resource discovery failed.", Provider: provider, Resource: resource}
	var responseErr providerResourceDiscoveryHTTPError
	if errors.As(err, &responseErr) {
		switch responseErr.Status {
		case http.StatusUnauthorized:
			issue.Code = "caller_proof_rejected"
			issue.Message = "The hub rejected the signed caller context for provider discovery."
		case http.StatusForbidden:
			issue.Code = "resource_denied"
			issue.Message = "The caller cannot list this provider resource, or the provider is not enabled in this workspace."
		case http.StatusNotFound:
			issue.Code = "provider_unavailable"
			issue.Message = "The provider resource is not currently available in this workspace."
		default:
			if responseErr.Status >= http.StatusInternalServerError {
				issue.Code = "hub_unavailable"
				issue.Message = "The hub could not complete provider resource discovery."
			}
		}
	} else if strings.Contains(err.Error(), "caller proof is missing") {
		issue.Code = "caller_proof_missing"
		issue.Message = "The signed caller context is missing; retry from the workspace."
	}
	return issue
}

// availableProjectIntegrationCandidates returns discovered action metadata,
// including actions still awaiting consent on existing bindings. It never
// writes the Project or treats catalog metadata as a persisted grant.
func availableProjectIntegrationCandidates(project *aiv1alpha1.Project, discovery automaticIntegrationDiscovery) []projectIntegrationCandidate {
	if project == nil {
		return []projectIntegrationCandidate{}
	}
	usedAliases := map[string]struct{}{}
	for _, env := range project.Spec.Environments {
		for _, binding := range env.Bindings {
			if alias := strings.ToLower(strings.TrimSpace(binding.Name)); alias != "" {
				usedAliases[alias] = struct{}{}
			}
		}
	}
	seen := map[string]struct{}{}
	candidates := make([]projectIntegrationCandidate, 0, len(discovery.targets))
	for _, target := range discovery.targets {
		key := automaticProviderReferenceKey(target.provider, target.ref)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		bound := false
		for _, env := range project.Spec.Environments {
			for _, binding := range env.Bindings {
				if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || automaticProviderReferenceKey(binding.Provider, binding.ResourceRef) != key {
					continue
				}
				bound = true
				candidates = append(candidates, projectIntegrationCandidate{
					Environment: env.Name, Alias: binding.Name, Provider: target.provider,
					Kind: aiv1alpha1.ProjectBindingKindProviderReference, ResourceRef: target.ref.DeepCopy(),
					Actions: append([]providerCatalogAction(nil), target.catalogActions...), Phase: "Available",
				})
			}
		}
		if bound {
			continue
		}
		alias := automaticProviderIntegrationAlias(target.provider, target.ref, usedAliases)
		usedAliases[strings.ToLower(alias)] = struct{}{}
		actions := append([]providerCatalogAction(nil), target.catalogActions...)
		candidates = append(candidates, projectIntegrationCandidate{
			Environment: automaticProviderIntegrationEnvironment,
			Alias:       alias,
			Provider:    target.provider,
			Kind:        aiv1alpha1.ProjectBindingKindProviderReference,
			ResourceRef: target.ref.DeepCopy(),
			Actions:     actions,
			Phase:       "Available",
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if left.ResourceRef.Kind != right.ResourceRef.Kind {
			return left.ResourceRef.Kind < right.ResourceRef.Kind
		}
		if left.ResourceRef.Name != right.ResourceRef.Name {
			return left.ResourceRef.Name < right.ResourceRef.Name
		}
		if left.Environment != right.Environment {
			return left.Environment < right.Environment
		}
		return left.Alias < right.Alias
	})
	return candidates
}

func materializeDiscoveredAutomaticProjectIntegrations(ctx context.Context, c *asclient.Client, project *aiv1alpha1.Project, targets []automaticIntegrationTarget) (*aiv1alpha1.Project, error) {
	if c == nil || project == nil || len(targets) == 0 {
		return project, nil
	}
	next := project.DeepCopy()
	materializable := make([]automaticIntegrationTarget, 0, len(targets))
	for _, target := range targets {
		if len(target.actions) > 0 {
			materializable = append(materializable, target)
		}
	}
	changed := materializeAutomaticIntegrationTargets(next, materializable)
	if !changed {
		// Automatic discovery only writes the Project's providerReference
		// bindings. Provider-owned resources are converged by the Project
		// controller; this turn-start path must never create or update them.
		return project, nil
	}
	var lastErr error
	for attempt := 0; attempt < automaticIntegrationUpdateAttempts; attempt++ {
		updated, err := c.Projects().Update(ctx, next, metav1.UpdateOptions{})
		if err == nil {
			// Do not synchronously reconcile provider resources here. The controller is
			// the sole owner of provider-resource writes, while read-through status
			// remains available to subsequent project/integration reads.
			return updated, nil
		}
		lastErr = err
		if !apierrors.IsConflict(err) || attempt == automaticIntegrationUpdateAttempts-1 {
			break
		}

		// The initial Project may have gone stale while automatic discovery was
		// listing provider resources. Merge only our binding materialization into
		// the latest object so concurrent user/controller changes to the rest of
		// the Project survive the retry.
		next, err = c.Projects().Get(ctx, project.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("persist automatic provider integrations: %w", err)
		}
		if !materializeAutomaticIntegrationTargets(next, targets) {
			return next, nil
		}
	}
	return nil, fmt.Errorf("persist automatic provider integrations: %w", lastErr)
}

func automaticProviderCatalogResources(catalog []providerCatalogEntry) []automaticProviderCatalogResource {
	byKey := map[string]automaticProviderCatalogResource{}
	for _, provider := range catalog {
		providerName := strings.TrimSpace(provider.Name)
		if providerName == "" || !provider.Ready {
			continue
		}
		// The coordinate an action is discovered at is its PARENT resource's,
		// so the export is flattened once and every action arrives already
		// paired with the apiVersion, kind and plural it hangs off.
		for _, bound := range providerCatalogBoundActions(provider) {
			name, version, ok := automaticCatalogActionIdentity(bound.Action)
			if !ok {
				continue
			}
			gv, err := schema.ParseGroupVersion(bound.APIVersion)
			if err != nil || gv.Group == "" || gv.Version == "" {
				continue
			}
			key := automaticProviderCatalogResourceKey(providerName, gv.WithResource(bound.Resource), bound.Kind, bound.Resource)
			group := byKey[key]
			if group.provider == "" {
				group = automaticProviderCatalogResource{
					provider: providerName, apiVersion: bound.APIVersion, kind: bound.Kind, resource: bound.Resource,
					gvr: gv.WithResource(bound.Resource),
				}
			}
			group.actions = append(group.actions, automaticProviderCatalogAction{
				name: name, version: version, schemaDigest: strings.TrimSpace(bound.Action.SchemaDigest), catalog: bound.Action,
			})
			byKey[key] = group
		}
	}
	resources := make([]automaticProviderCatalogResource, 0, len(byKey))
	for _, resource := range byKey {
		// Catalog entries are external data. Keep one deterministic action
		// contract when a provider accidentally publishes the same ID twice.
		sort.Slice(resource.actions, func(i, j int) bool {
			left, right := resource.actions[i], resource.actions[j]
			if left.name != right.name {
				return left.name < right.name
			}
			if left.version != right.version {
				return left.version < right.version
			}
			return left.schemaDigest < right.schemaDigest
		})
		deduped := resource.actions[:0]
		seen := map[string]struct{}{}
		for _, action := range resource.actions {
			key := action.name + "\x00" + action.version
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			deduped = append(deduped, action)
		}
		resource.actions = deduped
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool {
		return automaticProviderCatalogResourceKey(resources[i].provider, resources[i].gvr, resources[i].kind, resources[i].resource) < automaticProviderCatalogResourceKey(resources[j].provider, resources[j].gvr, resources[j].kind, resources[j].resource)
	})
	return resources
}

func automaticCatalogActionIdentity(action providerCatalogAction) (string, string, bool) {
	name, version, err := normalizeIntegrationAction(action.Name, action.Version)
	if err != nil || !projectActionSchemaDigestRE.MatchString(strings.TrimSpace(action.SchemaDigest)) {
		return "", "", false
	}
	if action.Deprecation != nil && action.Deprecation.Deprecated {
		return "", "", false
	}
	return name, version, true
}

func automaticProviderCatalogResourceKey(provider string, gvr schema.GroupVersionResource, kind, resource string) string {
	return strings.TrimSpace(provider) + "\x00" + gvr.Group + "\x00" + gvr.Version + "\x00" + strings.TrimSpace(kind) + "\x00" + strings.TrimSpace(resource)
}

func automaticProviderReferenceKey(provider string, ref *aiv1alpha1.ProjectProviderResourceReference) string {
	if ref == nil {
		return ""
	}
	gvr, err := projectProviderResourceGVR(ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(provider) + "\x00" + gvr.Group + "\x00" + gvr.Version + "\x00" + strings.TrimSpace(ref.Kind) + "\x00" + gvr.Resource + "\x00" + strings.TrimSpace(ref.Name)
}

func materializeAutomaticIntegrationTargets(project *aiv1alpha1.Project, targets []automaticIntegrationTarget) bool {
	if project == nil || len(targets) == 0 {
		return false
	}
	usedAliases := map[string]struct{}{}
	for envIndex := range project.Spec.Environments {
		for bindingIndex := range project.Spec.Environments[envIndex].Bindings {
			binding := &project.Spec.Environments[envIndex].Bindings[bindingIndex]
			if alias := strings.ToLower(strings.TrimSpace(binding.Name)); alias != "" {
				usedAliases[alias] = struct{}{}
			}
		}
	}
	changed := false
	for _, target := range targets {
		key := automaticProviderReferenceKey(target.provider, target.ref)
		if key == "" {
			continue
		}
		if existing := findAutomaticIntegrationBinding(project, key); existing != nil {
			// Reuse the first binding for this exact target. Existing bindings
			// (including user-created aliases) retain their identity and audit.
			if mergeAutomaticIntegrationActions(existing, target.actions) {
				changed = true
			}
			continue
		}
		alias := automaticProviderIntegrationAlias(target.provider, target.ref, usedAliases)
		usedAliases[strings.ToLower(alias)] = struct{}{}
		binding := aiv1alpha1.ProjectProviderBindingSpec{
			Name: alias, Provider: target.provider, Kind: aiv1alpha1.ProjectBindingKindProviderReference,
			ResourceRef: target.ref.DeepCopy(), AllowedActions: cloneAutomaticIntegrationActions(target.actions),
		}
		env := ensureProjectIntegrationEnvironment(project, automaticProviderIntegrationEnvironment)
		env.Bindings = append(env.Bindings, binding)
		changed = true
	}
	return changed
}

func findAutomaticIntegrationBinding(project *aiv1alpha1.Project, targetKey string) *aiv1alpha1.ProjectProviderBindingSpec {
	if project == nil || targetKey == "" {
		return nil
	}
	for envIndex := range project.Spec.Environments {
		for bindingIndex := range project.Spec.Environments[envIndex].Bindings {
			binding := &project.Spec.Environments[envIndex].Bindings[bindingIndex]
			if binding.Kind == aiv1alpha1.ProjectBindingKindProviderReference && automaticProviderReferenceKey(binding.Provider, binding.ResourceRef) == targetKey {
				return binding
			}
		}
	}
	return nil
}

func mergeAutomaticIntegrationActions(binding *aiv1alpha1.ProjectProviderBindingSpec, desired []aiv1alpha1.ProjectProviderActionSpec) bool {
	if binding == nil {
		return false
	}
	byID := map[string]aiv1alpha1.ProjectProviderActionSpec{}
	for _, action := range binding.AllowedActions {
		byID[strings.ToLower(strings.TrimSpace(action.Name)+"\x00"+strings.TrimSpace(action.Version))] = action
	}
	changed := false
	for _, action := range desired {
		key := strings.ToLower(strings.TrimSpace(action.Name) + "\x00" + strings.TrimSpace(action.Version))
		prior, exists := byID[key]
		if !exists {
			byID[key] = automaticIntegrationActionWithAudit(action, automaticProviderActionTimestamp())
			changed = true
			continue
		}
		if prior.Revoked {
			// Revocations are durable operator intent. Automatic discovery must
			// never silently reactivate one.
			continue
		}
		if prior.GrantedBy == automaticProviderActionGrantedBy && prior.SchemaDigest != action.SchemaDigest {
			prior.SchemaDigest = action.SchemaDigest
			prior.GrantedAt = automaticProviderActionTimestamp()
			prior.RevokedBy = ""
			prior.RevokedAt = nil
			byID[key] = prior
			changed = true
			continue
		}
		// Keep an existing active grant's audit fields and digest. This is
		// what makes repeated turns idempotent and preserves user-created
		// grants that predate automatic materialization.
	}
	merged := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(byID))
	for _, action := range byID {
		merged = append(merged, action)
	}
	sort.Slice(merged, func(i, j int) bool {
		left, right := strings.ToLower(merged[i].Name), strings.ToLower(merged[j].Name)
		if left != right {
			return left < right
		}
		return strings.ToLower(merged[i].Version) < strings.ToLower(merged[j].Version)
	})
	if !reflect.DeepEqual(binding.AllowedActions, merged) {
		binding.AllowedActions = merged
		changed = true
	}
	return changed
}

func cloneAutomaticIntegrationActions(actions []aiv1alpha1.ProjectProviderActionSpec) []aiv1alpha1.ProjectProviderActionSpec {
	cloned := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(actions))
	now := automaticProviderActionTimestamp()
	for _, action := range actions {
		cloned = append(cloned, automaticIntegrationActionWithAudit(action, now))
	}
	return cloned
}

func automaticIntegrationActionWithAudit(action aiv1alpha1.ProjectProviderActionSpec, grantedAt *metav1.Time) aiv1alpha1.ProjectProviderActionSpec {
	action.GrantedBy = automaticProviderActionGrantedBy
	action.GrantedAt = grantedAt.DeepCopy()
	action.Revoked = false
	action.RevokedBy = ""
	action.RevokedAt = nil
	return action
}

func automaticProviderActionTimestamp() *metav1.Time {
	now := metav1.Now()
	return &now
}

func automaticProviderIntegrationAlias(provider string, ref *aiv1alpha1.ProjectProviderResourceReference, used map[string]struct{}) string {
	key := automaticProviderReferenceKey(provider, ref)
	hash := sha256.Sum256([]byte(key))
	suffix := hex.EncodeToString(hash[:])[:10]
	parts := []string{provider, ref.Resource, ref.Name}
	slug := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		part = automaticIntegrationAliasPartRE.ReplaceAllString(part, "-")
		part = strings.Trim(part, "-_")
		if part != "" {
			slug = append(slug, part)
		}
	}
	base := strings.Join(slug, "-")
	if base == "" {
		base = "provider-resource"
	}
	maxBase := 63 - len(automaticProviderIntegrationPrefix) - 1 - len(suffix)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-_")
	}
	candidate := automaticProviderIntegrationPrefix + base + "-" + suffix
	for attempt := 1; ; attempt++ {
		if _, exists := used[strings.ToLower(candidate)]; !exists && projectIntegrationIdentifierRE.MatchString(candidate) {
			return candidate
		}
		attemptHash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", key, attempt)))
		attemptSuffix := hex.EncodeToString(attemptHash[:])[:10]
		candidate = automaticProviderIntegrationPrefix + base + "-" + attemptSuffix
	}
}
