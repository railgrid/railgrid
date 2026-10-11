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

package commitexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/provider-code/commitbundle"
)

// ErrCommitRequestConflict refuses reuse of a key for another immutable intent.
var ErrCommitRequestConflict = errors.New("commit request ID already names another request")

const commitRequestFingerprintAnnotation = "code.railgrid.ai/commit-request-fingerprint"

func validCommitRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// createKeyedCommit reserves a deterministic Kubernetes object name. Replays
// read that object before touching the bundle, which may already have been
// collected after the commit completed. The API server's create is the single
// cross-replica arbitration point; no process-local lock authorizes an effect.
func createKeyedCommit(ctx context.Context, dyn dynamic.Interface, bundles commitbundle.Store, scope string, req Request) (Result, error) {
	repo, err := getRepository(ctx, dyn, req.RepositoryRef)
	if err != nil {
		return Result{}, err
	}
	if string(repo.UID) != req.RepositoryUID {
		return Result{}, fmt.Errorf("repository UID no longer matches the commit request")
	}
	key := sha256.Sum256([]byte(scope + "\x00" + req.RepositoryUID + "\x00" + req.RequestID))
	name := "commit-" + hex.EncodeToString(key[:20])
	intent, err := json.Marshal(struct {
		RepositoryUID string
		Message       string
		Branch        string
		BundleName    string
		BundleDigest  string
	}{req.RepositoryUID, req.Message, req.Branch, req.Bundle.Name, req.Bundle.Digest})
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(intent)
	fingerprint := hex.EncodeToString(sum[:])
	commits := dyn.Resource(RepositoryCommitsGVR)
	if existing, err := commits.Get(ctx, name, metav1.GetOptions{}); err == nil {
		return keyedCommitResult(existing, scope, fingerprint, req)
	} else if !apierrors.IsNotFound(err) {
		return Result{}, fmt.Errorf("read keyed RepositoryCommit: %w", err)
	}

	// The staged bundle already exists in this logical cluster. Never delete
	// it on a Create error: the server may have accepted the commit before its
	// response was lost, or another replica may have won the same request.
	if _, err := bundles.Get(ctx, scope, req.Bundle.Name, req.Bundle.Digest); err != nil {
		return Result{}, fmt.Errorf("read keyed commit bundle: %w", err)
	}
	obj := commitObject(repo, *req.Bundle, req)
	obj.SetName(name)
	obj.SetAnnotations(map[string]string{commitRequestFingerprintAnnotation: fingerprint})
	created, err := commits.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, getErr := commits.Get(ctx, name, metav1.GetOptions{})
			if getErr != nil {
				return Result{}, fmt.Errorf("read concurrent keyed RepositoryCommit: %w", getErr)
			}
			return keyedCommitResult(existing, scope, fingerprint, req)
		}
		return Result{}, fmt.Errorf("create keyed RepositoryCommit: %w", err)
	}
	return keyedCommitResult(created, scope, fingerprint, req)
}

func keyedCommitResult(obj *unstructured.Unstructured, scope, fingerprint string, req Request) (Result, error) {
	if obj.GetAnnotations()[commitRequestFingerprintAnnotation] != fingerprint {
		return Result{}, ErrCommitRequestConflict
	}
	for _, field := range []struct {
		path []string
		want string
	}{
		{[]string{"spec", "repositoryRef"}, req.RepositoryRef},
		{[]string{"spec", "message"}, req.Message},
		{[]string{"spec", "branch"}, req.Branch},
		{[]string{"spec", "source", "bundleRef", "name"}, req.Bundle.Name},
		{[]string{"spec", "source", "bundleRef", "digest"}, req.Bundle.Digest},
	} {
		value, _, err := unstructured.NestedString(obj.Object, field.path...)
		if err != nil || value != field.want {
			return Result{}, ErrCommitRequestConflict
		}
	}
	if obj.GetDeletionTimestamp() != nil {
		return Result{}, fmt.Errorf("keyed RepositoryCommit is being deleted")
	}
	if bundleStorageScope(obj) != scope {
		return Result{}, fmt.Errorf("keyed RepositoryCommit does not belong to the requested logical cluster")
	}
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "bundleRef", "name")
	digest, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "bundleRef", "digest")
	if strings.TrimSpace(name) == "" || strings.TrimSpace(digest) == "" {
		return Result{}, fmt.Errorf("keyed RepositoryCommit has no immutable source bundle")
	}
	return Result{Name: obj.GetName(), UID: string(obj.GetUID()), Bundle: commitbundle.BundleRef{Name: name, Digest: digest}}, nil
}
