package client

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

const (
	// DefaultResyncPeriod is the default resync period for informers.
	DefaultResyncPeriod = 10 * time.Minute
)

// InformerFactory wraps a dynamic shared informer factory and provides
// convenience methods for getting informers for kedge resources.
type InformerFactory struct {
	factory dynamicinformer.DynamicSharedInformerFactory
}

// NewInformerFactory creates a new InformerFactory for kedge resources.
func NewInformerFactory(client dynamic.Interface, resyncPeriod time.Duration) *InformerFactory {
	return &InformerFactory{
		factory: dynamicinformer.NewDynamicSharedInformerFactory(client, resyncPeriod),
	}
}

// NewFilteredInformerFactory creates a new InformerFactory with list option tweaks.
func NewFilteredInformerFactory(client dynamic.Interface, resyncPeriod time.Duration, namespace string, tweakListOptions dynamicinformer.TweakListOptionsFunc) *InformerFactory {
	return &InformerFactory{
		factory: dynamicinformer.NewFilteredDynamicSharedInformerFactory(client, resyncPeriod, namespace, tweakListOptions),
	}
}

// ForResource returns a SharedIndexInformer for a specific GVR.
func (f *InformerFactory) ForResource(gvr schema.GroupVersionResource) informers.GenericInformer {
	return f.factory.ForResource(gvr)
}

// VirtualWorkloads returns the informer for VirtualWorkload resources.
func (f *InformerFactory) VirtualWorkloads() cache.SharedIndexInformer {
	return f.factory.ForResource(VirtualWorkloadGVR).Informer()
}

// Sites returns the informer for Site resources.
func (f *InformerFactory) Sites() cache.SharedIndexInformer {
	return f.factory.ForResource(SiteGVR).Informer()
}

// Placements returns the informer for Placement resources.
func (f *InformerFactory) Placements() cache.SharedIndexInformer {
	return f.factory.ForResource(PlacementGVR).Informer()
}

// Start starts all informers.
func (f *InformerFactory) Start(stopCh <-chan struct{}) {
	f.factory.Start(stopCh)
}

// WaitForCacheSync waits for all informer caches to sync.
func (f *InformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
	return f.factory.WaitForCacheSync(stopCh)
}
