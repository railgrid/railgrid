package scheduler

import (
	"fmt"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
