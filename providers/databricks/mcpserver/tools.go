// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-databricks/actions"
	"github.com/faroshq/provider-databricks/queryapi"
)

type tableSummary struct {
	Name    string `json:"name"`
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	// Columns is the controller-cached schema from the Table resource status.
	// Empty when the schema has not been refreshed yet or the resolver cannot
	// read status.
	Columns []queryapi.QueryColumn `json:"columns,omitempty"`
	// SchemaRefreshedAt is when the cached schema was last refreshed.
	SchemaRefreshedAt string `json:"schemaRefreshedAt,omitempty"`
}

// tableSchemaReader is optionally implemented by resolvers that can read the
// controller-cached column schema off the Table resource status. The tenant
// resolver implements it; static test resolvers need not.
type tableSchemaReader interface {
	GetTableSchema(ctx context.Context, name string) ([]queryapi.QueryColumn, string, error)
}

type listTablesOutput struct {
	Tables    []tableSummary `json:"tables"`
	Truncated bool           `json:"truncated,omitempty"`
}

type describeTableInput struct {
	TableRef string `json:"tableRef" jsonschema:"Exact imported faros Table resource name (the name returned by list_tables or the grant), e.g. order-history; never an App Studio integration alias"`
}

type queryTableInput struct {
	ActionVersion string   `json:"actionVersion" jsonschema:"Pinned provider action contract version; currently v1"`
	TableRef      string   `json:"tableRef" jsonschema:"Exact imported faros Table resource name (the name returned by list_tables or the grant), e.g. order-history; never an App Studio integration alias"`
	Columns       []string `json:"columns,omitempty" jsonschema:"Optional exact column-name projection; SQL expressions are not accepted"`
	Limit         int      `json:"limit,omitempty" jsonschema:"Maximum 100 rows; defaults to 100"`
}

type queryTableOutput struct {
	ActionVersion string                 `json:"actionVersion"`
	TableRef      string                 `json:"tableRef"`
	Columns       []queryapi.QueryColumn `json:"columns"`
	Rows          []map[string]any       `json:"rows"`
	Truncated     bool                   `json:"truncated,omitempty"`
}

// The SDK sends structuredContent and, when the handler does not provide
// content itself, a text block containing the same JSON. The response also
// echoes the accepted request ID, so reserve the complete request cap plus a
// conservative amount for the fixed JSON-RPC/SSE framing.
const (
	mcpResponseFramingReserveBytes = 4 * 1024
	mcpEnvelopeReserveBytes        = maxMCPRequestBytes + mcpResponseFramingReserveBytes
)

type mcpTextContentWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResultWire struct {
	Content           []mcpTextContentWire `json:"content"`
	StructuredContent json.RawMessage      `json:"structuredContent,omitempty"`
}

// mcpQueryEnvelopeBytes models the wire shape emitted by ToolHandlerFor. In
// particular, marshaling Text a second time accounts for quotes, backslashes,
// and control characters being escaped again by the SDK. It returns the
// measured tool-result bytes plus a fixed reserve for the surrounding
// JSON-RPC response and transport framing.
func mcpToolEnvelopeBytes(output any) (int, bool) {
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return 0, false
	}
	envelope, err := json.Marshal(mcpToolResultWire{
		Content: []mcpTextContentWire{{Type: "text", Text: string(outputJSON)}},
		// The SDK stores the first marshal as a RawMessage, avoiding another
		// layer of escaping in structuredContent.
		StructuredContent: json.RawMessage(outputJSON),
	})
	if err != nil {
		return 0, false
	}
	return len(envelope) + mcpEnvelopeReserveBytes, true
}

func mcpQueryEnvelopeBytes(output queryTableOutput) (int, bool) {
	return mcpToolEnvelopeBytes(output)
}

func mcpListEnvelopeBytes(output listTablesOutput) (int, bool) {
	return mcpToolEnvelopeBytes(output)
}

func boundMCPQueryOutput(tableRef string, result queryapi.QueryTableResult) queryTableOutput {
	// Normalize shape and apply the ordinary result cap first. The loop below
	// only removes complete rows or columns; retained cell values are never
	// shortened or rewritten.
	result = queryapi.BoundQueryResult(result)
	for {
		output := queryTableOutput{
			ActionVersion: queryapi.ActionVersionV1,
			TableRef:      tableRef,
			Columns:       result.Columns,
			Rows:          result.Rows,
			Truncated:     result.Truncated,
		}
		if size, ok := mcpQueryEnvelopeBytes(output); ok && size <= queryapi.MaxQueryBytes {
			return output
		}
		if len(result.Rows) > 0 {
			result.Rows = result.Rows[:len(result.Rows)-1]
			result.Truncated = true
			continue
		}
		if len(result.Columns) > 0 {
			result.Columns = result.Columns[:len(result.Columns)-1]
			result = queryapi.BoundQueryResult(result)
			result.Truncated = true
			continue
		}
		// The request contract bounds tableRef and the action-version metadata,
		// so this fallback is only defensive against a future wire-shape change.
		return queryTableOutput{
			ActionVersion: queryapi.ActionVersionV1,
			TableRef:      tableRef,
			Columns:       []queryapi.QueryColumn{},
			Rows:          []map[string]any{},
			Truncated:     true,
		}
	}
}

