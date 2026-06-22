/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// ServeNamespace is the runtime-cluster namespace the operator deploys the
// provider serve workload into.
const ServeNamespace = "kedge-infrastructure-provider"

const (
	providerKubeconfigMount = "/var/run/secrets/kedge/provider/kubeconfig"
	runtimeKubeconfigMount  = "/var/run/secrets/kedge/runtime/kubeconfig"
)

// EnsureProviderServe replicates the provider + runtime kubeconfigs (and hub
// token) into the runtime cluster and create-or-updates the provider serve
// Deployment + Service there, with the image/replicas/port from the CR. The
// serve container runs `infrastructure-provider serve`, reading the provider
// kubeconfig (INFRASTRUCTURE_KUBECONFIG) for its controllers and the runtime
// kubeconfig (KRO_KUBECONFIG) for the kro backend.
func EnsureProviderServe(
	ctx context.Context,
	cs kubernetes.Interface,
	cr *v1alpha1.InfrastructureProvider,
	providerKubeconfig, runtimeKubeconfig, hubToken []byte,
) error {
	if err := ensureNamespace(ctx, cs, ServeNamespace); err != nil {
		return err
	}

	name := cr.Name
	providerSecret := name + "-provider-kubeconfig"
	if err := upsertOpaqueSecret(ctx, cs, ServeNamespace, providerSecret, "kubeconfig", providerKubeconfig); err != nil {
		return fmt.Errorf("replicate provider kubeconfig: %w", err)
	}

	// inCluster: the runtime is the operator's own cluster (no runtime
	// kubeconfig). The serve pod then runs the kro backend with its pod
	// ServiceAccount (in-cluster) instead of a mounted runtime kubeconfig.
	inCluster := len(runtimeKubeconfig) == 0

	port := cr.Spec.Provider.Port
	if port == 0 {
		port = 8081
	}
	replicas := cr.Spec.Provider.Replicas
	if replicas == 0 {
		replicas = 1
	}

	env := []corev1.EnvVar{
		{Name: "PORT", Value: fmt.Sprintf("%d", port)},
		{Name: "KEDGE_PROVIDER_NAME", Value: "infrastructure"},
		{Name: "INFRASTRUCTURE_KUBECONFIG", Value: providerKubeconfigMount},
	}
	if cr.Spec.Hub.URL != "" {
		env = append(env, corev1.EnvVar{Name: "KEDGE_HUB_URL", Value: cr.Spec.Hub.URL})
	}
	if cr.Spec.Hub.Insecure {
		env = append(env, corev1.EnvVar{Name: "KEDGE_HUB_INSECURE", Value: "true"})
	}
	// Application-template exposure layer. KEDGE_APP_BASE_DOMAIN also gates the
	// Application instance controller (it stays disabled when unset); KEDGE_INGRESS_CLASS
	// falls back to the in-binary "cloudflare" default when left empty.
	if cr.Spec.Application.BaseDomain != "" {
		env = append(env, corev1.EnvVar{Name: "KEDGE_APP_BASE_DOMAIN", Value: cr.Spec.Application.BaseDomain})
	}
	if cr.Spec.Application.IngressClass != "" {
		env = append(env, corev1.EnvVar{Name: "KEDGE_INGRESS_CLASS", Value: cr.Spec.Application.IngressClass})
	}
	volMounts := []corev1.VolumeMount{
		{Name: "provider-kubeconfig", MountPath: "/var/run/secrets/kedge/provider", ReadOnly: true},
	}
	volumes := []corev1.Volume{
		{Name: "provider-kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: providerSecret}}},
	}
	// Serve's kro backend reaches the runtime cluster either via a mounted
	// runtime kubeconfig (explicit runtime) or its in-cluster SA (in-cluster
	// runtime — KRO_KUBECONFIG left unset; controller_manager falls back to
	// in-cluster).
	serveSA := ""
	if !inCluster {
		runtimeSecret := name + "-runtime-kubeconfig"
		if err := upsertOpaqueSecret(ctx, cs, ServeNamespace, runtimeSecret, "kubeconfig", runtimeKubeconfig); err != nil {
			return fmt.Errorf("replicate runtime kubeconfig: %w", err)
		}
		env = append(env, corev1.EnvVar{Name: "KRO_KUBECONFIG", Value: runtimeKubeconfigMount})
		volMounts = append(volMounts, corev1.VolumeMount{Name: "runtime-kubeconfig", MountPath: "/var/run/secrets/kedge/runtime", ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: "runtime-kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: runtimeSecret}}})
	} else {
		// Give the serve pod an SA bound to the access its kro backend needs on
		// the (operator's own) runtime cluster.
		serveSA = name
		if err := ensureServeRBAC(ctx, cs, serveSA); err != nil {
			return fmt.Errorf("serve RBAC: %w", err)
		}
	}
	if cr.Spec.Hub.TokenSecret != nil && len(hubToken) > 0 {
		hubSecret := name + "-hub-token"
		key := cr.Spec.Hub.TokenSecret.Key
		if key == "" {
			key = "token"
		}
		if err := upsertOpaqueSecret(ctx, cs, ServeNamespace, hubSecret, key, hubToken); err != nil {
			return fmt.Errorf("replicate hub token: %w", err)
		}
		env = append(env, corev1.EnvVar{Name: "KEDGE_HUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: hubSecret},
				Key:                  key,
			},
		}})
	}

	image := cr.Spec.Provider.Image.Repository + ":" + cr.Spec.Provider.Image.Tag
	labels := map[string]string{"app.kubernetes.io/name": "kedge-infrastructure-provider", "app.kubernetes.io/instance": name}

	want := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ServeNamespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: serveSA,
					Containers: []corev1.Container{{
						Name:         "provider",
						Image:        image,
						Args:         []string{"serve"},
						Env:          env,
						Ports:        []corev1.ContainerPort{{ContainerPort: port, Name: "http"}},
						VolumeMounts: volMounts,
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(port)},
							},
							PeriodSeconds: 5,
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}

	existing, err := cs.AppsV1().Deployments(ServeNamespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Labels = want.Labels
		existing.Spec = want.Spec
		if _, uerr := cs.AppsV1().Deployments(ServeNamespace).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update serve Deployment: %w", uerr)
		}
	case apierrors.IsNotFound(err):
		if _, cerr := cs.AppsV1().Deployments(ServeNamespace).Create(ctx, want, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("create serve Deployment: %w", cerr)
		}
	default:
		return fmt.Errorf("get serve Deployment: %w", err)
	}

	return ensureServeService(ctx, cs, name, labels, port)
}

