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

package project

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/internal/pullcredential"
)

// pullCredentialOutput is the binding status output key for a pull
// credential problem. The portal shows it with the production environment.
const pullCredentialOutput = "imagePullCredential"

var (
	secretGVK     = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
	connectionGVK = schema.GroupVersionKind{Group: pullcredential.ConnectionsGVR.Group, Version: pullcredential.ConnectionsGVR.Version, Kind: "Connection"}
)

// pullCredentialResult is what one pass of ensurePullCredential decided.
type pullCredentialResult struct {
	// problem is shown on the binding's status; empty when all is well.
	problem string
	// retry asks for a backoff requeue: the call failed and may succeed.
	retry bool
	// refreshIn is when the credential next needs re-minting; zero when it
	// does not expire.
	refreshIn time.Duration
}

// ensurePullCredential gets the binding's pull credential onto its Instance.
//
// The binding's imagePullSecretRef names App Studio's own Secret (written at
// promote). Infrastructure cannot read that Secret, so this hands its
// contents to infrastructure's image-pull-credential verb, which stores them
// under infrastructure's label and returns the Secret name the Instance's
// spec.imagePullSecretRef must carry (internal/pullcredential). It also
// re-mints the credential before it expires: a GitHub App token lives about
// an hour, and a rescheduled pod with a lapsed one cannot pull.
//
// Every durable fact lives on an object: the credential and its expiry on
// the source Secret, the hand-over's result on the Instance (the reference
// and a digest of what was handed over). A pass that finds them in step does
// nothing.
func (r *Reconciler) ensurePullCredential(ctx context.Context, reader client.Reader, c client.Client, clusterName string, p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, inst *unstructured.Unstructured) pullCredentialResult {
	ref := binding.ImagePullSecretRef
	if ref == nil || strings.TrimSpace(ref.Name) == "" || inst == nil {
		return pullCredentialResult{}
	}
	if r.Callers == nil {
		return pullCredentialResult{problem: "no provider credential is configured, so the pull credential cannot be handed to the infrastructure provider"}
	}

	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(secretGVK)
	key := types.NamespacedName{Namespace: pullcredential.Namespace, Name: strings.TrimSpace(ref.Name)}
	if err := reader.Get(ctx, key, source); err != nil {
		if apierrors.IsNotFound(err) {
			// Nothing to hand over. A binding written before the hand-over
			// existed names infrastructure's Secret directly, which App
			// Studio cannot see. That Instance keeps the reference it has.
			return pullCredentialResult{problem: fmt.Sprintf("App Studio holds no pull credential in Secret %s; promote again to renew it", key.Name)}
		}
		return pullCredentialResult{problem: "reading the pull credential: " + err.Error(), retry: true}
	}

	expiresAt, expires := pullcredential.ExpiresAt(source)
	if expires && time.Until(expiresAt) < pullcredential.RefreshBefore {
		renewed, err := r.renewPullCredential(ctx, reader, c, clusterName, source)
		if err != nil {
			return pullCredentialResult{problem: "renewing the pull credential: " + err.Error(), retry: true}
		}
		source = renewed
		expiresAt, expires = pullcredential.ExpiresAt(source)
	}

	dockerConfig, err := pullcredential.SourceDockerConfig(source)
	if err != nil {
		return pullCredentialResult{problem: err.Error()}
	}
	result := pullCredentialResult{}
	if expires {
		result.refreshIn = max(time.Until(expiresAt)-pullcredential.RefreshBefore, time.Minute)
	}

	digest := pullcredential.Digest(dockerConfig)
	currentRef, _, _ := unstructured.NestedString(inst.Object, "spec", "imagePullSecretRef", "name")
	if currentRef != "" && inst.GetAnnotations()[pullcredential.DigestAnnotation] == digest && !pullSecretMissing(inst) {
		return result
	}

	name, err := pullcredential.HandOver(ctx, r.Callers, clusterName, inst.GetName(), dockerConfig, source.GetAnnotations()[pullcredential.ExpiresAtAnnotation])
	if err != nil {
		return pullCredentialResult{problem: "handing the pull credential to the infrastructure provider: " + err.Error(), retry: true, refreshIn: result.refreshIn}
	}
	if err := setInstancePullSecretRef(ctx, c, inst, name, digest); err != nil {
		return pullCredentialResult{problem: "naming the pull credential on the instance: " + err.Error(), retry: true, refreshIn: result.refreshIn}
	}
	return result
}

