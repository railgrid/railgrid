// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func visualizationExec(t *testing.T) func(context.Context, string) (string, error) {
	t.Helper()
	tools := Visualization()
	if len(tools) != 1 {
		t.Fatalf("Visualization() returned %d tools, want one", len(tools))
	}
	if tools[0].Name != "visualize_data" || tools[0].Exec == nil || tools[0].JSONSchema == nil {
		t.Fatalf("visualization tool is missing its name, schema, or executor: %+v", tools[0])
	}
	return tools[0].Exec
}

func TestVisualizationReturnsFaithfulVersionedData(t *testing.T) {
	exec := visualizationExec(t)
	input := `{"title":"Monthly revenue","description":"Reported totals","kind":"line","data":[{"month":"2026-07-01","team":"west","revenue":1.25,"ratio":1.2300,"note":"<b>keep this as data</b>"},{"month":"2026-08-01","team":"east","revenue":1e0,"ratio":1.23,"note":"<img src=x onerror=alert(1)>"}],"x":"month","y":"revenue","series":"team","xType":"temporal","source":"observed report"}`
	got, err := exec(context.Background(), input)
	if err != nil {
		t.Fatalf("visualize_data: %v", err)
	}
	if !strings.Contains(got, `"revenue":1.25`) || !strings.Contains(got, `"revenue":1e0`) {
		t.Fatalf("numeric JSON lexemes were not preserved: %s", got)
	}
	if !strings.Contains(got, `"ratio":1.2300`) {
		t.Fatalf("equivalent trailing-zero decimal spelling should be preserved: %s", got)
	}
	if !strings.Contains(got, `<b>keep this as data</b>`) || !strings.Contains(got, `<img src=x onerror=alert(1)>`) {
		t.Fatalf("HTML-like values should remain ordinary data strings: %s", got)
	}
	var result struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		Chart   struct {
			Title  string           `json:"title"`
			Kind   string           `json:"kind"`
			X      string           `json:"x"`
			Y      string           `json:"y"`
			Series string           `json:"series"`
			XType  string           `json:"xType"`
			Source string           `json:"source"`
			Data   []map[string]any `json:"data"`
		} `json:"chart"`
	}
	decoder := json.NewDecoder(strings.NewReader(got))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Type != "railgrid.visualization" || result.Version != 1 {
		t.Fatalf("envelope = %q v%d, want railgrid.visualization v1", result.Type, result.Version)
	}
	if result.Chart.Title != "Monthly revenue" || result.Chart.Kind != "line" || result.Chart.X != "month" ||
		result.Chart.Y != "revenue" || result.Chart.Series != "team" || result.Chart.XType != "temporal" || result.Chart.Source != "observed report" {
		t.Fatalf("chart metadata was changed: %+v", result.Chart)
	}
	if len(result.Chart.Data) != 2 || result.Chart.Data[0]["team"] != "west" || result.Chart.Data[1]["team"] != "east" {
		t.Fatalf("multi-series rows were changed or omitted: %#v", result.Chart.Data)
	}
	if number, ok := result.Chart.Data[1]["revenue"].(json.Number); !ok || number.String() != "1e0" {
		t.Fatalf("second revenue = %#v, want exact JSON number 1e0", result.Chart.Data[1]["revenue"])
	}
}

func TestVisualizationValidatesChartRequests(t *testing.T) {
	exec := visualizationExec(t)
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "unknown top-level field",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":1}],"x":"category","y":"value","color":"red"}`,
			want: "unsupported chart field",
		},
		{
			name: "non-numeric y",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":"one"}],"x":"category","y":"value"}`,
			want: "finite JSON number",
		},
		{
			name: "nested row value",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":1,"extra":[]}],"x":"category","y":"value"}`,
			want: "must be a scalar",
		},
		{
			name: "non-finite number",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":1e9999}],"x":"category","y":"value"}`,
			want: "finite numeric range",
		},
		{
			name: "invalid non-JSON number",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":NaN}],"x":"category","y":"value"}`,
			want: "invalid chart JSON",
		},
		{
			name: "unsafe integer",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":1.5,"id":9007199254740993}],"x":"category","y":"value"}`,
			want: "safe numeric range",
		},
		{
			name: "number outside safe range",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":9007199254740992}],"x":"category","y":"value"}`,
			want: "safe numeric range",
		},
		{
			name: "nonzero number underflows",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":1e-9999}],"x":"category","y":"value"}`,
			want: "underflows to zero",
		},
		{
			name: "decimal loses precision",
			json: `{"title":"x","kind":"bar","data":[{"category":"a","value":0.1234567890123456789}],"x":"category","y":"value"}`,
			want: "loses decimal precision",
		},
		{
			name: "inconsistent x types",
			json: `{"title":"x","kind":"line","data":[{"x":"a","y":1},{"x":2,"y":3}],"x":"x","y":"y"}`,
			want: "inconsistent value types",
		},
		{
			name: "invalid temporal x",
			json: `{"title":"x","kind":"line","data":[{"x":"next week","y":1}],"x":"x","y":"y","xType":"temporal"}`,
			want: "ISO date or RFC3339",
		},
		{
			name: "missing x value",
			json: `{"title":"x","kind":"bar","data":[{"value":1}],"x":"category","y":"value"}`,
			want: "missing x field",
		},
		{
			name: "unsafe Vega field path",
			json: `{"title":"x","kind":"bar","data":[{"cost.value":"a","value":1}],"x":"cost.value","y":"value"}`,
			want: "flat field name",
		},
		{
			name: "reserved JavaScript field",
			json: `{"title":"x","kind":"bar","data":[{"__proto__":"a","value":1}],"x":"__proto__","y":"value"}`,
			want: "reserved",
		},
		{
			name: "duplicate row key",
			json: `{"title":"x","kind":"bar","data":[{"x":"a","x":"b","y":1}],"x":"x","y":"y"}`,
			want: "duplicate object key",
		},
		{
			name: "scatter needs quantitative x",
			json: `{"title":"x","kind":"scatter","data":[{"x":"a","y":1}],"x":"x","y":"y"}`,
			want: "scatter charts require quantitative",
		},
		{
			name: "pie rejects negative values",
			json: `{"title":"x","kind":"pie","data":[{"x":"a","y":-1}],"x":"x","y":"y"}`,
			want: "must not contain negative",
		},
		{
			name: "pie rejects all-zero values",
			json: `{"title":"x","kind":"pie","data":[{"x":"a","y":0}],"x":"x","y":"y"}`,
			want: "positive total",
		},
		{
			name: "pie rejects series",
			json: `{"title":"x","kind":"pie","data":[{"x":"a","y":1,"group":"g"}],"x":"x","y":"y","series":"group"}`,
			want: "do not support a series",
		},
		{
			name: "pie rejects quantitative x",
			json: `{"title":"x","kind":"pie","data":[{"x":1,"y":2}],"x":"x","y":"y","xType":"quantitative"}`,
			want: "pie charts require nominal",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exec(context.Background(), test.json); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want message containing %q", err, test.want)
			}
		})
	}
}

