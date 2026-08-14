// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package importapi owns the bounded, metadata-only Databricks discovery and
// resource-registration request contract.
package importapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/faroshq/provider-databricks/hostpolicy"
)

const (
	KindWarehouse               = "warehouse"
	KindTable                   = "table"
	MaxItems                    = 50
	MaxFieldBytes               = 2048
	maxResponseSize             = 4 << 20
	pageSize                    = 50
	metricViewUnsupportedReason = "Databricks METRIC_VIEW objects cannot be registered because query_table/v1 does not support metric-view query semantics."
)

type Connection struct{ Host, AuthType, Token string }
type Page[T any] struct {
	Items         []T    `json:"items"`
	NextPageToken string `json:"nextPageToken,omitempty"`
}

type Warehouse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	State             string `json:"state,omitempty"`
	WarehouseType     string `json:"warehouseType,omitempty"`
	Supported         bool   `json:"supported"`
	Unsupported       bool   `json:"unsupported,omitempty"`
	UnsupportedReason string `json:"unsupportedReason,omitempty"`
}
type Catalog struct {
	Name              string `json:"name"`
	Comment           string `json:"comment,omitempty"`
	CatalogType       string `json:"catalogType,omitempty"`
	Supported         bool   `json:"supported"`
	Unsupported       bool   `json:"unsupported,omitempty"`
	UnsupportedReason string `json:"unsupportedReason,omitempty"`
}
type Schema struct {
	Name              string `json:"name"`
	Catalog           string `json:"catalog"`
	Comment           string `json:"comment,omitempty"`
	Supported         bool   `json:"supported"`
	Unsupported       bool   `json:"unsupported,omitempty"`
	UnsupportedReason string `json:"unsupportedReason,omitempty"`
}
type Table struct {
	Name              string `json:"name"`
	Catalog           string `json:"catalog"`
	Schema            string `json:"schema"`
	TableType         string `json:"tableType,omitempty"`
	DataSourceFormat  string `json:"dataSourceFormat,omitempty"`
	Comment           string `json:"comment,omitempty"`
	Supported         bool   `json:"supported"`
	Unsupported       bool   `json:"unsupported,omitempty"`
	UnsupportedReason string `json:"unsupportedReason,omitempty"`
}

type Client struct{ HTTPClient *http.Client }

func NewClient(httpClient *http.Client) *Client { return &Client{HTTPClient: httpClient} }

func (c *Client) ListWarehouses(ctx context.Context, conn Connection, token string) (Page[Warehouse], error) {
	var payload struct {
		Warehouses []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			State         string `json:"state"`
			WarehouseType string `json:"warehouse_type"`
		} `json:"warehouses"`
		NextPageToken string `json:"next_page_token"`
	}
	query := url.Values{}
	query.Set("page_size", fmt.Sprint(pageSize))
	if token != "" {
		query.Set("page_token", token)
	}
	if err := c.get(ctx, conn, "/api/2.0/sql/warehouses", query, &payload); err != nil {
		return Page[Warehouse]{}, err
	}
	items := make([]Warehouse, 0, len(payload.Warehouses))
	for _, item := range payload.Warehouses {
		supported := item.ID != ""
		items = append(items, Warehouse{ID: item.ID, Name: item.Name, State: item.State, WarehouseType: item.WarehouseType, Supported: supported, Unsupported: !supported, UnsupportedReason: missingReason(item.ID, "warehouse ID")})
	}
	return Page[Warehouse]{Items: items, NextPageToken: payload.NextPageToken}, nil
}

func (c *Client) ListCatalogs(ctx context.Context, conn Connection, token string) (Page[Catalog], error) {
	var payload struct {
		Catalogs []struct {
			Name        string `json:"name"`
			Comment     string `json:"comment"`
			CatalogType string `json:"catalog_type"`
		} `json:"catalogs"`
		NextPageToken string `json:"next_page_token"`
	}
	query := pageQuery(token)
	if err := c.get(ctx, conn, "/api/2.1/unity-catalog/catalogs", query, &payload); err != nil {
		return Page[Catalog]{}, err
	}
	items := make([]Catalog, 0, len(payload.Catalogs))
	for _, item := range payload.Catalogs {
		supported := item.Name != ""
		items = append(items, Catalog{Name: item.Name, Comment: item.Comment, CatalogType: item.CatalogType, Supported: supported, Unsupported: !supported, UnsupportedReason: missingReason(item.Name, "catalog name")})
	}
	return Page[Catalog]{Items: items, NextPageToken: payload.NextPageToken}, nil
}

