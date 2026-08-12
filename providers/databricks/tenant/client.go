// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	multicluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/queryapi"
)

var (
	tablesGVR = databricksv1alpha1.SchemeGroupVersion.WithResource("tables")
)

type ClientFactory struct {
	baseHost  string
	baseTLS   rest.TLSClientConfig
	authority ClusterAuthority

	mu      sync.RWMutex
	hot     map[string]dynamic.Interface
	authHot map[string]authorizationv1client.AuthorizationV1Interface
}

// ClusterAuthority is the provider-owned multicluster manager. Its client is
// authenticated with the provider ServiceAccount, so provider-owned Tables,
// Warehouses, Connections, and credential Secrets are resolved with provider
// authority rather than the forwarded caller token.
type ClusterAuthority interface {
	GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error)
}

func NewClientFactory(base *rest.Config) *ClientFactory {
	if base == nil {
		return nil
	}
	baseHost, err := stripClusterSuffix(base.Host)
	if err != nil {
		baseHost = strings.TrimRight(base.Host, "/")
	}
	tls := base.TLSClientConfig
	tls.CertData = nil
	tls.CertFile = ""
	tls.KeyData = nil
	tls.KeyFile = ""
	return &ClientFactory{
		baseHost: baseHost,
		baseTLS:  tls,
		hot:      make(map[string]dynamic.Interface),
		authHot:  make(map[string]authorizationv1client.AuthorizationV1Interface),
	}
}

// SetAuthority wires the multicluster manager after it has been constructed.
// The HTTP server is started only after this is set when controller startup is
// available; a nil authority makes direct actions fail closed.
func (f *ClientFactory) SetAuthority(authority ClusterAuthority) {
	if f != nil {
		f.authority = authority
	}
}