func boundMCPListOutput(tables []tableSummary, initialTruncated ...bool) listTablesOutput {
	// Sorting before applying the byte budget makes truncation deterministic
	// even though the resolver's KCP item map has no iteration order.
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	truncated := len(initialTruncated) > 0 && initialTruncated[0]
	result := listTablesOutput{Tables: append([]tableSummary(nil), tables...), Truncated: truncated}
	for {
		if size, ok := mcpListEnvelopeBytes(result); ok && size <= queryapi.MaxQueryBytes {
			return result
		}
		if len(result.Tables) == 0 {
			return listTablesOutput{Tables: []tableSummary{}, Truncated: true}
		}
		result.Tables = result.Tables[:len(result.Tables)-1]
		result.Truncated = true
	}
}

func boundedTableMap(tables map[string]queryapi.TableRef, limit int) (map[string]queryapi.TableRef, bool) {
	if limit < 1 {
		limit = queryapi.MaxTableListItems
	}
	if len(tables) <= limit {
		return tables, false
	}
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	bounded := make(map[string]queryapi.TableRef, limit)
	for _, name := range names[:limit] {
		bounded[name] = tables[name]
	}
	return bounded, true
}

func registerTools(srv *mcp.Server, resolver queryapi.TableResolver, executor actions.QueryExecutor) {
	safeRegister("list_tables", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "list_tables",
			Title:       "List imported Databricks tables",
			Description: "List Databricks tables already imported into this faros workspace. The returned tables[].name is the exact faros Table resource name to copy as tableRef; never substitute an App Studio integration alias or another binding identifier.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listTablesOutput, error) {
			var (
				tables    map[string]queryapi.TableRef
				truncated bool
				err       error
			)
			if boundedResolver, ok := resolver.(queryapi.BoundedTableResolver); ok {
				tables, truncated, err = boundedResolver.ListTablesBounded(ctx, queryapi.MaxTableListItems)
			} else {
				tables, err = resolver.ListTables(ctx)
			}
			if err != nil {
				return nil, listTablesOutput{}, err
			}
			bounded, itemTruncated := boundedTableMap(tables, queryapi.MaxTableListItems)
			truncated = truncated || itemTruncated
			out := make([]tableSummary, 0, len(bounded))
			for name, ref := range bounded {
				out = append(out, tableSummary{Name: name, Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table})
			}
			return nil, boundMCPListOutput(out, truncated), nil
		})
	})

	safeRegister("describe_table", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "describe_table",
			Title:       "Describe an imported Databricks table",
			Description: "Describe one imported faros Databricks Table resource by its exact tableRef (the name returned by list_tables or the project grant). Returns the cached column schema (columns[].name/type) — use exactly these names for query_table projections; requesting a column not in this list fails the query, and an App Studio integration alias is never a tableRef.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in describeTableInput) (*mcp.CallToolResult, tableSummary, error) {
			ref, ok, err := resolver.GetTable(ctx, in.TableRef)
			if err != nil {
				return nil, tableSummary{}, err
			}
			if !ok {
				return nil, tableSummary{}, fmt.Errorf("tableRef %q not found", in.TableRef)
			}
			summary := tableSummary{Name: in.TableRef, Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table}
			if schemaReader, ok := resolver.(tableSchemaReader); ok {
				// Schema is additive evidence: a status read failure must not
				// hide the table identity that GetTable already proved.
				if columns, refreshedAt, err := schemaReader.GetTableSchema(ctx, in.TableRef); err == nil {
					summary.Columns = columns
					summary.SchemaRefreshedAt = refreshedAt
				}
			}
			return nil, summary, nil
		})
	})

	safeRegister("query_table", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "query_table",
			Title:       "Query an imported Databricks table",
			Description: "Run the versioned v1 bounded table query action. Supply only the exact imported Table resource name as tableRef (copied from list_tables or the project grant), optional exact column names, and a limit; never send an App Studio integration alias or binding alias. The provider resolves the warehouse, connection, and credentials without creating a query resource.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryTableInput) (*mcp.CallToolResult, queryTableOutput, error) {
			request, err := queryapi.NormalizeQueryRequest(queryapi.QueryTableRequest{
				ActionVersion: in.ActionVersion,
				TableRef:      in.TableRef,
				Columns:       in.Columns,
				Limit:         in.Limit,
			})
			if err != nil {
				return nil, queryTableOutput{}, err
			}
			if executor == nil {
				return nil, queryTableOutput{}, fmt.Errorf("databricks table query is unavailable")
			}
			result, err := executor.QueryTable(ctx, actions.ResourceRef{
				APIVersion: "databricks.faros.sh/v1alpha1",
				Kind:       "Table",
				Resource:   "tables",
				Name:       request.TableRef,
			}, actions.QueryInput{Columns: request.Columns, Limit: request.Limit})
			if err != nil {
				return nil, queryTableOutput{}, err
			}
			return nil, boundMCPQueryOutput(request.TableRef, result), nil
		})
	})
}
