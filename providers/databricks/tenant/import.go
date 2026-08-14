// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/controller/shared"
	"github.com/faroshq/provider-databricks/importapi"
)

const (
	MaxRequestBytes = 64 << 10
	maxQueryValue   = 2048
)

var warehousesGVR = databricksv1alpha1.SchemeGroupVersion.WithResource("warehouses")

type DiscoveryClient interface {
	ListWarehouses(context.Context, importapi.Connection, string) (importapi.Page[importapi.Warehouse], error)
	ListCatalogs(context.Context, importapi.Connection, string) (importapi.Page[importapi.Catalog], error)
	ListSchemas(context.Context, importapi.Connection, string, string) (importapi.Page[importapi.Schema], error)
	ListTables(context.Context, importapi.Connection, string, string, string) (importapi.Page[importapi.Table], error)
}

type ImportFactory interface {
	ResolveConnection(context.Context, string, string) (importapi.Connection, error)
	AuthorizeResource(context.Context, string, string, string, string, string) error
	For(string, string) (dynamic.Interface, error)
}

type ImportHandler struct {
	Factory ImportFactory
	Remote  DiscoveryClient
}

func NewImportHandler(factory ImportFactory, remote DiscoveryClient) *ImportHandler {
	if concrete, ok := factory.(*ClientFactory); ok && concrete == nil {
		factory = nil
	}
	return &ImportHandler{Factory: factory, Remote: remote}
}

func (h *ImportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/discovery/") {
		h.serveDiscovery(w, r)
		return
	}
	if r.URL.Path == "/api/v1/registrations" {
		if r.Method != http.MethodPost {
			writeImportError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
			return
		}
		h.serveRegistration(w, r)
		return
	}
	writeImportError(w, http.StatusNotFound, "NotFound", "route not found")
}

func (h *ImportHandler) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	identity, err := importRequestIdentity(r)
	if err != nil {
		writeImportError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, "/api/v1/discovery/")
	query, err := parseDiscoveryQuery(r.URL.Query(), resource)
	if err != nil {
		writeImportError(w, http.StatusBadRequest, "ValidationError", err.Error())
		return
	}
	if h == nil || h.Factory == nil || h.Remote == nil {
		writeImportError(w, http.StatusServiceUnavailable, "Unavailable", "Databricks discovery is unavailable")
		return
	}
	if err := h.Factory.AuthorizeResource(r.Context(), identity.clusterID, identity.token, "connections", query.connectionRef, "get"); err != nil {
		writeImportOperationError(w, err, "caller is not allowed to read this connection")
		return
	}
	connection, err := h.Factory.ResolveConnection(r.Context(), identity.clusterID, query.connectionRef)
	if err != nil {
		writeImportOperationError(w, err, "Databricks connection is unavailable")
		return
	}
	var result any
	switch resource {
	case "warehouses":
		result, err = h.Remote.ListWarehouses(r.Context(), connection, query.pageToken)
	case "catalogs":
		result, err = h.Remote.ListCatalogs(r.Context(), connection, query.pageToken)
	case "schemas":
		result, err = h.Remote.ListSchemas(r.Context(), connection, query.catalog, query.pageToken)
	case "tables":
		result, err = h.Remote.ListTables(r.Context(), connection, query.catalog, query.schema, query.pageToken)
	default:
		writeImportError(w, http.StatusNotFound, "NotFound", "discovery resource not found")
		return
	}
	if err != nil {
		writeImportError(w, http.StatusBadGateway, "DiscoveryFailed", "Databricks discovery failed")
		return
	}
	writeImportJSON(w, http.StatusOK, result)
}

