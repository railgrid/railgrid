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

// Package reconciler reconciles workloads on the agent side: it watches the
// edge's Placements in the tenant workspace and materializes each as a local
// Deployment.
//
// The edges workload types (Workload/Placement, group
// edges.kedge.faros.sh) live in the standalone edges provider module. To keep
// the core agent independent of that provider, this reconciler reads them as
// unstructured objects and decodes only the fields it needs into local mirror
// structs — no cross-module import.
package reconciler

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const controllerName = "workload-reconciler"

// resyncPeriod for the Placement informer.
const resyncPeriod = 10 * time.Minute

// Group/version/labels for the edges provider's workload types, mirrored here so
// the agent needs no import of the provider module.
const (
	edgesGroup   = "edges.kedge.faros.sh"
	edgesVersion = "v1alpha1"

	labelEdge      = edgesGroup + "/edge"
	labelWorkload  = edgesGroup + "/workload"
	labelPlacement = edgesGroup + "/placement"
)

var (
	placementGVR = schema.GroupVersionResource{Group: edgesGroup, Version: edgesVersion, Resource: "placements"}
	workloadGVR  = schema.GroupVersionResource{Group: edgesGroup, Version: edgesVersion, Resource: "workloads"}
)

// placementView is the subset of a Placement the agent reads.
type placementView struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              struct {
		WorkloadRef corev1.ObjectReference `json:"workloadRef"`
		EdgeName    string                 `json:"edgeName"`
		Replicas    *int32                 `json:"replicas,omitempty"`
	} `json:"spec,omitempty"`
}

// workloadView is the subset of a Workload the agent reads.
type workloadView struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              struct {
		Simple   *simpleWorkload         `json:"simple,omitempty"`
		Template *corev1.PodTemplateSpec `json:"template,omitempty"`
		Replicas *int32                  `json:"replicas,omitempty"`
	} `json:"spec,omitempty"`
}

type simpleWorkload struct {
	Image     string                       `json:"image"`
	Ports     []corev1.ContainerPort       `json:"ports,omitempty"`
	Env       []corev1.EnvVar              `json:"env,omitempty"`
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	Command   []string                     `json:"command,omitempty"`
	Args      []string                     `json:"args,omitempty"`
}

// WorkloadReconciler watches the edge's Placements in the tenant workspace and
// materializes each as a local Deployment.
type WorkloadReconciler struct {
	edgeName         string
	hubDynamic       dynamic.Interface
	downstreamClient kubernetes.Interface
	queue            workqueue.TypedRateLimitingInterface[string]
}

// NewWorkloadReconciler creates a workload reconciler. hubDynamic is a dynamic
// client scoped to the edge's tenant workspace; downstreamClient targets the
// edge's local cluster.
func NewWorkloadReconciler(edgeName string, hubDynamic dynamic.Interface, downstreamClient kubernetes.Interface) *WorkloadReconciler {
	return &WorkloadReconciler{
		edgeName:         edgeName,
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
	logger.Info("Starting workload reconciler", "edgeName", r.edgeName)

	// Placement informer filtered to this edge.
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		r.hubDynamic, resyncPeriod, metav1.NamespaceAll,
		func(opts *metav1.ListOptions) {
			opts.LabelSelector = labelEdge + "=" + r.edgeName
		},
	)
	placementInformer := factory.ForResource(placementGVR).Informer()

	if _, err := placementInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { r.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { r.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { r.enqueue(obj) },
	}); err != nil {
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

	// Read the Placement.
	pu, err := r.hubDynamic.Resource(placementGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Placement deleted, cleaning up local deployment")
			return r.deleteLocalDeployment(ctx, name)
		}
		return err
	}
	var placement placementView
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pu.Object, &placement); err != nil {
		return fmt.Errorf("decoding placement %s/%s: %w", namespace, name, err)
	}

	// Only handle placements for our edge.
	if placement.Spec.EdgeName != r.edgeName {
		return nil
	}

	// Read the referenced Workload.
	vwRef := placement.Spec.WorkloadRef
	vu, err := r.hubDynamic.Resource(workloadGVR).Namespace(vwRef.Namespace).Get(ctx, vwRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting Workload %s/%s: %w", vwRef.Namespace, vwRef.Name, err)
	}
	var vw workloadView
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(vu.Object, &vw); err != nil {
		return fmt.Errorf("decoding Workload %s/%s: %w", vwRef.Namespace, vwRef.Name, err)
	}

	deployment, err := convertToDeployment(&vw, &placement)
	if err != nil {
		return fmt.Errorf("converting to deployment: %w", err)
	}

	existing, err := r.downstreamClient.AppsV1().Deployments(deployment.Namespace).Get(ctx, deployment.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logger.Info("Creating local deployment", "name", deployment.Name)
		_, err = r.downstreamClient.AppsV1().Deployments(deployment.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
		return err
	} else if err != nil {
		return err
	}

	logger.V(4).Info("Updating local deployment", "name", deployment.Name)
	existing.Spec = deployment.Spec
	existing.Labels = deployment.Labels
	existing.Annotations = deployment.Annotations
	_, err = r.downstreamClient.AppsV1().Deployments(deployment.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (r *WorkloadReconciler) deleteLocalDeployment(ctx context.Context, placementName string) error {
	deployments, err := r.downstreamClient.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{
		LabelSelector: labelPlacement + "=" + placementName,
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

// convertToDeployment converts a Workload + Placement to a local Deployment.
func convertToDeployment(vw *workloadView, placement *placementView) (*appsv1.Deployment, error) {
	var podSpec corev1.PodSpec
	switch {
	case vw.Spec.Template != nil:
		podSpec = vw.Spec.Template.Spec
	case vw.Spec.Simple != nil:
		podSpec = buildPodSpecFromSimple(vw.Spec.Simple)
	default:
		return nil, fmt.Errorf("workload must have either simple or template spec")
	}

	replicas := int32(1)
	if placement.Spec.Replicas != nil {
		replicas = *placement.Spec.Replicas
	} else if vw.Spec.Replicas != nil {
		replicas = *vw.Spec.Replicas
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vw.Name,
			Namespace: "default",
			Labels: map[string]string{
				labelWorkload:  vw.Name,
				labelPlacement: placement.Name,
			},
			Annotations: map[string]string{
				edgesGroup + "/placement-name":      placement.Name,
				edgesGroup + "/placement-namespace": placement.Namespace,
				edgesGroup + "/placement-uid":       string(placement.UID),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{labelWorkload: vw.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{labelWorkload: vw.Name},
				},
				Spec: podSpec,
			},
		},
	}, nil
}

func buildPodSpecFromSimple(simple *simpleWorkload) corev1.PodSpec {
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
	return corev1.PodSpec{Containers: []corev1.Container{container}}
}
