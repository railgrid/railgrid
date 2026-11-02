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

package api

import (
	"context"
	"sort"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Metadata listing never reads keys or exercises provider-account use. A
// restricted connection read must not hide an otherwise visible model.
func listModelCredentialSummaries(ctx context.Context, c *agentsclient.Client) (listCredentialsOutput, error) {
	list, err := c.ModelCredentials().List(ctx, metav1.ListOptions{})
	if err != nil {
		return listCredentialsOutput{}, err
	}
	out := listCredentialsOutput{Credentials: []credentialSummary{}}
	for i := range list.Items {
		cred := &list.Items[i]
		if cred.Spec.Model == "" && !agentsv1alpha1.IsHarnessProvider(cred.Spec.Provider) {
			continue
		}
		models := cred.Status.Models
		ready := meta.IsStatusConditionTrue(cred.Status.Conditions, agentsv1alpha1.ConditionReady)
		if cred.Spec.ConnectionRef != "" {
			connection, err := c.ModelProviderConnections().Get(ctx, cred.Spec.ConnectionRef, metav1.GetOptions{})
			switch {
			case apierrors.IsForbidden(err): // Keep the binding's observed status.
			case apierrors.IsNotFound(err):
				ready = false
			case err != nil:
				return listCredentialsOutput{}, err
			default:
				resolved, err := agentsv1alpha1.ResolveModelConnection(cred, connection)
				if err != nil {
					ready = false
				} else {
					cred = resolved
					models = connection.Status.Models
				}
			}
		}
		out.Credentials = append(out.Credentials, credentialSummary{Name: cred.Name, Provider: cred.Spec.Provider, BaseURL: cred.Spec.BaseURL, AnthropicWorkspaceID: cred.Spec.AnthropicWorkspaceID, Model: cred.Spec.Model, Ready: ready, Models: models})
	}
	sort.Slice(out.Credentials, func(i, j int) bool { return out.Credentials[i].Name < out.Credentials[j].Name })
	return out, nil
}
