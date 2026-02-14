package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const controllerName = "scheduler"

// Scheduler watches VirtualWorkloads and Sites, creating Placements.
type Scheduler struct {
	client          *kedgeclient.Client
	informerFactory *kedgeclient.InformerFactory
	queue           workqueue.TypedRateLimitingInterface[string]
	vwInformer      cache.SharedIndexInformer
	siteInformer    cache.SharedIndexInformer
	placementInformer cache.SharedIndexInformer
}

// NewScheduler creates a new scheduler.
func NewScheduler(client *kedgeclient.Client, factory *kedgeclient.InformerFactory) *Scheduler {
	s := &Scheduler{
		client:          client,
		informerFactory: factory,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerName},
		),
		vwInformer:        factory.VirtualWorkloads(),
		siteInformer:      factory.Sites(),
		placementInformer: factory.Placements(),
	}

	// Watch VirtualWorkloads
	s.vwInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { s.enqueueVW(obj) },
		UpdateFunc: func(_, obj interface{}) { s.enqueueVW(obj) },
	})

	// Watch Sites - re-enqueue all VirtualWorkloads when sites change
	s.siteInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { s.enqueueAllVWs() },
		UpdateFunc: func(_, _ interface{}) { s.enqueueAllVWs() },
		DeleteFunc: func(_ interface{}) { s.enqueueAllVWs() },
	})

	return s
}

func (s *Scheduler) enqueueVW(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	s.queue.Add(key)
}

func (s *Scheduler) enqueueAllVWs() {
	for _, obj := range s.vwInformer.GetStore().List() {
		s.enqueueVW(obj)
	}
}

// Run starts the scheduler controller.
func (s *Scheduler) Run(ctx context.Context) error {
	defer utilruntime.HandleCrash()
	defer s.queue.ShutDown()

	logger := klog.FromContext(ctx).WithName(controllerName)
	logger.Info("Starting scheduler controller")

	for i := 0; i < 2; i++ {
		go wait.UntilWithContext(ctx, s.worker, time.Second)
	}

	<-ctx.Done()
	logger.Info("Shutting down scheduler controller")
	return nil
}

func (s *Scheduler) worker(ctx context.Context) {
	for s.processNextWorkItem(ctx) {
	}
}

func (s *Scheduler) processNextWorkItem(ctx context.Context) bool {
	key, quit := s.queue.Get()
	if quit {
		return false
	}
	defer s.queue.Done(key)

	if err := s.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("reconciling %q: %w", key, err))
		s.queue.AddRateLimited(key)
		return true
	}
	s.queue.Forget(key)
	return true
}

func (s *Scheduler) reconcile(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("key", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil // invalid key, don't retry
	}

	// Get VirtualWorkload from cache
	obj, exists, err := s.vwInformer.GetStore().GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		logger.V(4).Info("VirtualWorkload deleted, cleaning up placements")
		return s.cleanupPlacements(ctx, namespace, name)
	}

	vw, err := convertToVirtualWorkload(obj)
	if err != nil {
		return fmt.Errorf("converting to VirtualWorkload: %w", err)
	}

	// List all Sites
	siteObjs := s.siteInformer.GetStore().List()
	sites, err := convertToSites(siteObjs)
	if err != nil {
		return fmt.Errorf("converting sites: %w", err)
	}

	// Match and select sites
	matched, err := MatchSites(sites, vw.Spec.Placement)
	if err != nil {
		return fmt.Errorf("matching sites: %w", err)
	}
	selected := SelectSites(matched, vw.Spec.Placement.Strategy)

	logger.V(4).Info("Scheduling", "matched", len(matched), "selected", len(selected))

	// Get existing placements for this VW
	existingPlacements := s.getPlacementsForVW(namespace, name)

	// Build desired placement set
	desiredSites := make(map[string]bool)
	for _, site := range selected {
		desiredSites[site.Name] = true
	}

	// Delete placements for sites no longer selected
	for _, p := range existingPlacements {
		if !desiredSites[p.Spec.SiteName] {
			logger.Info("Deleting stale placement", "placement", p.Name, "site", p.Spec.SiteName)
			if err := s.client.Placements(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
				logger.Error(err, "Failed to delete placement", "name", p.Name)
			}
		}
	}

	// Create placements for newly selected sites
	existingSites := make(map[string]bool)
	for _, p := range existingPlacements {
		existingSites[p.Spec.SiteName] = true
	}

	for _, site := range selected {
		if existingSites[site.Name] {
			continue
		}

		placementName := fmt.Sprintf("%s-%s", name, site.Name)
		placement := &kedgev1alpha1.Placement{
			TypeMeta: metav1.TypeMeta{
				APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
				Kind:       "Placement",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      placementName,
				Namespace: namespace,
				Labels: map[string]string{
					"kedge.faros.sh/virtualworkload": name,
					"kedge.faros.sh/site":            site.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
						Kind:       "VirtualWorkload",
						Name:       vw.Name,
						UID:        vw.UID,
					},
				},
			},
			Spec: kedgev1alpha1.PlacementObjSpec{
				WorkloadRef: makeWorkloadRef(vw),
				SiteName:    site.Name,
				Replicas:    vw.Spec.Replicas,
			},
		}

		logger.Info("Creating placement", "placement", placementName, "site", site.Name)
		if _, err := s.client.Placements(namespace).Create(ctx, placement, metav1.CreateOptions{}); err != nil {
			logger.Error(err, "Failed to create placement", "name", placementName)
		}
	}

	return nil
}

