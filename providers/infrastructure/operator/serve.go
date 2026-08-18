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
	"maps"
	"os"
	"slices"
	"strings"

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
const ServeNamespace = "faros-infrastructure-provider"

const (
	providerKubeconfigMount = "/var/run/secrets/faros/provider/kubeconfig"
	runtimeKubeconfigMount  = "/var/run/secrets/faros/runtime/kubeconfig"
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
		{Name: "FAROS_PROVIDER_NAME", Value: "infrastructure"},
		{Name: "INFRASTRUCTURE_KUBECONFIG", Value: providerKubeconfigMount},
	}
	if cr.Spec.ProviderWorkspace != "" {
		// The mounted kubeconfig may be root-scoped (the supplied-admin flow,
		// where spec.providerWorkspace is set; hub-minted kubeconfigs are
		// already workspace-scoped and leave it empty). Without this, serve's
		// controllers watch the root cluster, where the platform kinds don't
		// exist — the Template cache never syncs and instances never
		// reconcile or finalize.
		env = append(env, corev1.EnvVar{Name: "INFRASTRUCTURE_WORKSPACE_PATH", Value: cr.Spec.ProviderWorkspace})
	}
	if cr.Spec.Hub.URL != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_HUB_URL", Value: cr.Spec.Hub.URL})
	}
	if cr.Spec.Hub.Insecure {
		env = append(env, corev1.EnvVar{Name: "FAROS_HUB_INSECURE", Value: "true"})
	}
	// Template-generic publishing access layer. Keep Application fields as a
	// compatibility fallback while operators move to spec.publishing.
	publishingBaseDomain := cr.Spec.Publishing.BaseDomain
	if publishingBaseDomain == "" {
		publishingBaseDomain = cr.Spec.Application.BaseDomain
	}
	if publishingBaseDomain != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_APP_BASE_DOMAIN", Value: publishingBaseDomain})
	}
	if cr.Spec.Publishing.AccessProxyImage != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_ACCESS_PROXY_IMAGE", Value: cr.Spec.Publishing.AccessProxyImage})
	}
	publishingHubURL := cr.Spec.Publishing.HubURL
	if publishingHubURL == "" {
		publishingHubURL = cr.Spec.Hub.URL
	}
	if publishingHubURL != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_ACCESS_HUB_URL", Value: publishingHubURL})
	}
	if cr.Spec.Publishing.HubPublicURL != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_ACCESS_HUB_PUBLIC_URL", Value: cr.Spec.Publishing.HubPublicURL})
	}
	if cr.Spec.Publishing.HubInsecure {
		env = append(env, corev1.EnvVar{Name: "FAROS_ACCESS_HUB_INSECURE", Value: "true"})
	}
	if cr.Spec.Publishing.PublicScheme != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_ACCESS_PUBLIC_SCHEME", Value: cr.Spec.Publishing.PublicScheme})
	}
	if cr.Spec.Publishing.PublicPort > 0 {
		env = append(env, corev1.EnvVar{Name: "FAROS_APP_PUBLIC_PORT", Value: fmt.Sprintf("%d", cr.Spec.Publishing.PublicPort)})
	}
	publishingGatewayName := cr.Spec.Publishing.Gateway.Name
	if publishingGatewayName == "" {
		publishingGatewayName = cr.Spec.Application.Gateway.Name
	}
	if publishingGatewayName != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_GATEWAY_NAME", Value: publishingGatewayName})
	}
	publishingGatewayNamespace := cr.Spec.Publishing.Gateway.Namespace
	if publishingGatewayNamespace == "" {
		publishingGatewayNamespace = cr.Spec.Application.Gateway.Namespace
	}
	if publishingGatewayNamespace != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_GATEWAY_NAMESPACE", Value: publishingGatewayNamespace})
	}
	// Dev-mode image set (${faros.devAgentImage} / ${faros.devImage.*}); empty
	// values fall back to the in-binary defaults (node toolchain + agent).
	if cr.Spec.Development.AgentImage != "" {
		env = append(env, corev1.EnvVar{Name: "FAROS_DEV_AGENT_IMAGE", Value: cr.Spec.Development.AgentImage})
	}
	if verificationJWKS := strings.TrimSpace(os.Getenv("FAROS_PREVIEW_CONSOLE_VERIFICATION_JWKS")); verificationJWKS != "" {
		env = append(env, corev1.EnvVar{
			Name:  "FAROS_PREVIEW_CONSOLE_VERIFICATION_JWKS",
			Value: verificationJWKS,
		})
	}
	for _, toolchain := range slices.Sorted(maps.Keys(cr.Spec.Development.Images)) {
		if image := cr.Spec.Development.Images[toolchain]; image != "" {
			envName := "FAROS_DEV_IMAGE_" + strings.ToUpper(strings.ReplaceAll(toolchain, "-", "_"))
			env = append(env, corev1.EnvVar{Name: envName, Value: image})
		}
	}
	volMounts := []corev1.VolumeMount{
		{Name: "provider-kubeconfig", MountPath: "/var/run/secrets/faros/provider", ReadOnly: true},
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
		volMounts = append(volMounts, corev1.VolumeMount{Name: "runtime-kubeconfig", MountPath: "/var/run/secrets/faros/runtime", ReadOnly: true})
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
		env = append(env, corev1.EnvVar{Name: "FAROS_HUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: hubSecret},
				Key:                  key,
			},
		}})
	}

	image := cr.Spec.Provider.Image.Repository + ":" + cr.Spec.Provider.Image.Tag
	labels := map[string]string{"app.kubernetes.io/name": "faros-infrastructure-provider", "app.kubernetes.io/instance": name}

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

	crbName := "faros-infrastructure-serve-" + saName
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