func (h *ImportHandler) serveRegistration(w http.ResponseWriter, r *http.Request) {
	identity, err := importRequestIdentity(r)
	if err != nil {
		writeImportError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writeImportError(w, http.StatusUnsupportedMediaType, "UnsupportedMediaType", "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil || len(body) > MaxRequestBytes {
		writeImportError(w, http.StatusRequestEntityTooLarge, "RequestTooLarge", "registration request is too large")
		return
	}
	var request importapi.Request
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeImportError(w, http.StatusBadRequest, "ValidationError", "registration request is invalid JSON")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeImportError(w, http.StatusBadRequest, "ValidationError", err.Error())
		return
	}
	request.Kind = request.NormalizedKind()
	if err := request.Validate(); err != nil {
		writeImportError(w, http.StatusBadRequest, "ValidationError", err.Error())
		return
	}
	request.ConnectionRef = strings.TrimSpace(request.ConnectionRef)
	request.WarehouseRef = strings.TrimSpace(request.WarehouseRef)
	normalized := make([]importapi.NormalizedItem, len(request.Items))
	seenNames := make(map[string]struct{}, len(request.Items))
	for i := range request.Items {
		item, err := importapi.Normalize(request, i)
		if err != nil {
			writeImportError(w, http.StatusBadRequest, "ValidationError", fmt.Sprintf("item %d: %v", i, err))
			return
		}
		if _, exists := seenNames[item.Name]; exists {
			writeImportError(w, http.StatusBadRequest, "ValidationError", fmt.Sprintf("item %d: resource name is duplicated", i))
			return
		}
		seenNames[item.Name] = struct{}{}
		normalized[i] = item
	}
	if h == nil || h.Factory == nil {
		writeImportError(w, http.StatusServiceUnavailable, "Unavailable", "registration is unavailable")
		return
	}
	if err := h.Factory.AuthorizeResource(r.Context(), identity.clusterID, identity.token, "connections", request.ConnectionRef, "get"); err != nil {
		writeImportOperationError(w, err, "caller is not allowed to read this connection")
		return
	}
	if _, err := h.Factory.ResolveConnection(r.Context(), identity.clusterID, request.ConnectionRef); err != nil {
		writeImportOperationError(w, err, "Databricks connection is unavailable")
		return
	}
	resourceName := importapi.ResourceForKind(request.Kind)
	for _, verb := range []string{"get", "create"} {
		if err := h.Factory.AuthorizeResource(r.Context(), identity.clusterID, identity.token, resourceName, "", verb); err != nil {
			writeImportOperationError(w, err, "caller is not allowed to register this resource")
			return
		}
	}
	dyn, err := h.Factory.For(identity.clusterID, identity.token)
	if err != nil {
		writeImportError(w, http.StatusBadGateway, "Unavailable", "tenant resource client is unavailable")
		return
	}
	if request.Kind == importapi.KindTable {
		if err := preflightTables(r.Context(), dyn, request); err != nil {
			writeImportOperationError(w, err, "table registration preflight failed")
			return
		}
	}
	results := make([]RegistrationResult, 0, len(normalized))
	for _, item := range normalized {
		results = append(results, registerOne(r.Context(), dyn, request, item))
	}
	writeImportJSON(w, http.StatusOK, struct {
		Results []RegistrationResult `json:"results"`
	}{Results: results})
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("registration request must contain one JSON object")
	}
	return nil
}

type RegistrationResult struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

func registerOne(ctx context.Context, dyn dynamic.Interface, request importapi.Request, item importapi.NormalizedItem) RegistrationResult {
	result := RegistrationResult{Index: item.Index, Name: item.Name}
	resource := dyn.Resource(resourceForRequest(request))
	existing, err := resource.Get(ctx, item.Name, metav1.GetOptions{})
	if err == nil {
		if importapi.SpecEqual(existing, item.Spec) {
			result.State = "existing"
		} else {
			result.State, result.Message = "conflict", "a resource with this name has a different specification"
		}
		return result
	}
	if !apierrors.IsNotFound(err) {
		result.State, result.Message = "failed", "could not inspect the existing resource"
		return result
	}
	if _, err := resource.Create(ctx, objectForItem(request, item), metav1.CreateOptions{}); err == nil {
		result.State = "created"
		return result
	} else if !apierrors.IsAlreadyExists(err) {
		result.State, result.Message = "failed", "could not create the resource"
		return result
	}
	existing, err = resource.Get(ctx, item.Name, metav1.GetOptions{})
	if err == nil && importapi.SpecEqual(existing, item.Spec) {
		result.State = "existing"
		return result
	}
	if err == nil {
		result.State, result.Message = "conflict", "a resource with this name has a different specification"
		return result
	}
	result.State, result.Message = "failed", "could not verify the concurrently created resource"
	return result
}

func objectForItem(request importapi.Request, item importapi.NormalizedItem) *unstructured.Unstructured {
	kind := "Warehouse"
	if request.Kind == importapi.KindTable {
		kind = "Table"
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": databricksv1alpha1.SchemeGroupVersion.String(), "kind": kind, "metadata": map[string]any{"name": item.Name}, "spec": item.Spec}}
}

func preflightTables(ctx context.Context, dyn dynamic.Interface, request importapi.Request) error {
	warehouse, err := dyn.Resource(warehousesGVR).Get(ctx, strings.TrimSpace(request.WarehouseRef), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &importError{http.StatusNotFound, "NotFound", "warehouseRef is not visible to the caller"}
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return &importError{http.StatusForbidden, "Forbidden", "caller is not allowed to read warehouseRef"}
		}
		return &importError{http.StatusBadGateway, "PreflightFailed", "could not verify warehouseRef"}
	}
	actual, ok, valueErr := unstructured.NestedString(warehouse.Object, "spec", "connectionRef")
	if valueErr != nil || !ok || actual != request.ConnectionRef {
		return &importError{http.StatusConflict, "ConnectionMismatch", "warehouseRef belongs to a different connection"}
	}
	return nil
}

func resourceForRequest(request importapi.Request) schema.GroupVersionResource {
	if request.Kind == importapi.KindTable {
		return tablesGVR
	}
	return warehousesGVR
}

type importIdentity struct{ clusterID, tenant, token string }

