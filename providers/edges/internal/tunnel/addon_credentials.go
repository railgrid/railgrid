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

package tunnel

import (
	"encoding/json"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	edgesv1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
)

type addonCredentialsRequest struct {
	Addon         string                  `json:"addon"`
	UID           types.UID               `json:"uid,omitempty"`
	AuthSecretRef *corev1.SecretReference `json:"authSecretRef,omitempty"`
	Token         string                  `json:"token,omitempty"`
}

// The ordinary gate runs before this handler: kcp authorized the POST on
// {resource}/addon-credentials and the caller's visibility of its own edge was
// reviewed on its behalf (dataplane.Gate). The provider
// then proves the Addon belongs to that edge and resolves all Secret coordinates
// from persisted state. Agents never receive general Secret permissions.
func (p *Server) serveAddonCredentials(w http.ResponseWriter, r *http.Request, req dataplane.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if req.Tail != "" {
		http.Error(w, "unexpected path suffix", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body addonCredentialsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if body.Addon == "" || (body.AuthSecretRef == nil) == (body.Token == "") || len(body.Token) > 4096 {
		http.Error(w, "supply an addon and either authSecretRef or token", http.StatusBadRequest)
		return
	}
	cfg, err := p.tenantConfigFor(r.Context(), req.ClusterID)
	if err != nil {
		http.Error(w, "tenant unavailable", http.StatusServiceUnavailable)
		return
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		http.Error(w, "tenant unavailable", http.StatusServiceUnavailable)
		return
	}
	raw, err := dyn.Resource(schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "addons"}).Get(r.Context(), body.Addon, metav1.GetOptions{})
	if err != nil {
		http.Error(w, "addon unavailable", http.StatusNotFound)
		return
	}
	var addon edgesv1.Addon
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &addon); err != nil {
		http.Error(w, "invalid addon", http.StatusInternalServerError)
		return
	}
	_, kind, _ := p.gvrForResource(req.Resource)
	if addon.DeletionTimestamp != nil || addon.Spec.EdgeRef.Kind != kind || addon.Spec.EdgeRef.Name != req.Name || addon.Spec.Type != edgesv1.AddonTypeRunner || addon.Spec.Runner == nil {
		http.Error(w, "addon does not belong to this edge", http.StatusForbidden)
		return
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		http.Error(w, "tenant unavailable", http.StatusServiceUnavailable)
		return
	}
	if body.AuthSecretRef != nil {
		var ref *corev1.SecretReference
		keys := []string{"auth.json"}
		if addon.Spec.Runner.Harness == edgesv1.AddonHarnessClaude {
			if addon.Spec.Runner.Claude != nil {
				ref = addon.Spec.Runner.Claude.AuthSecretRef
			}
			keys = []string{"oauthToken", "apiKey"}
		} else if addon.Spec.Runner.Codex != nil {
			ref = addon.Spec.Runner.Codex.AuthSecretRef
		}
		if ref == nil {
			http.Error(w, "addon has no auth reference", http.StatusBadRequest)
			return
		}
		namespace := ref.Namespace
		if namespace == "" {
			namespace = "default"
		}
		requestedNamespace := body.AuthSecretRef.Namespace
		if requestedNamespace == "" {
			requestedNamespace = "default"
		}
		if ref.Name != body.AuthSecretRef.Name || namespace != requestedNamespace {
			http.Error(w, "auth reference changed", http.StatusConflict)
			return
		}
		secret, err := kube.CoreV1().Secrets(namespace).Get(r.Context(), ref.Name, metav1.GetOptions{})
		if err != nil {
			http.Error(w, "runner auth Secret unavailable to edges; label it railgrid.ai/owner=edges", http.StatusNotFound)
			return
		}
		if secret.Labels["railgrid.ai/owner"] != "edges" {
			http.Error(w, "runner auth Secret must be labelled railgrid.ai/owner=edges", http.StatusForbidden)
			return
		}
		result := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: namespace}, Data: map[string][]byte{}}
		for _, key := range keys {
			if value, ok := secret.Data[key]; ok {
				result.Data[key] = value
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			p.logger.Error(err, "writing add-on credential response")
		}
		return
	}
	if body.UID == "" || body.UID != addon.UID {
		http.Error(w, "addon UID changed", http.StatusConflict)
		return
	}
	name := addon.Name + "-runner-token"
	secrets := kube.CoreV1().Secrets("default")
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{"railgrid.ai/owner": "edges", "edges.railgrid.ai/edge": req.Name, "edges.railgrid.ai/addon": addon.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "edges.railgrid.ai/v1alpha1", Kind: "Addon", Name: addon.Name, UID: addon.UID, Controller: ptr.To(true)}}},
		Type:       corev1.SecretTypeOpaque, Data: map[string][]byte{"token": []byte(body.Token)},
	}
	existing, err := secrets.Get(r.Context(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(r.Context(), desired, metav1.CreateOptions{})
	} else if err == nil {
		owner := metav1.GetControllerOf(existing)
		if existing.Labels["railgrid.ai/owner"] != "edges" || owner == nil || owner.UID != addon.UID || owner.Kind != "Addon" || owner.Name != addon.Name {
			http.Error(w, "runner token Secret has another owner", http.StatusConflict)
			return
		}
		existing.Data = desired.Data
		_, err = secrets.Update(r.Context(), existing, metav1.UpdateOptions{})
	}
	if err != nil {
		http.Error(w, "could not publish runner token", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
