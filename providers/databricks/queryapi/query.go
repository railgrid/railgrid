// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package queryapi exposes provider-owned Databricks table metadata helpers.
package queryapi

import (
	"fmt"
	"regexp"
	"strings"
)

var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type TableRef struct {
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`
	Table   string `json:"table"`
}

type ConnectionRef struct {
	Name     string `json:"name,omitempty"`
	Host     string `json:"host"`
	AuthType string `json:"authType"`
}

type WarehouseRef struct {
	Name        string `json:"name,omitempty"`
	WarehouseID string `json:"warehouseID"`
}

type Credential struct {
	BearerToken string `json:"-"`
}

type TableTarget struct {
	Table      TableRef      `json:"table"`
	Connection ConnectionRef `json:"connection"`
	Warehouse  WarehouseRef  `json:"warehouse"`
	Credential Credential    `json:"-"`
}

func DescribeTableSQL(ref TableRef) (string, error) {
	from, err := qualifiedTable(ref)
	if err != nil {
		return "", err
	}
	return "DESCRIBE TABLE " + from, nil
}

func qualifiedTable(ref TableRef) (string, error) {
	catalog, err := quoteIdent(ref.Catalog)
	if err != nil {
		return "", fmt.Errorf("catalog %q: %w", ref.Catalog, err)
	}
	schema, err := quoteIdent(ref.Schema)
	if err != nil {
		return "", fmt.Errorf("schema %q: %w", ref.Schema, err)
	}
	table, err := quoteIdent(ref.Table)
	if err != nil {
		return "", fmt.Errorf("table %q: %w", ref.Table, err)
	}
	return catalog + "." + schema + "." + table, nil
}

func quoteIdent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !identifierRE.MatchString(value) {
		return "", fmt.Errorf("invalid identifier")
	}
	return "`" + value + "`", nil
}
