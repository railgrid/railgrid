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

func NewStatementClient(httpClient *http.Client) StatementClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return StatementClient{
		HTTPClient:  httpClient,
		WaitTimeout: defaultStatementWaitTimeout,
	}
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
			status: resp.Status,
			body:   strings.TrimSpace(string(body)),
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
				Name string `json:"name"`
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

type statementHTTPError struct {
	status string
	body   string
}

func (e statementHTTPError) Error() string {
	if e.body == "" {
		return statementHTTPFailureSafeMessage + ": " + e.status
	}
	return fmt.Sprintf("%s: %s: %s", statementHTTPFailureSafeMessage, e.status, e.body)
}

func (e statementHTTPError) SafeStatusMessage() string {
	return statementHTTPFailureSafeMessage + ": " + e.status
}

type statementStateError struct {
	state   string
	message string
}

func (e statementStateError) Error() string {
	if e.message == "" {
		return "databricks statement did not complete: " + e.state
	}
	return fmt.Sprintf("databricks statement %s: %s", e.state, e.message)
}

func (e statementStateError) SafeStatusMessage() string {
	if e.state == "" {
		return "databricks statement did not complete"
	}
	return "databricks statement did not complete: " + e.state
}

// Stub is a local-development validator used only when DATABRICKS_DEV_STATIC_TABLES
// is explicitly enabled.
type Stub struct{}
