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

package main

import (
	"context"
	"fmt"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// inClusterNamespace is the namespace where the agent Secret is stored.
	inClusterNamespace = "kedge-system"
	// kubeconfigSecretKey is the data key in the Secret that holds the kubeconfig.
	kubeconfigSecretKey = "kubeconfig"
)

// isInCluster returns true when the process is running inside a Kubernetes Pod
// (detected by the presence of the KUBERNETES_SERVICE_HOST environment variable).
func isInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// agentKubeconfigSecretName returns the name of the Secret used to persist
// the hub kubeconfig for the given edge.
func agentKubeconfigSecretName(edgeName string) string {
	return "kedge-agent-" + edgeName + "-kubeconfig"
}

// newInClusterKubernetesClient builds a Kubernetes clientset using the
// in-cluster service account credentials.
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

// loadKubeconfigFromSecret fetches the hub kubeconfig from the in-cluster Secret.
// Returns ("", nil) when the Secret exists but the kubeconfig key is empty or absent.
func loadKubeconfigFromSecret(edgeName string) (string, error) {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return "", err
	}
	secret, err := cs.CoreV1().Secrets(inClusterNamespace).Get(
		context.Background(),
		agentKubeconfigSecretName(edgeName),
		metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting kubeconfig secret: %w", err)
	}
	return string(secret.Data[kubeconfigSecretKey]), nil
}

// writeKubeconfigToTempFile writes kubeconfig content to a temp file under /tmp
// and returns the path. The file persists for the lifetime of the process.
func writeKubeconfigToTempFile(data string) (string, error) {
	f, err := os.CreateTemp("/tmp", "kedge-agent-kubeconfig-*.yaml")
	if err != nil {
		return "", fmt.Errorf("creating temp kubeconfig file: %w", err)
	}
	name := f.Name()
	_, writeErr := f.WriteString(data)
	closeErr := f.Close()
	if writeErr != nil {
		return "", fmt.Errorf("writing temp kubeconfig file: %w", writeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("closing temp kubeconfig file: %w", closeErr)
	}
	return name, nil
}
