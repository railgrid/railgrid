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

package bootstrap

import (
	"context"
	"fmt"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// InstallCRDs installs the kedge CRDs into the cluster.
func InstallCRDs(ctx context.Context, config *rest.Config) error {
	logger := klog.FromContext(ctx)
	logger.Info("Installing kedge CRDs")

	client, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating apiextensions client: %w", err)
	}

	entries, err := crdFS.ReadDir("crds")
	if err != nil {
		return fmt.Errorf("reading embedded CRD directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		data, err := crdFS.ReadFile("crds/" + entry.Name())
		if err != nil {
			return fmt.Errorf("reading embedded CRD file %s: %w", entry.Name(), err)
		}

		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(data, &crd); err != nil {
			return fmt.Errorf("unmarshaling CRD %s: %w", entry.Name(), err)
		}

		existing, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			logger.Info("Creating CRD", "name", crd.Name)
			if _, err := client.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, &crd, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("creating CRD %s: %w", crd.Name, err)
			}
		} else if err != nil {
			return fmt.Errorf("getting CRD %s: %w", crd.Name, err)
		} else {
			logger.Info("Updating CRD", "name", crd.Name)
			crd.ResourceVersion = existing.ResourceVersion
			if _, err := client.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, &crd, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("updating CRD %s: %w", crd.Name, err)
			}
		}
	}

	// Wait for all CRDs to be established
	crdNames := []string{
		"virtualworkloads.kedge.faros.sh",
		"sites.kedge.faros.sh",
		"placements.kedge.faros.sh",
		"users.kedge.faros.sh",
	}

	for _, name := range crdNames {
		logger.Info("Waiting for CRD to be established", "name", name)
		if err := waitForCRDEstablished(ctx, client, name); err != nil {
			return fmt.Errorf("waiting for CRD %s: %w", name, err)
		}
	}

	logger.Info("All kedge CRDs installed and established")
	return nil
}

func waitForCRDEstablished(ctx context.Context, client apiextensionsclient.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, cond := range crd.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}
