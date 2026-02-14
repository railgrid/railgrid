package scheduler

import (
	"testing"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMatchSites(t *testing.T) {
	sites := []kedgev1alpha1.Site{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "site-1",
				Labels: map[string]string{"env": "prod", "region": "us-east"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "site-2",
				Labels: map[string]string{"env": "prod", "region": "eu-west"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "site-3",
				Labels: map[string]string{"env": "staging", "region": "us-east"},
			},
		},
	}

	tests := []struct {
		name      string
		placement kedgev1alpha1.PlacementSpec
		wantCount int
	}{
		{
			name:      "nil selector matches all",
			placement: kedgev1alpha1.PlacementSpec{},
			wantCount: 3,
		},
		{
			name: "match prod env",
			placement: kedgev1alpha1.PlacementSpec{
				SiteSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				},
			},
			wantCount: 2,
		},
		{
			name: "match specific region",
			placement: kedgev1alpha1.PlacementSpec{
				SiteSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"region": "us-east"},
				},
			},
			wantCount: 2,
		},
		{
			name: "match prod + us-east",
			placement: kedgev1alpha1.PlacementSpec{
				SiteSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod", "region": "us-east"},
				},
			},
			wantCount: 1,
		},
		{
			name: "no match",
			placement: kedgev1alpha1.PlacementSpec{
				SiteSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "dev"},
				},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := MatchSites(sites, tt.placement)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(matched) != tt.wantCount {
				t.Errorf("got %d matched sites, want %d", len(matched), tt.wantCount)
			}
		})
	}
}

func TestSelectSites_Spread(t *testing.T) {
	sites := []kedgev1alpha1.Site{
		{ObjectMeta: metav1.ObjectMeta{Name: "site-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "site-2"}},
	}

	selected := SelectSites(sites, kedgev1alpha1.PlacementStrategySpread)
	if len(selected) != 2 {
		t.Errorf("Spread: got %d sites, want 2", len(selected))
	}
}

func TestSelectSites_Singleton(t *testing.T) {
	sites := []kedgev1alpha1.Site{
		{ObjectMeta: metav1.ObjectMeta{Name: "site-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "site-2"}},
	}

	selected := SelectSites(sites, kedgev1alpha1.PlacementStrategySingleton)
	if len(selected) != 1 {
		t.Errorf("Singleton: got %d sites, want 1", len(selected))
	}
}

func TestSelectSites_Empty(t *testing.T) {
	selected := SelectSites(nil, kedgev1alpha1.PlacementStrategySingleton)
	if len(selected) != 0 {
		t.Errorf("Empty: got %d sites, want 0", len(selected))
	}
}
