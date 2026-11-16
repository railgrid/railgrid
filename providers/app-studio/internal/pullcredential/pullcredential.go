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

// Package pullcredential carries a production instance's registry pull
// credential from the Code provider, which mints it, to the infrastructure
// provider, which mounts it.
//
// Three providers and two label-scoped claims are involved, and the shape
// follows from what each claim allows:
//
//   - App Studio's `secrets` claim reaches only Secrets labelled
//     railgrid.ai/owner: app-studio. kcp refuses its write of a Secret
//     labelled for anyone else, so App Studio cannot write the Secret
//     infrastructure reads. (It tried: every promotion since the provider
//     identity took over that write logged the refusal and left production
//     pulling anonymously.)
//   - Infrastructure's claim reaches only Secrets labelled for it, so it
//     cannot read App Studio's.
//
// So the credential moves through both providers' published APIs, as each
// provider itself:
//
//  1. Mint calls Code's connections/{name}/mint-registry-token.
//  2. SourceSecret is App Studio's own record of it, under its own label:
//     the durable input the Project reconciler hands over and re-mints.
//  3. HandOver calls infrastructure's instances/{name}/image-pull-credential,
//     which writes infrastructure's own Secret and returns the name the
//     Instance's spec.imagePullSecretRef must carry.
package pullcredential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-app-studio/internal/crossprovider"
	"github.com/railgrid/provider-sdk/claimscope"
	"github.com/railgrid/provider-sdk/dataplane"
)

// Caller is what this package needs of the provider's caller factory: a
// claimed verb's URL on this provider's export virtual workspace, and an HTTP
// client that authenticates as the provider.
type Caller interface {
	ExportVerbURL(ctx context.Context, gvr schema.GroupVersionResource, r dataplane.Request) (string, error)
	ProviderHTTPClient() (*http.Client, error)
}

var (
	// ConnectionsGVR is the Code kind the mint verb hangs off.
	ConnectionsGVR = schema.GroupVersionResource{Group: crossprovider.CodeAPIGroup, Version: "v1alpha1", Resource: "connections"}
	// InstancesGVR is the infrastructure kind the hand-over verb hangs off.
	InstancesGVR = schema.GroupVersionResource{Group: crossprovider.InfrastructureAPIGroup, Version: "v1alpha1", Resource: crossprovider.InstancesResource}
)

const (
	// MintVerb is Code's pull-credential verb on connections.
	MintVerb = "mint-registry-token"
	// HandOverVerb is infrastructure's pull-credential verb on instances.
	HandOverVerb = "image-pull-credential"

	// Namespace is where the source Secret lives in the tenant workspace.
	Namespace = "default"
	// DockerConfigKey is the one key a dockerconfigjson Secret carries.
	DockerConfigKey = ".dockerconfigjson"
	// ExpiresAtAnnotation records on the source Secret when the minted
	// credential stops working.
	ExpiresAtAnnotation = "ai.railgrid.ai/registry-token-expires-at"
	// ConnectionAnnotation and ConnectionUIDAnnotation record on the source
	// Secret which Connection the credential was minted from (Source).
	ConnectionAnnotation    = "ai.railgrid.ai/pull-credential-connection"
	ConnectionUIDAnnotation = "ai.railgrid.ai/pull-credential-connection-uid"
	// DigestAnnotation records on the Instance which credential was last
	// handed over, so an unchanged credential is not handed over again on
	// every reconcile. It is a hash, never the credential.
	DigestAnnotation = "ai.railgrid.ai/image-pull-credential-digest"

	// RefreshBefore is how long before expiry a credential is re-minted. A
	// GitHub App installation token lives about an hour, and a pod that is
	// rescheduled after it lapses cannot pull.
	RefreshBefore = 15 * time.Minute

	callTimeout      = 30 * time.Second
	maxResponseBytes = 64 << 10
	owner            = "app-studio"
)

// SourceSecretName is App Studio's own Secret holding an instance's pull
// credential.
func SourceSecretName(instance string) string { return instance + "-pull-credential" }

