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

package providerinstall

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func missingConfigurationSecrets(ctx context.Context, edge kubernetes.Interface, p *Plan) ([]string, error) {
	var missing []string
	for _, name := range p.RequiredSecrets {
		secret, err := edge.CoreV1().Secrets(p.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("checking configuration Secret %s: %w", name, err)
		}
		for _, key := range p.RequiredSecretKeys[name] {
			if len(secret.Data[key]) == 0 {
				missing = append(missing, fmt.Sprintf("%s (missing or empty key %s)", name, key))
			}
		}
	}
	return missing, nil
}

func configurationStatus(ctx context.Context, edge kubernetes.Interface, p *Plan) (*Status, error) {
	ns, err := edge.CoreV1().Namespaces().Get(ctx, p.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &Status{Phase: "AwaitingConfiguration", Message: "The installation namespace was removed. Continue installation to prepare it again before adding configuration.", Namespace: p.Namespace, Version: p.Version, Attempt: p.Attempt, CanRetry: true}, nil
	}
	if err != nil {
		return nil, err
	}
	if ns.Labels[ownerLabel] != p.Owner || (p.NamespaceUID != "" && string(ns.UID) != p.NamespaceUID) {
		return nil, fmt.Errorf("installation namespace ownership changed while waiting for configuration")
	}
	missing, err := missingConfigurationSecrets(ctx, edge, p)
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf("Namespace %s is ready. Create the referenced Secrets there, then continue installation. Helm has not started. Missing configuration: %s.", p.Namespace, strings.Join(missing, ", "))
	if len(missing) == 0 {
		message = "The referenced Secrets are available. Continue installation to start Helm."
	}
	return &Status{Phase: "AwaitingConfiguration", Message: message, Namespace: p.Namespace, Version: p.Version, Attempt: p.Attempt, CanRetry: true}, nil
}