func TestVisualizationPieUsesNominalNumericCategories(t *testing.T) {
	exec := visualizationExec(t)
	got, err := exec(context.Background(), `{"title":"x","kind":"pie","data":[{"x":1,"y":2}],"x":"x","y":"y"}`)
	if err != nil {
		t.Fatalf("numeric pie category: %v", err)
	}
	if !strings.Contains(got, `"xType":"nominal"`) {
		t.Fatalf("numeric pie category should default to nominal: %s", got)
	}
}

func TestFiniteNumberAcceptsEquivalentDecimalSpellings(t *testing.T) {
	for _, value := range []string{"1e-3", "1.2e-7", "0.00000012", "1e-100", "1.2300", "-1e-3"} {
		t.Run(value, func(t *testing.T) {
			if _, err := finiteNumber(json.Number(value)); err != nil {
				t.Fatalf("finiteNumber(%q): %v", value, err)
			}
		})
	}
}

func TestVisualizationEnforcesInputAndRowLimits(t *testing.T) {
	exec := visualizationExec(t)
	if _, err := exec(context.Background(), strings.Repeat(" ", visualizationMaxInputBytes+1)); err == nil || !strings.Contains(err.Error(), "input limit") {
		t.Fatalf("oversized input error = %v, want input limit", err)
	}

	var rows strings.Builder
	rows.WriteString(`{"title":"x","kind":"bar","data":[`)
	for i := 0; i <= visualizationMaxRows; i++ {
		if i > 0 {
			rows.WriteByte(',')
		}
		rows.WriteString(`{"x":"a","y":1}`)
	}
	rows.WriteString(`],"x":"x","y":"y"}`)
	if _, err := exec(context.Background(), rows.String()); err == nil || !strings.Contains(err.Error(), "maximum is 1000") {
		t.Fatalf("row limit error = %v, want maximum row count", err)
	}

	tooManyFields := `{"title":"x","kind":"bar","data":[{"x":"a","y":1,"c":1,"d":1,"e":1,"f":1,"g":1,"h":1,"i":1,"j":1,"k":1,"l":1,"m":1,"n":1,"o":1,"p":1,"q":1}],"x":"x","y":"y"}`
	if _, err := exec(context.Background(), tooManyFields); err == nil || !strings.Contains(err.Error(), "maximum is 16") {
		t.Fatalf("field limit error = %v, want maximum field count", err)
	}
}

func TestVisualizationRejectsExpandedResultOverLimit(t *testing.T) {
	exec := visualizationExec(t)
	value := strings.Repeat("\u2028", 150)
	var input strings.Builder
	input.WriteString(`{"title":"x","kind":"bar","data":[`)
	for i := range 800 {
		if i > 0 {
			input.WriteByte(',')
		}
		input.WriteString(`{"x":"`)
		input.WriteString(value)
		input.WriteString(`","y":1}`)
	}
	input.WriteString(`],"x":"x","y":"y"}`)
	if input.Len() >= visualizationMaxInputBytes {
		t.Fatalf("test input is %d bytes, want below the input cap", input.Len())
	}
	if _, err := exec(context.Background(), input.String()); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("expanded output error = %v, want explicit output limit", err)
	}
}

func TestVisualizationInputSchemaIsClosed(t *testing.T) {
	schema := visualizationInputSchema()
	if schema["additionalProperties"] != false {
		t.Fatalf("top-level additionalProperties = %#v, want false", schema["additionalProperties"])
	}
	if !strings.Contains(fmt.Sprint(schema["required"]), "title") || !strings.Contains(fmt.Sprint(schema["required"]), "data") {
		t.Fatalf("required fields missing from schema: %#v", schema["required"])
	}
}

func TestVisualizationTimeMatchesBrowserContract(t *testing.T) {
	for _, value := range []string{"2026-01-01T1:02:03Z", "2026-01-01T01:02:03+24:00", "2026-01-01T01:02:03+01:60", "2026-01-01T01:02:03,123Z"} {
		if validChartTime(value) {
			t.Errorf("accepted non-browser timestamp %s", value)
		}
	}
	for _, value := range []string{"2026-01-01", "2026-01-01T01:02:03.123Z", "2026-01-01T01:02:03+05:30"} {
		if !validChartTime(value) {
			t.Errorf("rejected valid timestamp %s", value)
		}
	}
}
