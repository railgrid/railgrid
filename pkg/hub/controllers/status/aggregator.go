package status

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const controllerName = "status-aggregator"

// Aggregator watches Placement status updates and aggregates them
// into the parent VirtualWorkload status.
type Aggregator struct {
	client            *kedgeclient.Client
	informerFactory   *kedgeclient.InformerFactory
	queue             workqueue.TypedRateLimitingInterface[string]
	placementInformer cache.SharedIndexInformer
	vwInformer        cache.SharedIndexInformer
}

// NewAggregator creates a new status aggregator.
func NewAggregator(client *kedgeclient.Client, factory *kedgeclient.InformerFactory) *Aggregator {
	a := &Aggregator{
		client:          client,
		informerFactory: factory,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerName},
		),
		placementInformer: factory.Placements(),
		vwInformer:        factory.VirtualWorkloads(),
	}

	// Watch Placement status changes - enqueue the parent VirtualWorkload
	a.placementInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { a.enqueuePlacement(obj) },
		UpdateFunc: func(_, obj interface{}) { a.enqueuePlacement(obj) },
		DeleteFunc: func(obj interface{}) { a.enqueuePlacement(obj) },
	})

	return a
}

func (a *Aggregator) enqueuePlacement(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	// Get the VirtualWorkload name from labels
	vwName, ok := u.GetLabels()["kedge.faros.sh/virtualworkload"]
	if !ok {
		return
	}

	ns := u.GetNamespace()
	key := ns + "/" + vwName
	a.queue.Add(key)
}

// Run starts the status aggregator controller.
func (a *Aggregator) Run(ctx context.Context) error {
	defer utilruntime.HandleCrash()
	defer a.queue.ShutDown()

	logger := klog.FromContext(ctx).WithName(controllerName)
	logger.Info("Starting status aggregator controller")

	for i := 0; i < 2; i++ {
		go wait.UntilWithContext(ctx, a.worker, time.Second)
	}

	<-ctx.Done()
	logger.Info("Shutting down status aggregator controller")
	return nil
}

func (a *Aggregator) worker(ctx context.Context) {
	for a.processNextWorkItem(ctx) {
	}
}

func (a *Aggregator) processNextWorkItem(ctx context.Context) bool {
	key, quit := a.queue.Get()
	if quit {
		return false
	}
	defer a.queue.Done(key)

	if err := a.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("reconciling %q: %w", key, err))
		a.queue.AddRateLimited(key)
		return true
	}
	a.queue.Forget(key)
	return true
}

func (a *Aggregator) reconcile(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("key", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil
	}

	// Check VirtualWorkload exists
	_, exists, err := a.vwInformer.GetStore().GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	// List all Placements for this VW
	var placements []kedgev1alpha1.Placement
	for _, obj := range a.placementInformer.GetStore().List() {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if u.GetNamespace() != namespace {
			continue
		}
		vwLabel, _ := u.GetLabels()["kedge.faros.sh/virtualworkload"]
		if vwLabel != name {
			continue
		}

		var p kedgev1alpha1.Placement
		data, marshalErr := json.Marshal(u.Object)
		if marshalErr != nil {
			continue
		}
		if json.Unmarshal(data, &p) != nil {
			continue
		}
		placements = append(placements, p)
	}

	// Aggregate status
	status := AggregateStatus(placements)

	// Patch VirtualWorkload status
	statusData, err := json.Marshal(map[string]interface{}{
		"status": status,
	})
	if err != nil {
		return fmt.Errorf("marshaling status: %w", err)
	}

	logger.V(4).Info("Updating VirtualWorkload status", "readyReplicas", status.ReadyReplicas, "phase", status.Phase)
	_, err = a.client.VirtualWorkloads(namespace).Patch(ctx, name, types.MergePatchType, statusData, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("patching VirtualWorkload status: %w", err)
	}

	return nil
}

// AggregateStatus computes the VirtualWorkload status from its placements.
func AggregateStatus(placements []kedgev1alpha1.Placement) kedgev1alpha1.VirtualWorkloadStatus {
	status := kedgev1alpha1.VirtualWorkloadStatus{
		Phase: kedgev1alpha1.VirtualWorkloadPhasePending,
	}

	var totalReady int32
	allRunning := true

	for _, p := range placements {
		totalReady += p.Status.ReadyReplicas

		siteStatus := kedgev1alpha1.SiteWorkloadStatus{
			SiteName:      p.Spec.SiteName,
			Phase:         p.Status.Phase,
			ReadyReplicas: p.Status.ReadyReplicas,
		}
		status.Sites = append(status.Sites, siteStatus)

		if p.Status.Phase != "Running" {
			allRunning = false
		}
	}

	status.ReadyReplicas = totalReady
	status.AvailableReplicas = totalReady

	if len(placements) > 0 && allRunning {
		status.Phase = kedgev1alpha1.VirtualWorkloadPhaseRunning
	} else if len(placements) > 0 {
		status.Phase = kedgev1alpha1.VirtualWorkloadPhasePending
	}

	return status
}
