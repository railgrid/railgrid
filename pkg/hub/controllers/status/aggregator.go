package status

import (
	"context"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"k8s.io/klog/v2"
)

// Aggregator watches Placement status updates and aggregates them
// into the parent VirtualWorkload status.
type Aggregator struct{}

// NewAggregator creates a new status aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{}
}

// Run starts the status aggregator controller.
func (a *Aggregator) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting status aggregator controller")

	// TODO: Watch Placement status updates
	// TODO: Aggregate into parent VirtualWorkload status

	<-ctx.Done()
	return nil
}

// AggregateStatus computes the VirtualWorkload status from its placements.
func AggregateStatus(placements []kedgev1alpha1.Placement) kedgev1alpha1.VirtualWorkloadStatus {
	status := kedgev1alpha1.VirtualWorkloadStatus{
		Phase: kedgev1alpha1.VirtualWorkloadPhasePending,
	}

	var totalReady int32
	var allRunning bool = true

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
