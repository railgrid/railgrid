// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package backend isolates Databricks validation behind a small interface.
package backend

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/faroshq/provider-databricks/queryapi"
)

const (
	defaultStatementWaitTimeout     = "10s"
	statementOnWaitTimeoutCancel    = "CANCEL"
	statementStatusSucceeded        = "SUCCEEDED"
	statementHTTPFailureSafeMessage = "databricks statement failed"
)

type StatementClient struct {
	HTTPClient                   *http.Client
	WaitTimeout                  string
	AllowedWorkspaceHostSuffixes []string
	// AllowInsecureWorkspaceHost is only for loopback httptest URLs.
	AllowInsecureWorkspaceHost bool
}

// QueryExecutionTarget is assembled by a tenant-scoped direct action executor
// after it resolves the tenant-owned Table -> Warehouse -> Connection -> Secret
// chain. Callers never supply the credential or backend endpoint through MCP.
type QueryExecutionTarget struct {
	Table          queryapi.TableRef
	Connection     queryapi.ConnectionRef
	Warehouse      queryapi.WarehouseRef
	BearerToken    string
	Projection     []string
	Limit          int
	AllowedColumns []string
}

type QueryExecutor interface {
	ExecuteTableQuery(context.Context, QueryExecutionTarget) (queryapi.QueryTableResult, error)
}

func NewStatementClient(httpClient *http.Client) StatementClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return StatementClient{
		HTTPClient:  httpClient,
		WaitTimeout: defaultStatementWaitTimeout,
	}
}

// NewDevelopmentLoopbackStatementClient is an explicit local-E2E escape hatch
// for an HTTPS fake Databricks endpoint using a self-signed certificate. TLS
// verification is relaxed only for loopback destinations; remote Databricks
// hosts continue to use the normal verified transport. Production wiring never
// calls this constructor by default.
func NewDevelopmentLoopbackStatementClient() StatementClient {
	secure := transportClone(http.DefaultTransport)
	insecure := secure.Clone()
	if insecure.TLSClientConfig == nil {
		insecure.TLSClientConfig = &tls.Config{}
	} else {
		insecure.TLSClientConfig = insecure.TLSClientConfig.Clone()
	}
	insecure.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // explicit loopback-only development opt-in
	return StatementClient{
		HTTPClient: &http.Client{
			Transport: loopbackTransport{secure: secure, insecure: insecure},
			Timeout:   30 * time.Second,
		},
		AllowInsecureWorkspaceHost: true,
	}
}

func transportClone(rt http.RoundTripper) *http.Transport {
	if tr, ok := rt.(*http.Transport); ok {
		return tr.Clone()
	}
	return (&http.Transport{}).Clone()
}

type loopbackTransport struct {
	secure   *http.Transport
	insecure *http.Transport
}

func (t loopbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && isLoopbackHost(strings.ToLower(req.URL.Hostname())) {
		return t.insecure.RoundTrip(req)
	}
	return t.secure.RoundTrip(req)
}

var _ QueryExecutor = StatementClient{}

// ExecuteTableQuery executes a provider-constructed SELECT and returns only a
// bounded structured result. The SQL builder rejects arbitrary expressions and
// raw SQL before the request can reach Databricks.
func (c StatementClient) ExecuteTableQuery(ctx context.Context, target QueryExecutionTarget) (queryapi.QueryTableResult, error) {
	request, err := queryapi.NormalizeQueryRequest(queryapi.QueryTableRequest{
		ActionVersion: queryapi.ActionVersionV1,
		TableRef:      "table",
		Columns:       target.Projection,
		Limit:         target.Limit,
	})
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	sql, err := queryapi.SelectTableSQL(target.Table, request.Columns, request.Limit, target.AllowedColumns)
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	response, err := c.executeStatement(ctx, queryapi.TableTarget{
		Table:      target.Table,
		Connection: target.Connection,
		Warehouse:  target.Warehouse,
		Credential: queryapi.Credential{BearerToken: target.BearerToken},
	}, sql)
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	result := queryResultFromStatement(response, request.TableRef)
	return boundQueryResult(result), nil
}

func (c StatementClient) executeStatement(ctx context.Context, target queryapi.TableTarget, sql string) (statementResponse, error) {
	if strings.TrimSpace(target.Connection.Host) == "" {
		return statementResponse{}, fmt.Errorf("databricks connection host is required")
	}
	if strings.TrimSpace(target.Warehouse.WarehouseID) == "" {
		return statementResponse{}, fmt.Errorf("databricks warehouse_id is required")
	}
	if strings.TrimSpace(target.Credential.BearerToken) == "" {
		return statementResponse{}, fmt.Errorf("databricks bearer token is required")
	}
	endpoint, err := c.statementExecutionURL(target.Connection.Host)
	if err != nil {
		return statementResponse{}, err
	}
	body := statementRequest{
		Statement:     sql,
		WarehouseID:   target.Warehouse.WarehouseID,
		WaitTimeout:   c.waitTimeout(),
		OnWaitTimeout: statementOnWaitTimeoutCancel,
		Disposition:   "INLINE",
		Format:        "JSON_ARRAY",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return statementResponse{}, fmt.Errorf("encode statement request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return statementResponse{}, fmt.Errorf("build statement request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+target.Credential.BearerToken)
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return statementResponse{}, fmt.Errorf("execute statement: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return statementResponse{}, statementHTTPError{
			statusCode: resp.StatusCode,
			status:     resp.Status,
			body:       strings.TrimSpace(string(body)),
		}
	}
	var out statementResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return statementResponse{}, fmt.Errorf("decode statement response: %w", err)
	}
	if state := strings.ToUpper(strings.TrimSpace(out.Status.State)); state != "" && state != statementStatusSucceeded {
		return statementResponse{}, statementStateError{state: state, message: out.Status.Error.Message}
	}
	return out, nil
}

