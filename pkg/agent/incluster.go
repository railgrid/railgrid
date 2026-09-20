/*
Copyright 2026 The Railgrid Authors.

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
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/agent/tunnel"
)

const (
	// kubeconfigSecretKey is the data key within the Secret.
	kubeconfigSecretKey = "kubeconfig"
	// saNamespaceFile is the standard ServiceAccount namespace file mounted
	// into every Pod by the kubelet.
	saNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// IsInCluster returns true when the process is running inside a Kubernetes Pod.
func IsInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// inClusterNamespace returns the namespace this Pod is running in.
// Reads from POD_NAMESPACE if set (downward API), otherwise from the
// ServiceAccount namespace file mounted into every Pod.
func inClusterNamespace() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	data, err := os.ReadFile(saNamespaceFile)
	if err != nil {
		return "", fmt.Errorf("reading pod namespace from %s: %w", saNamespaceFile, err)
	}
	ns := string(data)
	if ns == "" {
		return "", fmt.Errorf("pod namespace at %s is empty", saNamespaceFile)
	}
	return ns, nil
}

// AgentKubeconfigSecretName returns the name of the Secret used to persist
// the hub kubeconfig for the given edge when running in-cluster.
func AgentKubeconfigSecretName(edgeName string) string {
	return "railgrid-agent-" + edgeName + "-kubeconfig"
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

// LoadKubeconfigFromSecret reads the hub kubeconfig from the in-cluster Secret.
// Returns ("", nil) when the Secret does not exist yet (first boot before token exchange).
func LoadKubeconfigFromSecret(edgeName string) (string, error) {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return "", err
	}
	ns, err := inClusterNamespace()
	if err != nil {
		return "", err
	}
	secretName := AgentKubeconfigSecretName(edgeName)
	secret, err := cs.CoreV1().Secrets(ns).Get(
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

// credentialSecretKey is where the enrolment bundle lives inside the agent's
// own Secret. It sits beside the (now unused) kubeconfig key rather than
// replacing it, so a rollback does not have to migrate the Secret back.
const credentialSecretKey = "credential.json"

// SaveCredentialToSecret persists the agent's enrolment bundle into its
// in-cluster Secret so it survives a pod restart. A pod's filesystem does not.
func SaveCredentialToSecret(edgeName string, credential tunnel.Credential) error {
	encoded, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encoding the agent credential: %w", err)
	}
	return saveAgentSecretKey(edgeName, credentialSecretKey, encoded)
}

// LoadCredentialFromSecret reads a previously saved bundle from the agent's
// in-cluster Secret. Absent is not an error: it means this pod has not
// enrolled yet.
func LoadCredentialFromSecret(edgeName string) (tunnel.Credential, bool, error) {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return tunnel.Credential{}, false, err
	}
	ns, err := inClusterNamespace()
	if err != nil {
		return tunnel.Credential{}, false, err
	}
	secret, err := cs.CoreV1().Secrets(ns).Get(context.Background(), AgentKubeconfigSecretName(edgeName), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return tunnel.Credential{}, false, nil
	}
	if err != nil {
		return tunnel.Credential{}, false, fmt.Errorf("getting the agent secret: %w", err)
	}
	raw, ok := secret.Data[credentialSecretKey]
	if !ok || len(raw) == 0 {
		return tunnel.Credential{}, false, nil
	}
	var credential tunnel.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return tunnel.Credential{}, false, fmt.Errorf("parsing the stored agent credential: %w", err)
	}
	return credential, credential.Token != "", nil
}

// saveAgentSecretKey upserts one key of the agent's own Secret.
func saveAgentSecretKey(edgeName, key string, value []byte) error {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return err
	}
	ns, err := inClusterNamespace()
	if err != nil {
		return err
	}
	secretName := AgentKubeconfigSecretName(edgeName)
	existing, err := cs.CoreV1().Secrets(ns).Get(context.Background(), secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().Secrets(ns).Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{key: value},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating the agent secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting the agent secret: %w", err)
	}
	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	existing.Data[key] = value
	if _, err := cs.CoreV1().Secrets(ns).Update(context.Background(), existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating the agent secret: %w", err)
	}
	return nil
}

// SaveKubeconfigToSecret writes the hub kubeconfig to the in-cluster Secret so
// that it survives a pod restart.
func SaveKubeconfigToSecret(edgeName, kubeconfigData string) error {
	cs, err := newInClusterKubernetesClient()
	if err != nil {
		return err
	}
	ns, err := inClusterNamespace()
	if err != nil {
		return err
	}
	secretName := AgentKubeconfigSecretName(edgeName)
	existing, err := cs.CoreV1().Secrets(ns).Get(
		context.Background(),
		secretName,
		metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().Secrets(ns).Create(
			context.Background(),
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: ns,
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
	_, err = cs.CoreV1().Secrets(ns).Update(
		context.Background(),
		existing,
		metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating kubeconfig secret: %w", err)
	}
	return nil
}
