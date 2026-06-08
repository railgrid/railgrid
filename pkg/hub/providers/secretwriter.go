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

package providers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RuntimeKubeconfigSecretKey is the data key the minted kubeconfig is stored
// under. It MUST match the provider chart's volume item key (the infrastructure
// chart's deployment.yaml mounts items[key=kubeconfig]).
const RuntimeKubeconfigSecretKey = "kubeconfig"

// hostSecretWriter implements SecretWriter against a host-cluster kube client.
// server.go constructs one when the hub is given a host kubeconfig
// (--kubeconfig), so the catalog controller can deliver the minted
// kedge-provider-kubeconfig into spec.serviceAccountNamespace — the namespace
// where the provider Deployment (its init + serve containers) mounts it.
type hostSecretWriter struct {
	cs kubernetes.Interface
}

// NewHostSecretWriter returns a SecretWriter backed by cs.
func NewHostSecretWriter(cs kubernetes.Interface) SecretWriter {
	return &hostSecretWriter{cs: cs}
}

// WriteKubeconfigSecret creates or updates the named Secret with the kubeconfig
// under the well-known key. Idempotent: it overwrites the data on update so a
// re-mint (e.g. after the provider workspace is recreated) propagates.
func (w *hostSecretWriter) WriteKubeconfigSecret(ctx context.Context, namespace, name string, kubeconfig []byte) error {
	desired := map[string][]byte{RuntimeKubeconfigSecretKey: kubeconfig}
	existing, err := w.cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Data = desired
		if _, err := w.cs.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update Secret %s/%s: %w", namespace, name, err)
		}
		return nil
	case apierrors.IsNotFound(err):
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       desired,
		}
		if _, err := w.cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create Secret %s/%s: %w", namespace, name, err)
		}
		return nil
	default:
		return fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}
}