func (f *ClientFactory) AuthorityClient(ctx context.Context, clusterID string) (client.Client, error) {
	if f == nil || f.authority == nil {
		return nil, errors.New("provider authority client unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" || clusterID == "." || clusterID == ".." || url.PathEscape(clusterID) != clusterID {
		return nil, errors.New("invalid tenant logical-cluster ID")
	}
	cl, err := f.authority.GetCluster(ctx, multicluster.ClusterName(clusterID))
	if err != nil {
		return nil, fmt.Errorf("provider authority cluster %q: %w", clusterID, err)
	}
	if cl == nil || cl.GetClient() == nil {
		return nil, fmt.Errorf("provider authority cluster %q has no client", clusterID)
	}
	return cl.GetClient(), nil
}

func (f *ClientFactory) For(clusterID, token string) (dynamic.Interface, error) {
	cfg, err := f.configFor(clusterID, token)
	if err != nil {
		return nil, err
	}
	key := clusterID + ":" + hashToken(token)

	f.mu.RLock()
	dyn, ok := f.hot[key]
	f.mu.RUnlock()
	if ok {
		return dyn, nil
	}

	dyn, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for cluster %q: %w", clusterID, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.hot[key]; ok {
		return existing, nil
	}
	if f.hot == nil {
		f.hot = make(map[string]dynamic.Interface)
	}
	f.hot[key] = dyn
	return dyn, nil
}

func (f *ClientFactory) configFor(clusterID, token string) (*rest.Config, error) {
	if f == nil {
		return nil, errors.New("tenant client unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" || clusterID == "." || clusterID == ".." || url.PathEscape(clusterID) != clusterID {
		return nil, errors.New("invalid tenant logical-cluster ID")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("no bearer token on request; cannot act on the tenant's behalf")
	}
	return &rest.Config{Host: f.baseHost + "/clusters/" + clusterID, BearerToken: token, TLSClientConfig: f.baseTLS}, nil
}

// AuthorizationFor returns a caller-token-scoped authorization client for
// delegated SelfSubjectAccessReview checks. The provider's bootstrap
// credential is never used to authorize an action on behalf of a caller.
func (f *ClientFactory) AuthorizationFor(clusterID, token string) (authorizationv1client.AuthorizationV1Interface, error) {
	cfg, err := f.configFor(clusterID, token)
	if err != nil {
		return nil, err
	}
	key := clusterID + ":" + hashToken(token)
	f.mu.RLock()
	client, ok := f.authHot[key]
	f.mu.RUnlock()
	if ok {
		return client, nil
	}
	client, err = authorizationv1client.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("authorization client for cluster %q: %w", clusterID, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.authHot[key]; ok {
		return existing, nil
	}
	if f.authHot == nil {
		f.authHot = make(map[string]authorizationv1client.AuthorizationV1Interface)
	}
	f.authHot[key] = client
	return client, nil
}

func (f *ClientFactory) TableResolverForRequest(r *http.Request) queryapi.TableResolver {
	if f == nil {
		return queryapi.UnavailableResolver{Message: "tenant client unavailable (provider kubeconfig not set)"}
	}
	ident := identityFromRequest(r)
	return tableResolver{factory: f, identity: ident}
}

type identity struct {
	tenantPath string
	clusterID  string
	token      string
}

func identityFromRequest(r *http.Request) identity {
	return identity{
		tenantPath: r.Header.Get("X-Faros-Tenant"),
		clusterID:  r.Header.Get("X-Faros-Cluster"),
		token:      bearerToken(r),
	}
}

func bearerToken(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

type tableResolver struct {
	factory  *ClientFactory
	identity identity
}

func (r tableResolver) ListTables(ctx context.Context) (map[string]queryapi.TableRef, error) {
	dyn, err := r.dynamicClient()
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(tablesGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]queryapi.TableRef, len(list.Items))
	for _, item := range list.Items {
		ref, ok := tableRefFromObject(item)
		if ok {
			out[item.GetName()] = ref
		}
	}
	return out, nil
}

func (r tableResolver) GetTable(ctx context.Context, name string) (queryapi.TableRef, bool, error) {
	dyn, err := r.dynamicClient()
	if err != nil {
		return queryapi.TableRef{}, false, err
	}
	item, err := dyn.Resource(tablesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return queryapi.TableRef{}, false, nil
	}
	if err != nil {
		return queryapi.TableRef{}, false, err
	}
	ref, ok := tableRefFromObject(*item)
	if !ok {
		return queryapi.TableRef{}, false, nil
	}
	return ref, true, nil
}

func (r tableResolver) dynamicClient() (dynamic.Interface, error) {
	if r.identity.tenantPath == "" {
		return nil, errors.New("no tenant identity on this request; bearer token did not resolve to a workspace")
	}
	if r.identity.clusterID == "" {
		return nil, errors.New("no workspace cluster on this request (X-Faros-Cluster missing)")
	}
	if r.factory == nil {
		return nil, errors.New("tenant client unavailable (provider kubeconfig not set)")
	}
	return r.factory.For(r.identity.clusterID, r.identity.token)
}

// GetTableSchema returns the controller-cached column schema from the Table
// resource's status. Callers (describe_table) surface it so clients never have
// to guess column names against the bounded query contract.
func (r tableResolver) GetTableSchema(ctx context.Context, name string) ([]queryapi.QueryColumn, string, error) {
	dyn, err := r.dynamicClient()
	if err != nil {
		return nil, "", err
	}
	item, err := dyn.Resource(tablesGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	rawColumns, _, _ := unstructured.NestedSlice(item.Object, "status", "columns")
	columns := make([]queryapi.QueryColumn, 0, len(rawColumns))
	for _, raw := range rawColumns {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		columnName, _ := entry["name"].(string)
		if strings.TrimSpace(columnName) == "" {
			continue
		}
		columnType, _ := entry["type"].(string)
		columns = append(columns, queryapi.QueryColumn{Name: columnName, Type: columnType})
	}
	refreshedAt, _, _ := unstructured.NestedString(item.Object, "status", "refreshedAt")
	return columns, refreshedAt, nil
}

func tableRefFromObject(item unstructured.Unstructured) (queryapi.TableRef, bool) {
	catalog, _, _ := unstructured.NestedString(item.Object, "spec", "catalog")
	schemaName, _, _ := unstructured.NestedString(item.Object, "spec", "schema")
	table, _, _ := unstructured.NestedString(item.Object, "spec", "table")
	if strings.TrimSpace(catalog) == "" || strings.TrimSpace(schemaName) == "" || strings.TrimSpace(table) == "" {
		return queryapi.TableRef{}, false
	}
	return queryapi.TableRef{Catalog: catalog, Schema: schemaName, Table: table}, true
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func stripClusterSuffix(host string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse base kubeconfig host %q: %w", host, err)
	}
	idx := strings.Index(u.Path, "/clusters/")
	if idx < 0 {
		return strings.TrimRight(host, "/"), nil
	}
	u.Path = u.Path[:idx]
	return strings.TrimRight(u.String(), "/"), nil
}