// renewPullCredential mints a fresh credential from the Connection the
// current one was minted from, and rewrites the source Secret with it.
//
// It never reads the Project's connectionRef. Promote authorized that
// Connection by reading it as the caller; this runs as the provider with no
// caller to check, so following a connectionRef edited since would let anyone
// who can update the Project mint from a Connection they cannot read. A
// Connection deleted and recreated under the same name is a different one, so
// a UID mismatch stops renewal too. Either way, promoting again re-authorizes.
func (r *Reconciler) renewPullCredential(ctx context.Context, reader client.Reader, c client.Client, clusterName string, source *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	connectionRef, connectionUID := pullcredential.SourceConnection(source)
	if connectionRef == "" || connectionUID == "" {
		return nil, fmt.Errorf("the pull credential records no connection to renew from; promote again")
	}
	conn := &unstructured.Unstructured{}
	conn.SetGroupVersionKind(connectionGVK)
	if err := reader.Get(ctx, types.NamespacedName{Name: connectionRef}, conn); err != nil {
		return nil, fmt.Errorf("reading connection %q: %w", connectionRef, err)
	}
	if string(conn.GetUID()) != connectionUID {
		return nil, fmt.Errorf("connection %q was replaced after the project was promoted; promote again", connectionRef)
	}
	token, err := pullcredential.Mint(ctx, r.Callers, clusterName, connectionRef, connectionUID)
	if err != nil {
		return nil, err
	}
	desired, err := pullcredential.SourceSecret(pullcredential.Source{
		Name:          source.GetName(),
		Connection:    connectionRef,
		ConnectionUID: connectionUID,
	}, token)
	if err != nil {
		return nil, err
	}
	// Keep the Project ownership promote set, so renewal never orphans it.
	desired.SetOwnerReferences(source.GetOwnerReferences())
	desired.SetResourceVersion(source.GetResourceVersion())
	if err := c.Update(ctx, desired); err != nil {
		return nil, err
	}
	return desired, nil
}

// pullSecretMissing reports whether infrastructure says the pull Secret the
// Instance names is gone, in which case it is handed over again.
//
// Infrastructure uses the same reason for a missing BYO OIDC Secret
// (spec.oidcBridgeSecretRef), which no hand-over fixes, so the condition's
// message must name the pull reference too (bridge.go writes both).
func pullSecretMissing(inst *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(inst.Object, "status", "conditions")
	for _, raw := range conditions {
		cond, ok := raw.(map[string]any)
		if !ok || cond["type"] != "SecretsBridged" {
			continue
		}
		message, _ := cond["message"].(string)
		return cond["status"] == "False" && cond["reason"] == "SecretRefNotFound" &&
			strings.HasPrefix(message, pullSecretRefMessagePrefix)
	}
	return false
}

// pullSecretRefMessagePrefix starts infrastructure's SecretRefNotFound
// message when the missing Secret is the one spec.imagePullSecretRef names.
const pullSecretRefMessagePrefix = "spec.imagePullSecretRef "

// setInstancePullSecretRef names the infrastructure-owned Secret on the
// Instance and records which credential it holds.
func setInstancePullSecretRef(ctx context.Context, c client.Client, inst *unstructured.Unstructured, name, digest string) error {
	var lastErr error
	for attempt := 0; attempt < instanceConvergenceMaxAttempts; attempt++ {
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(inst.GroupVersionKind())
		if err := c.Get(ctx, types.NamespacedName{Name: inst.GetName()}, current); err != nil {
			return err
		}
		if err := unstructured.SetNestedField(current.Object, name, "spec", "imagePullSecretRef", "name"); err != nil {
			return err
		}
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[pullcredential.DigestAnnotation] = digest
		current.SetAnnotations(annotations)
		lastErr = c.Update(ctx, current)
		if lastErr == nil {
			inst.Object = current.Object
			return nil
		}
		if !apierrors.IsConflict(lastErr) {
			return lastErr
		}
	}
	return lastErr
}
