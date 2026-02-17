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