// ensureServeRBAC creates the serve pod's ServiceAccount (in ServeNamespace)
// and binds it to cluster-admin so its in-cluster kro backend can author
// RGD-defined instances, namespaces, and secrets on the runtime cluster. Used
// only for the in-cluster runtime (no runtime kubeconfig). Scope down for
// least privilege in hardened environments.
func ensureServeRBAC(ctx context.Context, cs kubernetes.Interface, saName string) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ServeNamespace}}
	if _, err := cs.CoreV1().ServiceAccounts(ServeNamespace).Get(ctx, saName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, cerr := cs.CoreV1().ServiceAccounts(ServeNamespace).Create(ctx, sa, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("create serve ServiceAccount: %w", cerr)
		}
	} else if err != nil {
		return fmt.Errorf("get serve ServiceAccount: %w", err)
	}

	crbName := "kedge-infrastructure-serve-" + saName
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: crbName},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: ServeNamespace}},
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, crbName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, cerr := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("create serve ClusterRoleBinding: %w", cerr)
		}
	} else if err != nil {
		return fmt.Errorf("get serve ClusterRoleBinding: %w", err)
	}
	return nil
}

func ensureServeService(ctx context.Context, cs kubernetes.Interface, name string, labels map[string]string, port int32) error {
	want := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ServeNamespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromInt32(port)}},
		},
	}
	existing, err := cs.CoreV1().Services(ServeNamespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Spec.Selector = want.Spec.Selector
		existing.Spec.Ports = want.Spec.Ports
		_, uerr := cs.CoreV1().Services(ServeNamespace).Update(ctx, existing, metav1.UpdateOptions{})
		return uerr
	case apierrors.IsNotFound(err):
		_, cerr := cs.CoreV1().Services(ServeNamespace).Create(ctx, want, metav1.CreateOptions{})
		if cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return cerr
		}
		return nil
	default:
		return err
	}
}
