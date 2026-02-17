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

// Package scheduler assigns workloads to sites.
package scheduler

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
)

const controllerName = "scheduler"

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
