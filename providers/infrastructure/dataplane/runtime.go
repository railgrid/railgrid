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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// restRuntime is the production Runtime: it wraps the runtime cluster's
// rest.Config (resolved from KRO_KUBECONFIG / in-cluster) and a clientset for
// reading the per-instance control-token Secret. It is the single holder of a
// runtime-cluster credential in the request path.
type restRuntime struct {
	config        *rest.Config
	client        kubernetes.Interface
	dynamicClient dynamic.Interface
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
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("runtime dynamic client: %w", err)
	}
	return &restRuntime{config: config, client: client, dynamicClient: dynamicClient}, nil
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

// RecordActivity updates the runtime object's provider-owned marker. The
// runtimeRef is copied into the tenant Instance status by the instance
// controller, so this path remains addressable after a Template is retired.
// The patch uses the runtime credential held by restRuntime; caller bearer
// tokens never reach this cluster client.
func (r *restRuntime) RecordActivity(ctx context.Context, instance *unstructured.Unstructured) error {
	if r == nil || r.dynamicClient == nil {
		return fmt.Errorf("runtime dynamic client is unavailable")
	}
	if instance == nil {
		return fmt.Errorf("instance is nil")
	}
	ref, found, err := unstructured.NestedMap(instance.Object, "status", "runtimeRef")
	if err != nil {
		return fmt.Errorf("read status.runtimeRef: %w", err)
	}
	if !found {
		return fmt.Errorf("status.runtimeRef is absent")
	}
	apiVersion, _ := ref["apiVersion"].(string)
	resource, _ := ref["resource"].(string)
	namespace, _ := ref["namespace"].(string)
	name, _ := ref["name"].(string)
	gv, err := schema.ParseGroupVersion(strings.TrimSpace(apiVersion))
	if err != nil || gv.Group == "" || gv.Version == "" {
		if err == nil {
			err = fmt.Errorf("group or version is empty")
		}
		return fmt.Errorf("invalid status.runtimeRef apiVersion %q: %w", apiVersion, err)
	}
	resource = strings.TrimSpace(resource)
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if resource == "" || namespace == "" || name == "" {
		return fmt.Errorf("status.runtimeRef must include resource, namespace, and name")
	}
	if expected, _, _ := unstructured.NestedString(instance.Object, "status", "runtimeNamespace"); expected != "" && namespace != expected {
		return fmt.Errorf("status.runtimeRef namespace %q does not match runtimeNamespace %q", namespace, expected)
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				infrav1alpha1.FarosLastActivityAnnotation: time.Now().UTC().Format(time.RFC3339Nano),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("encode activity patch: %w", err)
	}
	patchActivity := func() error {
		_, err := r.dynamicClient.Resource(gv.WithResource(resource)).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}
	if err := retry.OnError(retry.DefaultBackoff, apierrors.IsConflict, patchActivity); err != nil {
		return fmt.Errorf("patch runtime activity marker %s/%s: %w", namespace, name, err)
	}
	return nil
}
