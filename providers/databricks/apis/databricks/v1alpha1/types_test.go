// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import "testing"

func TestTableIsGovernedPointer(t *testing.T) {
	table := Table{
		Spec: TableSpec{
			ConnectionRef: "prod",
			WarehouseRef:  "main",
			Catalog:       "sales",
			Schema:        "gold",
			Table:         "order_history",
		},
	}

	if table.Spec.ConnectionRef == "" || table.Spec.WarehouseRef == "" {
		t.Fatal("table must reference connection and warehouse")
	}
	if table.Spec.Catalog == "" || table.Spec.Schema == "" || table.Spec.Table == "" {
		t.Fatal("table must point at a fully-qualified Databricks table")
	}
	if len(table.Status.Columns) != 0 {
		t.Fatal("status may cache schema, but table data must not live on the resource")
	}
}
