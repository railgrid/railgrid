package reconciler

import (
	"context"
	"fmt"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// WorkloadReconciler watches Placements on the hub and creates local Deployments.
type WorkloadReconciler struct {
	siteName string
}

// NewWorkloadReconciler creates a new workload reconciler.
func NewWorkloadReconciler(siteName string) *WorkloadReconciler {
	return &WorkloadReconciler{siteName: siteName}
}

// Run starts the workload reconciler.
func (r *WorkloadReconciler) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting workload reconciler", "siteName", r.siteName)

	// TODO: Watch Placements on hub where spec.siteName == r.siteName
	// TODO: For each Placement, fetch VirtualWorkload and convert to local Deployment

	<-ctx.Done()
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
				"kedge.faros.sh/placement-uid": string(placement.UID),
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