func (c StatementClient) waitTimeout() string {
	if strings.TrimSpace(c.WaitTimeout) == "" {
		return defaultStatementWaitTimeout
	}
	return c.WaitTimeout
}

type statementRequest struct {
	Statement     string `json:"statement"`
	WarehouseID   string `json:"warehouse_id"`
	WaitTimeout   string `json:"wait_timeout"`
	OnWaitTimeout string `json:"on_wait_timeout,omitempty"`
	Disposition   string `json:"disposition"`
	Format        string `json:"format"`
}

type statementResponse struct {
	Status struct {
		State string `json:"state"`
		Error struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	} `json:"status"`
	Manifest struct {
		Schema struct {
			Columns []struct {
				Name     string `json:"name"`
				TypeName string `json:"type_name,omitempty"`
				TypeText string `json:"type_text,omitempty"`
			} `json:"columns"`
		} `json:"schema"`
		Truncated bool `json:"truncated,omitempty"`
	} `json:"manifest"`
	Result struct {
		DataArray [][]any `json:"data_array"`
		Truncated bool    `json:"truncated,omitempty"`
	} `json:"result"`
}

func statementExecutionURL(host string) (string, error) {
	return StatementClient{}.statementExecutionURL(host)
}

func (c StatementClient) statementExecutionURL(host string) (string, error) {
	u, err := c.workspaceURL(host)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/2.0/sql/statements"
	return u.String(), nil
}

func rowsFromStatement(resp statementResponse) []map[string]any {
	columns := make([]string, 0, len(resp.Manifest.Schema.Columns))
	for _, column := range resp.Manifest.Schema.Columns {
		columns = append(columns, column.Name)
	}
	rows := make([]map[string]any, 0, len(resp.Result.DataArray))
	for _, values := range resp.Result.DataArray {
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			if i < len(values) {
				row[column] = values[i]
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func queryResultFromStatement(resp statementResponse, tableRef string) queryapi.QueryTableResult {
	columns := make([]queryapi.QueryColumn, 0, len(resp.Manifest.Schema.Columns))
	for _, column := range resp.Manifest.Schema.Columns {
		typ := strings.TrimSpace(column.TypeText)
		if typ == "" {
			typ = strings.TrimSpace(column.TypeName)
		}
		columns = append(columns, queryapi.QueryColumn{Name: column.Name, Type: typ})
	}
	rows := make([]map[string]any, 0, len(resp.Result.DataArray))
	for _, values := range resp.Result.DataArray {
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			if i < len(values) {
				row[column.Name] = values[i]
			}
		}
		rows = append(rows, row)
	}
	return queryapi.QueryTableResult{
		ActionVersion: queryapi.ActionVersionV1,
		TableRef:      tableRef,
		Columns:       columns,
		Rows:          rows,
		Truncated:     resp.Manifest.Truncated || resp.Result.Truncated,
	}
}

func boundQueryResult(result queryapi.QueryTableResult) queryapi.QueryTableResult {
	return queryapi.BoundQueryResult(result)
}

type statementHTTPError struct {
	statusCode int
	status     string
	body       string
}

func (e statementHTTPError) Error() string {
	return statementHTTPFailureSafeMessage + ": " + e.status
}

func (e statementHTTPError) SafeStatusMessage() string {
	return statementHTTPFailureSafeMessage + ": " + e.status
}

func (e statementHTTPError) ActionFailureCode() string { return "backend_failure" }

func (e statementHTTPError) ActionFailureMessage() string { return e.SafeStatusMessage() }

func (e statementHTTPError) ActionFailureStatus() int {
	if e.statusCode == http.StatusTooManyRequests || e.statusCode >= http.StatusInternalServerError {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

func (e statementHTTPError) ActionFailureRetryable() bool {
	switch e.statusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return e.statusCode >= http.StatusInternalServerError && e.statusCode <= 599
	}
}

type statementStateError struct {
	state   string
	message string
}

func (e statementStateError) Error() string {
	if e.state == "" {
		return "databricks statement did not complete"
	}
	return "databricks statement did not complete: " + e.state
}

func (e statementStateError) SafeStatusMessage() string {
	if e.state == "" {
		return "databricks statement did not complete"
	}
	return "databricks statement did not complete: " + e.state
}

func (e statementStateError) ActionFailureCode() string    { return "backend_failure" }
func (e statementStateError) ActionFailureStatus() int     { return http.StatusBadGateway }
func (e statementStateError) ActionFailureRetryable() bool { return false }
func (e statementStateError) ActionFailureMessage() string { return e.SafeStatusMessage() }

// Stub is a local-development validator used only when DATABRICKS_DEV_STATIC_TABLES
// is explicitly enabled.
type Stub struct{}
