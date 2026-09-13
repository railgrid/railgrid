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

package plugin

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// Each stateful provider gets its own small Postgres in the providers
// namespace, created once and kept across re-runs. One instance per provider
// (rather than a shared server with one database each) keeps it idempotent:
// Postgres creates POSTGRES_DB on first boot only, so adding a provider to an
// existing environment would otherwise need a separate migration step.
// Dev-only credentials; the data lives on a local-path PVC so a Docker restart
// keeps it.
const (
	devDBImage    = "postgres:16-alpine"
	devDBUser     = "faros"
	devDBPassword = "faros-dev"
	devDBPort     = 5432
)

func devDBName(provider string) string { return provider + "-db" }

// devDatabaseURL is the in-cluster connection URL for a provider's database.
func devDatabaseURL(provider, database string) string {
	return fmt.Sprintf("postgres://%s:%s@%s.%s.svc.cluster.local:%d/%s?sslmode=disable",
		devDBUser, devDBPassword, devDBName(provider), devProvidersNS, devDBPort, database)
}

// ensureProviderDatabase creates (if missing) the Postgres PVC, Deployment
// and Service for provider, waits until it accepts connections, and returns
// its connection URL.
func ensureProviderDatabase(ctx context.Context, clientset kubernetes.Interface, provider, database string, timeout time.Duration) (string, error) {
	name := devDBName(provider)
	labels := map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/component":  "database",
		"app.kubernetes.io/part-of":    provider,
		"app.kubernetes.io/managed-by": "faros-dev",
	}
	selector := map[string]string{"app.kubernetes.io/name": name}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: devProvidersNS, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	if _, err := clientset.CoreV1().PersistentVolumeClaims(devProvidersNS).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating PVC %s/%s: %w", devProvidersNS, name, err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: devProvidersNS, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports: []corev1.ServicePort{{
				Name: "postgres", Port: devDBPort, TargetPort: intstr.FromInt32(devDBPort),
			}},
		},
	}
	if _, err := clientset.CoreV1().Services(devProvidersNS).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating Service %s/%s: %w", devProvidersNS, name, err)
	}

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: devProvidersNS, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// One writer on an RWO volume: never run old and new side by side.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "postgres",
						Image: devDBImage,
						Env: []corev1.EnvVar{
							{Name: "POSTGRES_USER", Value: devDBUser},
							{Name: "POSTGRES_PASSWORD", Value: devDBPassword},
							{Name: "POSTGRES_DB", Value: database},
							// A subdirectory: the volume root holds lost+found on
							// some provisioners, which initdb refuses.
							{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"},
						},
						Ports: []corev1.ContainerPort{{Name: "postgres", ContainerPort: devDBPort}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
								Command: []string{"pg_isready", "-U", devDBUser, "-d", database},
							}},
							// First boot runs initdb and a temporary server
							// before the real one starts.
							InitialDelaySeconds: 5,
							PeriodSeconds:       5,
							FailureThreshold:    24,
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/postgresql/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: name,
						}},
					}},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(devProvidersNS).Create(ctx, deploy, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating Deployment %s/%s: %w", devProvidersNS, name, err)
	}

	if err := pollUntil(ctx, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		d, err := clientset.AppsV1().Deployments(devProvidersNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return d.Status.ReadyReplicas >= 1, nil
	}, func() error {
		return fmt.Errorf("postgres %s/%s did not become ready in %s", devProvidersNS, name, timeout)
	}); err != nil {
		return "", err
	}
	return devDatabaseURL(provider, database), nil
}
