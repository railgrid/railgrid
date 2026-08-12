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
	Tables []tableSummary `json:"tables"`
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

func registerTools(srv *mcp.Server, resolver queryapi.TableResolver, executor actions.QueryExecutor) {
	safeRegister("list_tables", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "list_tables",
			Title:       "List imported Databricks tables",
			Description: "List Databricks tables already imported into this faros workspace. The returned tables[].name is the exact faros Table resource name to copy as tableRef; never substitute an App Studio integration alias or another binding identifier.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listTablesOutput, error) {
			tables, err := resolver.ListTables(ctx)
			if err != nil {
				return nil, listTablesOutput{}, err
			}
			out := make([]tableSummary, 0, len(tables))
			for name, ref := range tables {
				out = append(out, tableSummary{Name: name, Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return nil, listTablesOutput{Tables: out}, nil
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
			result = queryapi.BoundQueryResult(result)
			return nil, queryTableOutput{
				ActionVersion: queryapi.ActionVersionV1,
				TableRef:      request.TableRef,
				Columns:       result.Columns,
				Rows:          result.Rows,
				Truncated:     result.Truncated,
			}, nil
		})
	})
}
