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

package reconciler

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

const controllerName = "workload-reconciler"

// WorkloadReconciler watches Placements on the hub and creates local Deployments.
type WorkloadReconciler struct {
	siteName         string
	hubClient        *kedgeclient.Client
	hubDynamic       dynamic.Interface
	downstreamClient kubernetes.Interface
	queue            workqueue.TypedRateLimitingInterface[string]
}

// NewWorkloadReconciler creates a new workload reconciler.
func NewWorkloadReconciler(siteName string, hubClient *kedgeclient.Client, hubDynamic dynamic.Interface, downstreamClient kubernetes.Interface) *WorkloadReconciler {
	return &WorkloadReconciler{
		siteName:         siteName,
		hubClient:        hubClient,
		hubDynamic:       hubDynamic,
		downstreamClient: downstreamClient,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerName},
		),
	}
}

// Run starts the workload reconciler.
func (r *WorkloadReconciler) Run(ctx context.Context) error {
	defer utilruntime.HandleCrash()
	defer r.queue.ShutDown()

	logger := klog.FromContext(ctx).WithName(controllerName)
	logger.Info("Starting workload reconciler", "siteName", r.siteName)

	// Create a filtered informer for Placements on the hub
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		r.hubDynamic,
		kedgeclient.DefaultResyncPeriod,
		metav1.NamespaceAll,
		func(opts *metav1.ListOptions) {
			opts.LabelSelector = "kedge.faros.sh/site=" + r.siteName
		},
	)

	placementInformer := factory.ForResource(kedgeclient.PlacementGVR).Informer()

	_, err := placementInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { r.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { r.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { r.enqueue(obj) },
	})
	if err != nil {
		return fmt.Errorf("adding event handler: %w", err)
	}

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	for i := 0; i < 2; i++ {
		go wait.UntilWithContext(ctx, r.worker, time.Second)
	}

	<-ctx.Done()
	logger.Info("Shutting down workload reconciler")
	return nil
}

func (r *WorkloadReconciler) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	r.queue.Add(key)
}

func (r *WorkloadReconciler) worker(ctx context.Context) {
	for r.processNextWorkItem(ctx) {
	}
}

func (r *WorkloadReconciler) processNextWorkItem(ctx context.Context) bool {
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

func (r *WorkloadReconciler) reconcile(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("key", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil
	}

	// Get placement from hub
	placement, err := r.hubClient.Placements(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Placement was deleted - delete the local deployment
			logger.Info("Placement deleted, cleaning up local deployment")
			return r.deleteLocalDeployment(ctx, namespace, name)
		}
		return err
	}

	// Only handle placements for our site
	if placement.Spec.SiteName != r.siteName {
		return nil
	}

	// Get VirtualWorkload from hub
	vwRef := placement.Spec.WorkloadRef
	vw, err := r.hubClient.VirtualWorkloads(vwRef.Namespace).Get(ctx, vwRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting VirtualWorkload %s/%s: %w", vwRef.Namespace, vwRef.Name, err)
	}

	// Convert to Deployment
	deployment, err := ConvertToDeployment(vw, placement)
	if err != nil {
		return fmt.Errorf("converting to deployment: %w", err)
	}

	// Apply to local cluster (create or update)
	existing, err := r.downstreamClient.AppsV1().Deployments(deployment.Namespace).Get(ctx, deployment.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logger.Info("Creating local deployment", "name", deployment.Name)
		_, err = r.downstreamClient.AppsV1().Deployments(deployment.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
		return err
	} else if err != nil {
		return err
	}

	// Update existing deployment
	logger.V(4).Info("Updating local deployment", "name", deployment.Name)
	existing.Spec = deployment.Spec
	existing.Labels = deployment.Labels
	existing.Annotations = deployment.Annotations
	_, err = r.downstreamClient.AppsV1().Deployments(deployment.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (r *WorkloadReconciler) deleteLocalDeployment(ctx context.Context, namespace, placementName string) error {
	// Find deployment by placement label
	deployments, err := r.downstreamClient.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{
		LabelSelector: "kedge.faros.sh/placement=" + placementName,
	})
	if err != nil {
		return err
	}
	for _, d := range deployments.Items {
		if err := r.downstreamClient.AppsV1().Deployments(d.Namespace).Delete(ctx, d.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// ConvertToDeployment converts a VirtualWorkload to a local Deployment.
func ConvertToDeployment(vw *kedgev1alpha1.VirtualWorkload, placement *kedgev1alpha1.Placement) (*appsv1.Deployment, error) {
	var podSpec corev1.PodSpec

	if vw.Spec.Template != nil {
		podSpec = vw.Spec.Template.Spec
	} else if vw.Spec.Simple != nil {
		podSpec = buildPodSpecFromSimple(vw.Spec.Simple)
	} else {
		return nil, fmt.Errorf("VirtualWorkload must have either simple or template spec")
	}

	replicas := int32(1)
	if placement.Spec.Replicas != nil {
		replicas = *placement.Spec.Replicas
	} else if vw.Spec.Replicas != nil {
		replicas = *vw.Spec.Replicas
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vw.Name,
			Namespace: "default",
			Labels: map[string]string{
				"kedge.faros.sh/workload":  vw.Name,
				"kedge.faros.sh/placement": placement.Name,
			},
			Annotations: map[string]string{
				"kedge.faros.sh/placement-name":      placement.Name,
				"kedge.faros.sh/placement-namespace": placement.Namespace,
				"kedge.faros.sh/placement-uid":       string(placement.UID),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"kedge.faros.sh/workload": vw.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kedge.faros.sh/workload": vw.Name,
					},
				},
				Spec: podSpec,
			},
		},
	}

	return deployment, nil
}

// buildPodSpecFromSimple converts a SimpleWorkloadSpec to a PodSpec.
func buildPodSpecFromSimple(simple *kedgev1alpha1.SimpleWorkloadSpec) corev1.PodSpec {
	container := corev1.Container{
		Name:  "main",
		Image: simple.Image,
		Ports: simple.Ports,
		Env:   simple.Env,
	}

	if simple.Resources != nil {
		container.Resources = *simple.Resources
	}
	if len(simple.Command) > 0 {
		container.Command = simple.Command
	}
	if len(simple.Args) > 0 {
		container.Args = simple.Args
	}

	return corev1.PodSpec{
		Containers: []corev1.Container{container},
	}
}
