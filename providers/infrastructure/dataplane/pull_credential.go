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

package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/claimscope"
	sdk "github.com/railgrid/provider-sdk/dataplane"
)

// The image-pull-credential verb.
//
// The pull Secret an Instance names in spec.imagePullSecretRef has to carry
// railgrid.ai/owner: infrastructure, or this provider's selector-scoped
// `secrets` claim cannot see it. No other provider can write such a Secret:
// kcp's virtual-workspace admission refuses a write whose owner label is not
// the writer's own. So a producer (App Studio, minting from a Code Connection)
// cannot hand the credential over by writing it. It hands it over here
// instead, and this provider writes the Secret under its own label and
// returns the name to put on spec.imagePullSecretRef.

var tenantSecretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

const (
	// pullCredentialMaxInputBytes bounds the request. A dockerconfigjson for
	// one registry is a few hundred bytes.
	pullCredentialMaxInputBytes = 32 << 10
	// pullCredentialExpiresAnnotation records when the credential stops
	// working, so an ImagePullBackOff can be explained from the Secret.
	pullCredentialExpiresAnnotation = "infrastructure.railgrid.ai/image-pull-credential-expires-at"
)

// PullCredentialInput is the verb's input.
type PullCredentialInput struct {
	// DockerConfigJSON is the .dockerconfigjson document: {"auths": {...}}.
	DockerConfigJSON string `json:"dockerConfigJSON"`
	// ExpiresAt is when the credential stops working (RFC 3339), if known.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// PullCredentialResult is the verb's result.
type PullCredentialResult struct {
	// SecretRef names the tenant Secret to set as spec.imagePullSecretRef.
	SecretRef infrav1alpha1.SecretReference `json:"secretRef"`
}

// servePullCredential stores the caller's pull credential as this provider's
// own Secret for the Instance Gate authorized. provider acts as this provider
// in the request's cluster.
func servePullCredential(w http.ResponseWriter, r *http.Request, provider dynamic.Interface, caller sdk.ProxiedIdentity, req sdk.Request, instance *unstructured.Unstructured) {
	env := actionwire.New(r, "infrastructure", infrav1alpha1.ImagePullCredentialVerb, actionwire.ResourceRef{
		APIVersion: instancesGVR.GroupVersion().String(),
		Kind:       "Instance",
		Resource:   instancesGVR.Resource,
		Name:       instance.GetName(),
	})
	sdk.Serve(w, r, env, sdk.Limits{MaxInputBytes: pullCredentialMaxInputBytes}, func(ctx context.Context, raw json.RawMessage) (any, *actionwire.Error) {
		// Storing a credential the workload runs with is a change to the
		// Instance, so a tenant caller needs update on it, not just the
		// visibility Gate checked. A foreign provider reached this verb
		// through its own export, which kcp built only because the tenant
		// accepted that provider's claim on instances/image-pull-credential.
		// The claim is the authorization.
		if !caller.IsForeignProvider(req.ClusterID) {
			allowed, err := sdk.Authorize(ctx, provider, caller, sdk.ResourceAttributes{
				Group: instancesGVR.Group, Version: instancesGVR.Version, Resource: instancesGVR.Resource,
				Name: instance.GetName(), Verb: "update",
			})
			if err != nil {
				klog.FromContext(ctx).Error(err, "image-pull-credential access review failed", "instance", instance.GetName())
				return nil, &actionwire.Error{Code: "access_review_failed", Message: "could not check access to the instance", Retryable: true}
			}
			if !allowed {
				return nil, &actionwire.Error{Code: "forbidden", Message: "updating this instance is not permitted"}
			}
		}

		in, aerr := parsePullCredentialInput(raw)
		if aerr != nil {
			return nil, aerr
		}
		name := infrav1alpha1.ImagePullCredentialSecretName(instance.GetName())
		if err := writePullCredentialSecret(ctx, provider, instance, name, in); err != nil {
			klog.FromContext(ctx).Error(err, "image-pull-credential write failed", "instance", instance.GetName(), "secret", name)
			return nil, &actionwire.Error{Code: "secret_write_failed", Message: "could not store the pull credential", Retryable: true}
		}
		return PullCredentialResult{SecretRef: infrav1alpha1.SecretReference{Name: name}}, nil
	})
}

// parsePullCredentialInput checks the input is a usable dockerconfigjson. It
// never echoes the document back: an error says what is wrong, not what was
// sent.
func parsePullCredentialInput(raw json.RawMessage) (PullCredentialInput, *actionwire.Error) {
	var in PullCredentialInput
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, &actionwire.Error{Code: "invalid_input", Message: "input must be {dockerConfigJSON, expiresAt?}"}
	}
	var doc struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if strings.TrimSpace(in.DockerConfigJSON) == "" || json.Unmarshal([]byte(in.DockerConfigJSON), &doc) != nil || len(doc.Auths) == 0 {
		return in, &actionwire.Error{Code: "invalid_input", Message: "dockerConfigJSON must be a .dockerconfigjson document with at least one entry under auths"}
	}
	if in.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, in.ExpiresAt); err != nil {
			return in, &actionwire.Error{Code: "invalid_input", Message: "expiresAt must be an RFC 3339 timestamp"}
		}
	}
	return in, nil
}

// writePullCredentialSecret creates or replaces the Instance's pull Secret.
// It is owned by the Instance, so deleting the Instance collects it.
func writePullCredentialSecret(ctx context.Context, provider dynamic.Interface, instance *unstructured.Unstructured, name string, in PullCredentialInput) error {
	metadata := map[string]any{
		"name":      name,
		"namespace": infrav1alpha1.CredentialsNamespace,
		"labels":    map[string]any{claimscope.OwnerLabel: "infrastructure"},
		"ownerReferences": []any{map[string]any{
			"apiVersion": instancesGVR.GroupVersion().String(),
			"kind":       "Instance",
			"name":       instance.GetName(),
			"uid":        string(instance.GetUID()),
		}},
	}
	if in.ExpiresAt != "" {
		metadata["annotations"] = map[string]any{pullCredentialExpiresAnnotation: in.ExpiresAt}
	}
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   metadata,
		"type":       "kubernetes.io/dockerconfigjson",
		"stringData": map[string]any{dockerConfigKey: in.DockerConfigJSON},
	}}
	secrets := provider.Resource(tenantSecretsGVR).Namespace(infrav1alpha1.CredentialsNamespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	_, err = secrets.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

// dockerConfigKey is the only key a kubernetes.io/dockerconfigjson Secret
// carries.
const dockerConfigKey = ".dockerconfigjson"
