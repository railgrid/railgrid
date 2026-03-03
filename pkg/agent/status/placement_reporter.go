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

package status

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

const (
	placementReporterName = "placement-status-reporter"
	// PlacementLabel is the label used to identify deployments managed by kedge.
	PlacementLabel = "kedge.faros.sh/placement"
)

// PlacementReporter watches local Deployments and reports their status back
// to the corresponding Placement resources on the hub.
type PlacementReporter struct {
	hubClient        *kedgeclient.Client
	downstreamClient kubernetes.Interface
	deploymentLister appslisters.DeploymentLister
	deploymentSynced cache.InformerSynced
	queue            workqueue.TypedRateLimitingInterface[string]
}

// NewPlacementReporter creates a new PlacementReporter.
func NewPlacementReporter(
	hubClient *kedgeclient.Client,
	downstreamClient kubernetes.Interface,
	informerFactory informers.SharedInformerFactory,
) *PlacementReporter {
	deploymentInformer := informerFactory.Apps().V1().Deployments()

	r := &PlacementReporter{
		hubClient:        hubClient,
		downstreamClient: downstreamClient,
		deploymentLister: deploymentInformer.Lister(),
		deploymentSynced: deploymentInformer.Informer().HasSynced,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: placementReporterName},
		),
	}

	if _, err := deploymentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.enqueueDeployment,
		UpdateFunc: func(_, newObj interface{}) { r.enqueueDeployment(newObj) },
		DeleteFunc: r.enqueueDeployment,
	}); err != nil {
		panic(fmt.Sprintf("failed to add deployment event handler: %v", err))
	}

	return r
}

func (r *PlacementReporter) enqueueDeployment(obj interface{}) {
	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
			return
		}
		deployment, ok = tombstone.Obj.(*appsv1.Deployment)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("unexpected tombstone object type: %T", tombstone.Obj))
			return
		}
	}

	// Only process deployments managed by kedge
	if _, ok := deployment.Labels[PlacementLabel]; !ok {
		return
	}

	key, err := cache.MetaNamespaceKeyFunc(deployment)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	r.queue.Add(key)
}

// Run starts the placement status reporter.
func (r *PlacementReporter) Run(ctx context.Context, workers int) error {
	defer utilruntime.HandleCrash()
	defer r.queue.ShutDown()

	logger := klog.FromContext(ctx).WithName(placementReporterName)
	logger.Info("Starting placement status reporter")

	logger.Info("Waiting for caches to sync")
	if !cache.WaitForCacheSync(ctx.Done(), r.deploymentSynced) {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	logger.Info("Starting workers", "count", workers)
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, r.worker, time.Second)
	}

	<-ctx.Done()
	logger.Info("Shutting down placement status reporter")
	return nil
}

func (r *PlacementReporter) worker(ctx context.Context) {
	for r.processNextWorkItem(ctx) {
	}
}

func (r *PlacementReporter) processNextWorkItem(ctx context.Context) bool {
	key, quit := r.queue.Get()
	if quit {
		return false
	}
	defer r.queue.Done(key)

	if err := r.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("reconciling %q: %w", key, err))
		r.queue.AddRateLimited(key)
		return true
	}
	r.queue.Forget(key)
	return true
}

func (r *PlacementReporter) reconcile(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("key", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil
	}

	deployment, err := r.deploymentLister.Deployments(namespace).Get(name)
	if err != nil {
		// Deployment was deleted - we could mark placement as failed/pending,
		// but for now we'll let the hub-side controller handle cleanup
		logger.V(4).Info("Deployment not found, skipping status update")
		return nil
	}

	// Get the placement name from the label
	placementName, ok := deployment.Labels[PlacementLabel]
	if !ok {
		return nil
	}

	// Get the placement namespace from the annotation
	placementNamespace := deployment.Annotations["kedge.faros.sh/placement-namespace"]
	if placementNamespace == "" {
		placementNamespace = "default"
	}

	// Determine the phase based on deployment status
	phase := "Pending"
	if deployment.Status.AvailableReplicas > 0 {
		phase = "Running"
	} else if deployment.Status.ReadyReplicas > 0 {
		phase = "Synced"
	}

	// Build the status patch
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"phase":         phase,
			"readyReplicas": deployment.Status.ReadyReplicas,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling placement status patch: %w", err)
	}

	_, err = r.hubClient.Placements(placementNamespace).Patch(
		ctx,
		placementName,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
		"status",
	)
	if err != nil {
		return fmt.Errorf("updating placement status: %w", err)
	}

	logger.V(4).Info("Updated placement status",
		"placement", placementNamespace+"/"+placementName,
		"phase", phase,
		"readyReplicas", deployment.Status.ReadyReplicas,
	)

	return nil
}
