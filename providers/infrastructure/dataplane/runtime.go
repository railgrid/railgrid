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

package dataplane

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// restRuntime is the production Runtime: it wraps the runtime cluster's
// rest.Config (resolved from KRO_KUBECONFIG / in-cluster) and a clientset for
// reading the per-instance control-token Secret. It is the single holder of a
// runtime-cluster credential in the request path.
type restRuntime struct {
	config *rest.Config
	client kubernetes.Interface
}

// NewRuntime builds a Runtime from the runtime cluster config. Returns nil when
// config is nil, which the handler reports as "data plane unavailable".
func NewRuntime(config *rest.Config) (Runtime, error) {
	if config == nil {
		return nil, nil
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("runtime clientset: %w", err)
	}
	return &restRuntime{config: config, client: client}, nil
}

func (r *restRuntime) Host() string {
	return strings.TrimRight(r.config.Host, "/")
}

func (r *restRuntime) Transport() (http.RoundTripper, error) {
	return rest.TransportFor(r.config)
}

func (r *restRuntime) ControlToken(ctx context.Context, namespace, name string) (string, error) {
	secret, err := r.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read control secret %s/%s: %w", namespace, name, err)
	}
	token := strings.TrimSpace(string(secret.Data["token"]))
	if token == "" {
		return "", fmt.Errorf("control secret %s/%s has no token", namespace, name)
	}
	return token, nil
}
