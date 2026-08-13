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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// InstallCRDs installs the faros CRDs into the cluster.
func InstallCRDs(ctx context.Context, config *rest.Config) error {
	logger := klog.FromContext(ctx)
	logger.Info("Installing faros CRDs")

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

		// Every hub replica installs the CRDs at startup, so create and update
		// both race their peers. AlreadyExists and Conflict simply mean another
		// replica got there first with byte-identical content; retrying on the
		// freshly observed resource version converges instead of crash-looping
		// the replica that lost the race.
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			desired := *crd.DeepCopy()
			existing, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, desired.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				logger.Info("Creating CRD", "name", desired.Name)
				_, err := client.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, &desired, metav1.CreateOptions{})
				if apierrors.IsAlreadyExists(err) {
					// Lost the create race; fall through to the update path.
					return apierrors.NewConflict(
						schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
						desired.Name, err)
				}
				return err
			} else if err != nil {
				return err
			}
			logger.Info("Updating CRD", "name", desired.Name)
			desired.ResourceVersion = existing.ResourceVersion
			_, err = client.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, &desired, metav1.UpdateOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("installing CRD %s: %w", crd.Name, err)
		}
	}

	// Wait for all CRDs to be established. KubernetesMCP + LinuxMCP
	// CRDs were removed when both per-kind endpoints collapsed into
	// the MCPServer aggregate. The legacy `users.faros.sh` CRD
	// was retired in the User CRD migration; the User type now lives
	// under tenants.faros.sh alongside Organization, Membership,
	// and UserMembershipIndex.
	crdNames := []string{
		// Edge / VirtualWorkload / Placement / MCPServer CRDs moved out of the
		// hub core into the edges-connectivity + edges-* providers, which install
		// their own schemas at provider init. The hub no longer bootstraps them.
		"users.tenants.faros.sh",
		"organizations.tenants.faros.sh",
		"memberships.tenants.faros.sh",
		"usermembershipindices.tenants.faros.sh",
		"userpreferences.tenants.faros.sh",
		"catalogentries.providers.faros.sh",
	}

	for _, name := range crdNames {
		logger.Info("Waiting for CRD to be established", "name", name)
		if err := waitForCRDEstablished(ctx, client, name); err != nil {
			return fmt.Errorf("waiting for CRD %s: %w", name, err)
		}
	}

	logger.Info("All faros CRDs installed and established")
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