func importRequestIdentity(r *http.Request) (importIdentity, error) {
	if r == nil {
		return importIdentity{}, errors.New("request is required")
	}
	identity := importIdentity{clusterID: strings.TrimSpace(r.Header.Get("X-Faros-Cluster")), tenant: strings.TrimSpace(r.Header.Get("X-Faros-Tenant")), token: bearerToken(r)}
	if identity.tenant == "" || identity.clusterID == "" {
		return importIdentity{}, errors.New("tenant identity is required")
	}
	if strings.ContainsAny(identity.tenant, "\r\n") || strings.ContainsAny(identity.clusterID, "/\\\r\n") {
		return importIdentity{}, errors.New("tenant identity is invalid")
	}
	if identity.token == "" {
		return importIdentity{}, errors.New("bearer token is required")
	}
	return identity, nil
}

type discoveryQuery struct{ connectionRef, catalog, schema, pageToken string }

func parseDiscoveryQuery(values url.Values, resource string) (discoveryQuery, error) {
	allowed := map[string]bool{"connectionRef": true, "pageToken": true}
	if resource == "schemas" || resource == "tables" {
		allowed["catalog"] = true
	}
	if resource == "tables" {
		allowed["schema"] = true
	}
	for key, value := range values {
		if !allowed[key] {
			return discoveryQuery{}, fmt.Errorf("unsupported query parameter %q", key)
		}
		if len(value) != 1 {
			return discoveryQuery{}, fmt.Errorf("query parameter %q must occur once", key)
		}
	}
	result := discoveryQuery{connectionRef: strings.TrimSpace(values.Get("connectionRef")), catalog: strings.TrimSpace(values.Get("catalog")), schema: strings.TrimSpace(values.Get("schema")), pageToken: values.Get("pageToken")}
	if result.connectionRef == "" || len(result.connectionRef) > 253 {
		return discoveryQuery{}, errors.New("connectionRef is required and must be at most 253 bytes")
	}
	if len(result.pageToken) > maxQueryValue {
		return discoveryQuery{}, errors.New("pageToken is too long")
	}
	if (resource == "schemas" || resource == "tables") && result.catalog == "" {
		return discoveryQuery{}, errors.New("catalog is required")
	}
	if resource == "tables" && result.schema == "" {
		return discoveryQuery{}, errors.New("schema is required")
	}
	if len(result.catalog) > 255 || len(result.schema) > 255 {
		return discoveryQuery{}, errors.New("catalog or schema is too long")
	}
	return result, nil
}

type importError struct {
	status          int
	reason, message string
}

func (e *importError) Error() string { return e.message }

func writeImportOperationError(w http.ResponseWriter, err error, fallback string) {
	status, reason, message := http.StatusBadGateway, "OperationFailed", fallback
	var typed *importError
	if errors.As(err, &typed) {
		status, reason, message = typed.status, typed.reason, typed.message
	} else if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		status, reason = http.StatusForbidden, "Forbidden"
	} else if apierrors.IsNotFound(err) {
		status, reason = http.StatusNotFound, "NotFound"
	}
	writeImportError(w, status, reason, message)
}

func writeImportError(w http.ResponseWriter, status int, reason, message string) {
	writeImportJSON(w, status, struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}{reason, message})
}
func writeImportJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (f *ClientFactory) ResolveConnection(ctx context.Context, clusterID, name string) (importapi.Connection, error) {
	if f == nil {
		return importapi.Connection{}, errors.New("provider authority client unavailable")
	}
	providerClient, err := f.AuthorityClient(ctx, clusterID)
	if err != nil {
		return importapi.Connection{}, err
	}
	connection, err := shared.ResolveConnection(ctx, providerClient, strings.TrimSpace(name))
	if err != nil {
		return importapi.Connection{}, apierrors.NewNotFound(databricksv1alpha1.SchemeGroupVersion.WithResource("connections").GroupResource(), name)
	}
	if connection.Spec.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		return importapi.Connection{}, &importError{http.StatusBadRequest, "UnsupportedAuthType", "connection authType is unsupported"}
	}
	token, err := shared.ResolveBearerToken(ctx, providerClient, connection)
	if err != nil {
		return importapi.Connection{}, errors.New("connection credential is unavailable")
	}
	return importapi.Connection{Host: connection.Spec.Host, AuthType: string(connection.Spec.AuthType), Token: token}, nil
}

func (f *ClientFactory) AuthorizeResource(ctx context.Context, clusterID, token, resource, name, verb string) error {
	if f == nil {
		return errors.New("tenant authorization unavailable")
	}
	authorizer, err := f.AuthorizationFor(clusterID, token)
	if err != nil {
		return err
	}
	review, err := authorizer.SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Group: databricksv1alpha1.GroupName, Version: databricksv1alpha1.Version, Resource: resource, Name: name, Verb: verb}}}, metav1.CreateOptions{})
	if err != nil {
		return errors.New("tenant authorization request failed")
	}
	if review == nil || !review.Status.Allowed {
		return apierrors.NewForbidden(databricksv1alpha1.SchemeGroupVersion.WithResource(resource).GroupResource(), name, errors.New("caller is not authorized"))
	}
	return nil
}