func (c *Client) ListSchemas(ctx context.Context, conn Connection, catalog, token string) (Page[Schema], error) {
	var payload struct {
		Schemas []struct {
			Name        string `json:"name"`
			CatalogName string `json:"catalog_name"`
			Comment     string `json:"comment"`
		} `json:"schemas"`
		NextPageToken string `json:"next_page_token"`
	}
	query := pageQuery(token)
	query.Set("catalog_name", catalog)
	if err := c.get(ctx, conn, "/api/2.1/unity-catalog/schemas", query, &payload); err != nil {
		return Page[Schema]{}, err
	}
	items := make([]Schema, 0, len(payload.Schemas))
	for _, item := range payload.Schemas {
		supported := item.Name != ""
		items = append(items, Schema{Name: item.Name, Catalog: item.CatalogName, Comment: item.Comment, Supported: supported, Unsupported: !supported, UnsupportedReason: missingReason(item.Name, "schema name")})
	}
	return Page[Schema]{Items: items, NextPageToken: payload.NextPageToken}, nil
}

func (c *Client) ListTables(ctx context.Context, conn Connection, catalog, schemaName, token string) (Page[Table], error) {
	var payload struct {
		Tables []struct {
			Name             string `json:"name"`
			CatalogName      string `json:"catalog_name"`
			SchemaName       string `json:"schema_name"`
			TableType        string `json:"table_type"`
			DataSourceFormat string `json:"data_source_format"`
			Comment          string `json:"comment"`
		} `json:"tables"`
		NextPageToken string `json:"next_page_token"`
	}
	query := pageQuery(token)
	query.Set("catalog_name", catalog)
	query.Set("schema_name", schemaName)
	query.Set("omit_columns", "true")
	query.Set("omit_properties", "true")
	query.Set("omit_username", "true")
	query.Set("include_manifest_capabilities", "false")
	if err := c.get(ctx, conn, "/api/2.1/unity-catalog/tables", query, &payload); err != nil {
		return Page[Table]{}, err
	}
	items := make([]Table, 0, len(payload.Tables))
	for _, item := range payload.Tables {
		supported := item.Name != "" && item.CatalogName != "" && item.SchemaName != ""
		reason := missingReason(item.Name, "table name")
		if item.Name != "" && item.CatalogName == "" {
			reason = "Databricks did not return a catalog name."
		}
		if item.Name != "" && item.CatalogName != "" && item.SchemaName == "" {
			reason = "Databricks did not return a schema name."
		}
		if strings.EqualFold(strings.TrimSpace(item.TableType), "METRIC_VIEW") {
			supported = false
			reason = metricViewUnsupportedReason
		}
		items = append(items, Table{Name: item.Name, Catalog: item.CatalogName, Schema: item.SchemaName, TableType: item.TableType, DataSourceFormat: item.DataSourceFormat, Comment: item.Comment, Supported: supported, Unsupported: !supported, UnsupportedReason: reason})
	}
	return Page[Table]{Items: items, NextPageToken: payload.NextPageToken}, nil
}

func pageQuery(token string) url.Values {
	query := url.Values{"max_results": []string{fmt.Sprint(pageSize)}}
	if token != "" {
		query.Set("page_token", token)
	}
	return query
}
func missingReason(value, field string) string {
	if value == "" {
		return "Databricks did not return a " + field + "."
	}
	return ""
}

func (c *Client) get(ctx context.Context, conn Connection, path string, query url.Values, output any) error {
	if conn.AuthType != "pat" {
		return errors.New("unsupported Databricks auth type")
	}
	if strings.TrimSpace(conn.Token) == "" {
		return errors.New("Databricks credential is required")
	}
	base, err := WorkspaceURL(conn.Host)
	if err != nil {
		return err
	}
	endpoint := *base
	endpoint.Path, endpoint.RawQuery = path, query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("build discovery request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Accept", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Databricks discovery request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Databricks discovery returned HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read Databricks metadata: %w", err)
	}
	if len(payload) > maxResponseSize {
		return fmt.Errorf("Databricks metadata response exceeds %d bytes", maxResponseSize)
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return errors.New("decode Databricks metadata: invalid JSON response")
	}
	return nil
}

func WorkspaceURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid Databricks workspace host")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, errors.New("Databricks workspace host must use the default https port")
	}
	if !hostpolicy.AllowedWorkspaceHost(host, nil) {
		return nil, errors.New("not an allowed Databricks workspace host")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

type Request struct {
	Kind          string `json:"kind"`
	ConnectionRef string `json:"connectionRef"`
	WarehouseRef  string `json:"warehouseRef,omitempty"`
	Items         []Item `json:"items"`
}
type Item struct {
	Name        string `json:"name"`
	WarehouseID string `json:"warehouseID,omitempty"`
	Catalog     string `json:"catalog,omitempty"`
	Schema      string `json:"schema,omitempty"`
	Table       string `json:"table,omitempty"`
}
type NormalizedItem struct {
	Index int
	Name  string
	Spec  map[string]any
}

func (r Request) NormalizedKind() string { return strings.ToLower(strings.TrimSpace(r.Kind)) }
func (r Request) Validate() error {
	kind := r.NormalizedKind()
	if kind != KindWarehouse && kind != KindTable {
		return errors.New("kind must be warehouse or table")
	}
	if strings.TrimSpace(r.ConnectionRef) == "" || len(r.ConnectionRef) > 253 {
		return errors.New("connectionRef is required and must be at most 253 bytes")
	}
	if problems := validation.NameIsDNSSubdomain(strings.TrimSpace(r.ConnectionRef), false); len(problems) > 0 {
		return errors.New("connectionRef must name a Kubernetes resource")
	}
	if len(r.Items) == 0 || len(r.Items) > MaxItems {
		return fmt.Errorf("items must contain between 1 and %d entries", MaxItems)
	}
	if kind == KindTable && strings.TrimSpace(r.WarehouseRef) == "" {
		return errors.New("warehouseRef is required for table registration")
	}
	if kind == KindTable {
		if problems := validation.NameIsDNSSubdomain(strings.TrimSpace(r.WarehouseRef), false); len(problems) > 0 {
			return errors.New("warehouseRef must name a Kubernetes resource")
		}
	}
	return nil
}

func Normalize(request Request, index int) (NormalizedItem, error) {
	if index < 0 || index >= len(request.Items) {
		return NormalizedItem{}, errors.New("item index is out of range")
	}
	item := request.Items[index]
	item.Name = strings.TrimSpace(item.Name)
	if problems := validation.NameIsDNSSubdomain(item.Name, false); len(problems) > 0 {
		return NormalizedItem{}, errors.New("name must be a DNS subdomain")
	}
	spec := map[string]any{"connectionRef": strings.TrimSpace(request.ConnectionRef)}
	if request.NormalizedKind() == KindWarehouse {
		item.WarehouseID = strings.TrimSpace(item.WarehouseID)
		if item.WarehouseID == "" || item.Catalog != "" || item.Schema != "" || item.Table != "" {
			return NormalizedItem{}, errors.New("warehouseID is required and table fields are not allowed")
		}
		if len(item.WarehouseID) > MaxFieldBytes {
			return NormalizedItem{}, errors.New("warehouseID is too long")
		}
		if strings.ContainsAny(item.WarehouseID, "\r\n\x00") {
			return NormalizedItem{}, errors.New("warehouseID is invalid")
		}
		spec["warehouseID"] = item.WarehouseID
	} else {
		item.Catalog, item.Schema, item.Table = strings.TrimSpace(item.Catalog), strings.TrimSpace(item.Schema), strings.TrimSpace(item.Table)
		if item.Catalog == "" || item.Schema == "" || item.Table == "" || item.WarehouseID != "" {
			return NormalizedItem{}, errors.New("catalog, schema, and table are required and warehouseID is not allowed")
		}
		for _, value := range []string{item.Catalog, item.Schema, item.Table} {
			if len(value) > 255 || strings.ContainsAny(value, "\r\n\x00") {
				return NormalizedItem{}, errors.New("table identifier is invalid")
			}
		}
		spec["warehouseRef"], spec["catalog"], spec["schema"], spec["table"] = strings.TrimSpace(request.WarehouseRef), item.Catalog, item.Schema, item.Table
	}
	return NormalizedItem{Index: index, Name: item.Name, Spec: spec}, nil
}

func SpecEqual(object *unstructured.Unstructured, expected map[string]any) bool {
	if object == nil {
		return false
	}
	actual, ok, err := unstructured.NestedMap(object.Object, "spec")
	return err == nil && ok && reflect.DeepEqual(actual, expected)
}
func ResourceForKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), KindTable) {
		return "tables"
	}
	return "warehouses"
}
