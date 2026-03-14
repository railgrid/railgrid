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

package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// inClusterNamespace is the namespace where the agent kubeconfig Secret is stored.
	inClusterNamespace = "kedge-system"
	// kubeconfigSecretKey is the data key within the Secret.
	kubeconfigSecretKey = "kubeconfig"
)

// IsInCluster returns true when the process is running inside a Kubernetes Pod.
func IsInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// AgentKubeconfigSecretName returns the name of the Secret used to persist
// the hub kubeconfig for the given edge when running in-cluster.
func AgentKubeconfigSecretName(edgeName string) string {
	return "kedge-agent-" + edgeName + "-kubeconfig"
}

// newInClusterKubernetesClient builds a Kubernetes clientset using in-cluster
// service-account credentials.
func newInClusterKubernetesClient() (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("building in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating in-cluster kubernetes client: %w", err)
	}
	return cs, nil
}

// decodeKubeconfigB64 decodes a base64-encoded kubeconfig string as returned
// by the hub's token-exchange header.
func decodeKubeconfigB64(kubeconfigB64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(kubeconfigB64)
	if err != nil {
		return "", fmt.Errorf("decoding base64 kubeconfig: %w", err)
	}
	return string(b), nil
}

// LoadKubeconfigFromSecret reads the hub kubeconfig from the in-cluster Secret.
// Returns ("", nil) when the Secret does not exist yet (first boot before token exchange).
func LoadKubeconfigFromSecret(edgeName string) (string, error) {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return "", err
	}
	secretName := AgentKubeconfigSecretName(edgeName)
	secret, err := cs.CoreV1().Secrets(inClusterNamespace).Get(
		context.Background(),
		secretName,
		metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting kubeconfig secret: %w", err)
	}
	data, ok := secret.Data[kubeconfigSecretKey]
	if !ok || len(data) == 0 {
		return "", nil
	}
	return string(data), nil
}

// SaveKubeconfigToSecret writes the hub kubeconfig to the in-cluster Secret so
// that it survives a pod restart.
func SaveKubeconfigToSecret(edgeName, kubeconfigData string) error {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return err
	}
	secretName := AgentKubeconfigSecretName(edgeName)
	existing, err := cs.CoreV1().Secrets(inClusterNamespace).Get(
		context.Background(),
		secretName,
		metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().Secrets(inClusterNamespace).Create(
			context.Background(),
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: inClusterNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					kubeconfigSecretKey: []byte(kubeconfigData),
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			return fmt.Errorf("creating kubeconfig secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting kubeconfig secret: %w", err)
	}
	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	existing.Data[kubeconfigSecretKey] = []byte(kubeconfigData)
	_, err = cs.CoreV1().Secrets(inClusterNamespace).Update(
		context.Background(),
		existing,
		metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating kubeconfig secret: %w", err)
	}
	return nil
}