// Token is the mint verb's result.
type Token struct {
	Registry  string `json:"registry"`
	Username  string `json:"username"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Scoped    bool   `json:"scoped"`
}

// Mint asks the Code provider for a pull credential for the Connection in
// cluster, as this provider.
func Mint(ctx context.Context, c Caller, cluster, connection, connectionUID string) (Token, error) {
	if strings.TrimSpace(connectionUID) == "" {
		return Token{}, fmt.Errorf("connection %q has no UID to pin the action to", connection)
	}
	var out Token
	if err := invoke(ctx, c, ConnectionsGVR, cluster, connection, MintVerb, map[string]any{"connectionUID": connectionUID}, &out); err != nil {
		return Token{}, err
	}
	if strings.TrimSpace(out.Token) == "" || strings.TrimSpace(out.Registry) == "" {
		return Token{}, fmt.Errorf("%s: the Code provider returned no credential", MintVerb)
	}
	return out, nil
}

// DockerConfigJSON renders t as a .dockerconfigjson document.
func DockerConfigJSON(t Token) (string, error) {
	doc, err := json.Marshal(map[string]any{
		"auths": map[string]any{
			t.Registry: map[string]any{
				"username": t.Username,
				"password": t.Token,
				"auth":     base64.StdEncoding.EncodeToString([]byte(t.Username + ":" + t.Token)),
			},
		},
	})
	return string(doc), err
}

// Source is what App Studio's own Secret records besides the credential.
type Source struct {
	// Name is the Secret's name (SourceSecretName).
	Name string
	// Connection and ConnectionUID name the Code Connection the credential
	// was minted from at promote, where the caller's right to use it was
	// checked by reading it as them. Renewal mints from this Connection only:
	// the Project's connectionRef can change after promote, and the
	// reconciler mints as the provider, with no caller to check.
	Connection    string
	ConnectionUID string
	// Owner is the Project, so kcp garbage-collects the credential with it.
	Owner *metav1.OwnerReference
}

// SourceSecret builds App Studio's own Secret for t, under its own owner
// label so its claim can write it and read it back.
func SourceSecret(s Source, t Token) (*unstructured.Unstructured, error) {
	if strings.TrimSpace(s.Connection) == "" || strings.TrimSpace(s.ConnectionUID) == "" {
		return nil, fmt.Errorf("pull credential %s records no connection to renew from", s.Name)
	}
	doc, err := DockerConfigJSON(t)
	if err != nil {
		return nil, err
	}
	annotations := map[string]string{
		ConnectionAnnotation:    s.Connection,
		ConnectionUIDAnnotation: s.ConnectionUID,
	}
	if t.ExpiresAt != "" {
		annotations[ExpiresAtAnnotation] = t.ExpiresAt
	}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      s.Name,
			"namespace": Namespace,
			"labels":    map[string]any{claimscope.OwnerLabel: owner},
		},
		"type":       "kubernetes.io/dockerconfigjson",
		"stringData": map[string]any{DockerConfigKey: doc},
	}}
	secret.SetAnnotations(annotations)
	if s.Owner != nil {
		secret.SetOwnerReferences([]metav1.OwnerReference{*s.Owner})
	}
	return secret, nil
}

// SourceConnection reads the Connection a source Secret's credential was
// minted from. Both are empty on a Secret that records none.
func SourceConnection(secret *unstructured.Unstructured) (name, uid string) {
	annotations := secret.GetAnnotations()
	return strings.TrimSpace(annotations[ConnectionAnnotation]), strings.TrimSpace(annotations[ConnectionUIDAnnotation])
}

// SourceDockerConfig reads the dockerconfigjson out of a source Secret as the
// API server returns it (base64 under .data).
func SourceDockerConfig(secret *unstructured.Unstructured) (string, error) {
	encoded, _, _ := unstructured.NestedString(secret.Object, "data", DockerConfigKey)
	if encoded == "" {
		if plain, _, _ := unstructured.NestedString(secret.Object, "stringData", DockerConfigKey); plain != "" {
			return plain, nil
		}
		return "", fmt.Errorf("secret %s has no %s", secret.GetName(), DockerConfigKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secret %s: %s is not base64", secret.GetName(), DockerConfigKey)
	}
	return string(decoded), nil
}

// ExpiresAt reads the expiry recorded on a source Secret. ok is false when
// none is recorded, meaning the credential does not rotate.
func ExpiresAt(secret *unstructured.Unstructured) (time.Time, bool) {
	raw := strings.TrimSpace(secret.GetAnnotations()[ExpiresAtAnnotation])
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// An unreadable expiry is treated as already due, so it is replaced.
		return time.Time{}, true
	}
	return at, true
}

// Digest identifies a credential without revealing it.
func Digest(dockerConfig string) string {
	sum := sha256.Sum256([]byte(dockerConfig))
	return hex.EncodeToString(sum[:16])
}

// HandOver gives the infrastructure provider the credential for instance and
// returns the name of the Secret it stored it in.
func HandOver(ctx context.Context, c Caller, cluster, instance, dockerConfig, expiresAt string) (string, error) {
	input := map[string]any{"dockerConfigJSON": dockerConfig}
	if expiresAt != "" {
		input["expiresAt"] = expiresAt
	}
	var out struct {
		SecretRef struct {
			Name string `json:"name"`
		} `json:"secretRef"`
	}
	if err := invoke(ctx, c, InstancesGVR, cluster, instance, HandOverVerb, input, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.SecretRef.Name) == "" {
		return "", fmt.Errorf("%s: the infrastructure provider returned no Secret name", HandOverVerb)
	}
	return strings.TrimSpace(out.SecretRef.Name), nil
}

// invoke POSTs one action envelope at name/verb on gvr and decodes its
// result. Neither the request nor the response body is ever put in an error.
func invoke(ctx context.Context, c Caller, gvr schema.GroupVersionResource, cluster, name, verb string, input, out any) error {
	if c == nil {
		return fmt.Errorf("%s: no provider credential is configured", verb)
	}
	endpoint, err := c.ExportVerbURL(ctx, gvr, dataplane.Request{
		ClusterID: cluster,
		Resource:  gvr.Resource,
		Name:      name,
		Verb:      verb,
	})
	if err != nil {
		return fmt.Errorf("addressing %s/%s: %w", gvr.Resource, verb, err)
	}
	client, err := c.ProviderHTTPClient()
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("%s: response exceeds %d bytes", verb, maxResponseBytes)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("%s: HTTP %d with a malformed response", verb, resp.StatusCode)
	}
	if envelope.Error != nil && envelope.Error.Code != "" {
		if msg := strings.TrimSpace(envelope.Error.Message); msg != "" {
			return fmt.Errorf("%s: %s: %s", verb, envelope.Error.Code, msg)
		}
		return fmt.Errorf("%s: %s", verb, envelope.Error.Code)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: HTTP %d", verb, resp.StatusCode)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("%s: malformed result", verb)
	}
	return nil
}
