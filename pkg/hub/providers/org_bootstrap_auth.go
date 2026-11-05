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

package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

var (
	ErrOrgProviderBootstrapUnauthorized = errors.New("organization provider bootstrap not authenticated")
	ErrOrgProviderBootstrapUnavailable  = errors.New("organization provider bootstrap authentication unavailable")
)

// OrgBootstrapIdentity comes from live kcp state verified by TokenReview. The
// organization, provider workspace, and selected Edge route never come from
// headers or the submitted declaration.
type OrgBootstrapIdentity struct {
	RegistrationUID string
	OrgUUID         string
	Cluster         string
	WorkspacePath   string
	EdgeWorkspace   string
	EdgeName        string
}

type OrgProviderBootstrapAuthenticator func(context.Context, *http.Request, string) (OrgBootstrapIdentity, error)

// NewOrgProviderBootstrapAuthenticator authenticates the restricted runtime
// identity before a CatalogEntry exists. A JWT cluster is only a bounded
// routing hint: TokenReview, the LogicalCluster, its hub-owned Workspace, the
// selected Edge route, and the live ServiceAccount must all agree. No result
// is cached, so registration removal and credential replacement take effect
// immediately.
func NewOrgProviderBootstrapAuthenticator(config *rest.Config) (OrgProviderBootstrapAuthenticator, error) {
	if config == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	return func(ctx context.Context, req *http.Request, name string) (OrgBootstrapIdentity, error) {
		empty := OrgBootstrapIdentity{}
		token, ok := bearerToken(req)
		cluster := orgBootstrapClusterHint(token)
		if !ok || cluster == "" || len(validation.IsDNS1123Label(name)) != 0 {
			return empty, ErrOrgProviderBootstrapUnauthorized
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(config.Host, cluster)
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return empty, fmt.Errorf("%w: %v", ErrOrgProviderBootstrapUnavailable, err)
		}
		review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
		if err != nil {
			return empty, fmt.Errorf("%w: token review: %v", ErrOrgProviderBootstrapUnavailable, err)
		}
		if review.Status.User.Username != ProviderSAUsername || !TokenReviewMatchesCluster(review.Status, cluster) {
			return empty, ErrOrgProviderBootstrapUnauthorized
		}
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return empty, fmt.Errorf("%w: %v", ErrOrgProviderBootstrapUnavailable, err)
		}
		lc, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return empty, orgBootstrapReadError(err)
		}
		path := lc.GetAnnotations()["kcp.io/path"]
		orgUUID, providerName, ok := kcppaths.SplitOrgProviderPath(path)
		if !ok || providerName != name || lc.GetAnnotations()["kcp.io/cluster"] != cluster || lc.GetDeletionTimestamp() != nil {
			return empty, ErrOrgProviderBootstrapUnauthorized
		}
		parentConfig := rest.CopyConfig(config)
		parentConfig.Host = apiurl.KCPClusterURL(config.Host, kcppaths.OrgProvidersParent(orgUUID))
		parent, err := dynamic.NewForConfig(parentConfig)
		if err != nil {
			return empty, fmt.Errorf("%w: %v", ErrOrgProviderBootstrapUnavailable, err)
		}
		ws, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return empty, orgBootstrapReadError(err)
		}
		registeredCluster, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
		annotations := ws.GetAnnotations()
		edgeWorkspace := annotations[OrgProviderEdgeRouteWorkspaceAnnotation]
		edgeName := annotations[OrgProviderEdgeRouteEdgeAnnotation]
		if ws.GetAnnotations()[OrgProviderBootstrapPolicyAnnotation] != OrgProviderBootstrapPolicy || orgProviderWorkspaceDeleting(ws) || registeredCluster != cluster || edgeWorkspace == "" || len(validation.IsDNS1123Label(edgeName)) != 0 {
			return empty, ErrOrgProviderBootstrapUnauthorized
		}
		sa, err := client.CoreV1().ServiceAccounts(ProviderSANamespace).Get(ctx, ProviderSAName, metav1.GetOptions{})
		if err != nil {
			return empty, orgBootstrapReadError(err)
		}
		if sa.Annotations[OrgProviderBootstrapPolicyAnnotation] != OrgProviderBootstrapPolicy || sa.DeletionTimestamp != nil || string(sa.UID) == "" || string(sa.UID) != review.Status.User.UID {
			return empty, ErrOrgProviderBootstrapUnauthorized
		}
		return OrgBootstrapIdentity{
			RegistrationUID: string(ws.GetUID()), OrgUUID: orgUUID, Cluster: cluster, WorkspacePath: path,
			EdgeWorkspace: edgeWorkspace, EdgeName: edgeName,
		}, nil
	}, nil
}

func orgBootstrapReadError(err error) error {
	if apierrors.IsNotFound(err) {
		return ErrOrgProviderBootstrapUnauthorized
	}
	return fmt.Errorf("%w: %v", ErrOrgProviderBootstrapUnavailable, err)
}

func orgBootstrapClusterHint(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) > 16*1024 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Legacy     string `json:"kubernetes.io/serviceaccount/clusterName"`
		Kubernetes struct {
			Cluster string `json:"clusterName"`
		} `json:"kubernetes.io"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	cluster := claims.Kubernetes.Cluster
	if cluster == "" {
		cluster = claims.Legacy
	}
	if len(validation.IsDNS1123Label(cluster)) != 0 {
		return ""
	}
	return cluster
}