func (s *Scheduler) cleanupPlacements(ctx context.Context, namespace, vwName string) error {
	placements := s.getPlacementsForVW(namespace, vwName)
	for _, p := range placements {
		if err := s.client.Placements(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
			klog.FromContext(ctx).Error(err, "Failed to delete placement during cleanup", "name", p.Name)
		}
	}
	return nil
}

func (s *Scheduler) getPlacementsForVW(namespace, vwName string) []kedgev1alpha1.Placement {
	var result []kedgev1alpha1.Placement
	for _, obj := range s.placementInformer.GetStore().List() {
		p, err := convertToPlacement(obj)
		if err != nil {
			continue
		}
		if p.Namespace == namespace && p.Labels["kedge.faros.sh/virtualworkload"] == vwName {
			result = append(result, *p)
		}
	}
	return result
}

func makeWorkloadRef(vw *kedgev1alpha1.VirtualWorkload) corev1.ObjectReference {
	return corev1.ObjectReference{
		APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
		Kind:       "VirtualWorkload",
		Name:       vw.Name,
		Namespace:  vw.Namespace,
		UID:        vw.UID,
	}
}

// MatchSites returns sites matching the given placement spec.
func MatchSites(sites []kedgev1alpha1.Site, placement kedgev1alpha1.PlacementSpec) ([]kedgev1alpha1.Site, error) {
	if placement.SiteSelector == nil {
		return sites, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(placement.SiteSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid site selector: %w", err)
	}

	var matched []kedgev1alpha1.Site
	for _, site := range sites {
		if selector.Matches(labels.Set(site.Labels)) {
			matched = append(matched, site)
		}
	}
	return matched, nil
}

// SelectSites applies the placement strategy to matched sites.
func SelectSites(matched []kedgev1alpha1.Site, strategy kedgev1alpha1.PlacementStrategy) []kedgev1alpha1.Site {
	switch strategy {
	case kedgev1alpha1.PlacementStrategySingleton:
		if len(matched) > 0 {
			return matched[:1]
		}
		return nil
	case kedgev1alpha1.PlacementStrategySpread:
		return matched
	default:
		return matched
	}
}

func convertToVirtualWorkload(obj interface{}) (*kedgev1alpha1.VirtualWorkload, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("expected *unstructured.Unstructured, got %T", obj)
	}
	var vw kedgev1alpha1.VirtualWorkload
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &vw); err != nil {
		return nil, err
	}
	return &vw, nil
}

func convertToSites(objs []interface{}) ([]kedgev1alpha1.Site, error) {
	var sites []kedgev1alpha1.Site
	for _, obj := range objs {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		var site kedgev1alpha1.Site
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &site); err != nil {
			continue
		}
		sites = append(sites, site)
	}
	return sites, nil
}

func convertToPlacement(obj interface{}) (*kedgev1alpha1.Placement, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("expected *unstructured.Unstructured, got %T", obj)
	}
	data, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}
	var p kedgev1alpha1.Placement
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
