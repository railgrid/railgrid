// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/railgrid/provider-agents/engine"
)

const (
	visualizationMaxInputBytes  = 512 * 1024
	visualizationMaxResultBytes = 525312
	visualizationMaxRows        = 1000
	visualizationMaxFields      = 16
	visualizationMaxTitle       = 160
	visualizationMaxDesc        = 1000
	visualizationMaxSource      = 512
	visualizationMaxFieldName   = 128
	visualizationMaxCellString  = 4096
	visualizationMaxSafeInt     = int64(1<<53 - 1)
)

// Visualization returns the pure-data chart family. The frontend owns the
// visualization grammar; this tool accepts tabular data and chart choices,
// validates them, then returns a versioned envelope for the renderer.
func Visualization() []engine.Tool {
	return []engine.Tool{{
		Name:       "visualize_data",
		Desc:       "Build a chart from rows already provided by the user or returned by another tool. Never invent values or silently omit rows or columns. Aggregate data upstream before calling this tool, and include its source. Every row needs the selected x and finite numeric y values in the JavaScript safe range (absolute value at most 9,007,199,254,740,991); numbers that would lose decimal precision or underflow to zero are rejected. Temporal x values must be ISO dates or RFC3339 timestamps. Pie charts need nonnegative y values with a positive total and do not support a series field. The result is JSON shaped as {type: \"railgrid.visualization\", version: 1, chart: ...}, up to 525,312 bytes; the UI renders the data. This tool does not access the network or files.",
		JSONSchema: visualizationInputSchema(),
		Exec:       visualizeData,
	}}
}

type visualizationChart struct {
	Title       string           `json:"title"`
	Description *string          `json:"description,omitempty"`
	Kind        string           `json:"kind"`
	Data        []map[string]any `json:"data"`
	X           string           `json:"x"`
	Y           string           `json:"y"`
	Series      *string          `json:"series,omitempty"`
	XType       string           `json:"xType"`
	Source      *string          `json:"source,omitempty"`
}

type visualizationEnvelope struct {
	Type    string             `json:"type"`
	Version int                `json:"version"`
	Chart   visualizationChart `json:"chart"`
}

func visualizationInputSchema() map[string]any {
	stringSchema := func(maxLength int) map[string]any {
		return map[string]any{"type": "string", "maxLength": maxLength}
	}
	cellSchema := map[string]any{
		"oneOf": []any{
			stringSchema(visualizationMaxCellString),
			map[string]any{"type": "number", "description": "finite JavaScript safe-range number; nonzero values that underflow to zero are rejected"},
			map[string]any{"type": "boolean"},
			map[string]any{"type": "null"},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"title", "kind", "data", "x", "y"},
		"properties": map[string]any{
			"title":       map[string]any{"type": "string", "maxLength": visualizationMaxTitle, "description": "short chart title"},
			"description": map[string]any{"type": "string", "maxLength": visualizationMaxDesc, "description": "optional context shown with the chart"},
			"kind":        map[string]any{"type": "string", "enum": []any{"bar", "line", "area", "scatter", "pie"}, "description": "chart form; pie uses nonnegative values with a positive total and cannot have a series field"},
			"data": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    visualizationMaxRows,
				"description": "complete source rows, with scalar values only; no rows or fields are dropped or aggregated",
				"items": map[string]any{
					"type":                 "object",
					"minProperties":        1,
					"maxProperties":        visualizationMaxFields,
					"additionalProperties": cellSchema,
				},
			},
			"x":      map[string]any{"type": "string", "maxLength": visualizationMaxFieldName, "description": "field name for the horizontal/category axis; every row must have a non-null value of one consistent type"},
			"y":      map[string]any{"type": "string", "maxLength": visualizationMaxFieldName, "description": "field name for the vertical/value axis; every row must have a finite numeric value"},
			"series": map[string]any{"type": "string", "maxLength": visualizationMaxFieldName, "description": "optional field that splits a chart into series; every row must have a non-null value of one consistent type"},
			"xType":  map[string]any{"type": "string", "enum": []any{"nominal", "temporal", "quantitative"}, "description": "x axis type; defaults to nominal for pie charts, quantitative for other numeric x values, and nominal otherwise"},
			"source": map[string]any{"type": "string", "maxLength": visualizationMaxSource, "description": "where the supplied rows came from"},
		},
	}
}

func visualizeData(_ context.Context, argsJSON string) (string, error) {
	chart, err := parseVisualizationChart(argsJSON)
	if err != nil {
		return "", err
	}
	result := visualizationEnvelope{Type: "railgrid.visualization", Version: 1, Chart: chart}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return "", fmt.Errorf("encode visualization: %w", err)
	}
	encoded := strings.TrimSuffix(out.String(), "\n")
	if len(encoded) > visualizationMaxResultBytes {
		return "", fmt.Errorf("chart result exceeds the %d-byte output limit", visualizationMaxResultBytes)
	}
	return encoded, nil
}

func parseVisualizationChart(raw string) (visualizationChart, error) {
	if len(raw) > visualizationMaxInputBytes {
		return visualizationChart{}, fmt.Errorf("chart request exceeds the %d-byte input limit", visualizationMaxInputBytes)
	}
	if !utf8.ValidString(raw) {
		return visualizationChart{}, fmt.Errorf("chart request must be valid UTF-8 JSON")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return visualizationChart{}, fmt.Errorf("invalid chart JSON: %w", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return visualizationChart{}, fmt.Errorf("invalid chart JSON: %w", err)
	}
	if fields == nil {
		return visualizationChart{}, fmt.Errorf("chart request must be a JSON object")
	}
	allowed := map[string]bool{
		"title": true, "description": true, "kind": true, "data": true,
		"x": true, "y": true, "series": true, "xType": true, "source": true,
	}
	for name := range fields {
		if !allowed[name] {
			return visualizationChart{}, fmt.Errorf("unsupported chart field %q", name)
		}
	}

	title, err := requiredString(fields, "title", visualizationMaxTitle, true)
	if err != nil {
		return visualizationChart{}, err
	}
	kind, err := requiredString(fields, "kind", 16, true)
	if err != nil {
		return visualizationChart{}, err
	}
	switch kind {
	case "bar", "line", "area", "scatter", "pie":
	default:
		return visualizationChart{}, fmt.Errorf("unsupported chart kind %q; expected bar, line, area, scatter, or pie", kind)
	}
	x, err := requiredFieldName(fields, "x")
	if err != nil {
		return visualizationChart{}, err
	}
	y, err := requiredFieldName(fields, "y")
	if err != nil {
		return visualizationChart{}, err
	}

	chart := visualizationChart{Title: title, Kind: kind, X: x, Y: y}
	if value, present, err := optionalString(fields, "description", visualizationMaxDesc); err != nil {
		return visualizationChart{}, err
	} else if present {
		chart.Description = &value
	}
	if value, present, err := optionalString(fields, "source", visualizationMaxSource); err != nil {
		return visualizationChart{}, err
	} else if present {
		chart.Source = &value
	}
	if value, present, err := optionalString(fields, "series", visualizationMaxFieldName); err != nil {
		return visualizationChart{}, err
	} else if present {
		if err := validateFieldName("series", value); err != nil {
			return visualizationChart{}, err
		}
		chart.Series = &value
	}

	declaredXType, hasXType, err := optionalString(fields, "xType", 32)
	if err != nil {
		return visualizationChart{}, err
	}
	if hasXType && declaredXType != "nominal" && declaredXType != "temporal" && declaredXType != "quantitative" {
		return visualizationChart{}, fmt.Errorf("unsupported xType %q; expected nominal, temporal, or quantitative", declaredXType)
	}

	dataRaw, ok := fields["data"]
	if !ok {
		return visualizationChart{}, fmt.Errorf("data is required")
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(dataRaw, &rows); err != nil {
		return visualizationChart{}, fmt.Errorf("data must be an array of row objects: %w", err)
	}
	if len(rows) == 0 {
		return visualizationChart{}, fmt.Errorf("data must contain at least one row")
	}
	if len(rows) > visualizationMaxRows {
		return visualizationChart{}, fmt.Errorf("data has %d rows; the maximum is %d", len(rows), visualizationMaxRows)
	}

	chart.Data = make([]map[string]any, 0, len(rows))
	fieldSet := make(map[string]struct{}, visualizationMaxFields)
	var xType string
	var xValueKind string
	var seriesKind string
	for rowIndex, rawRow := range rows {
		if rawRow == nil {
			return visualizationChart{}, fmt.Errorf("data row %d must be an object", rowIndex+1)
		}
		if len(rawRow) == 0 {
			return visualizationChart{}, fmt.Errorf("data row %d must contain at least one field", rowIndex+1)
		}
		if len(rawRow) > visualizationMaxFields {
			return visualizationChart{}, fmt.Errorf("data row %d has %d fields; the maximum is %d", rowIndex+1, len(rawRow), visualizationMaxFields)
		}
		row := make(map[string]any, len(rawRow))
		for name, rawValue := range rawRow {
			if err := validateFieldName("data", name); err != nil {
				return visualizationChart{}, fmt.Errorf("data row %d: %w", rowIndex+1, err)
			}
			fieldSet[name] = struct{}{}
			if len(fieldSet) > visualizationMaxFields {
				return visualizationChart{}, fmt.Errorf("data has more than %d distinct fields", visualizationMaxFields)
			}
			value, err := parseCellValue(rawValue)
			if err != nil {
				return visualizationChart{}, fmt.Errorf("data row %d field %q: %w", rowIndex+1, name, err)
			}
			row[name] = value
		}
		chart.Data = append(chart.Data, row)

		xValue, ok := row[x]
		if !ok {
			return visualizationChart{}, fmt.Errorf("data row %d is missing x field %q", rowIndex+1, x)
		}
		currentKind := scalarKind(xValue)
		if currentKind == "null" {
			return visualizationChart{}, fmt.Errorf("data row %d x field %q must not be null", rowIndex+1, x)
		}
		if xValueKind == "" {
			xValueKind = currentKind
		} else if currentKind != xValueKind {
			return visualizationChart{}, fmt.Errorf("x field %q has inconsistent value types: %s and %s", x, xValueKind, currentKind)
		}

		yValue, ok := row[y]
		if !ok {
			return visualizationChart{}, fmt.Errorf("data row %d is missing y field %q", rowIndex+1, y)
		}
		if _, ok := yValue.(json.Number); !ok {
			return visualizationChart{}, fmt.Errorf("data row %d y field %q must be a finite JSON number", rowIndex+1, y)
		}

		if chart.Series != nil {
			seriesValue, ok := row[*chart.Series]
			if !ok {
				return visualizationChart{}, fmt.Errorf("data row %d is missing series field %q", rowIndex+1, *chart.Series)
			}
			currentSeriesKind := scalarKind(seriesValue)
			if currentSeriesKind == "null" {
				return visualizationChart{}, fmt.Errorf("data row %d series field %q must not be null", rowIndex+1, *chart.Series)
			}
			if seriesKind == "" {
				seriesKind = currentSeriesKind
			} else if currentSeriesKind != seriesKind {
				return visualizationChart{}, fmt.Errorf("series field %q has inconsistent value types: %s and %s", *chart.Series, seriesKind, currentSeriesKind)
			}
		}
	}

	if hasXType {
		xType = declaredXType
	} else if kind == "pie" {
		xType = "nominal"
	} else if xValueKind == "number" {
		xType = "quantitative"
	} else {
		xType = "nominal"
	}
	if err := validateXType(x, xType, xValueKind, chart.Data); err != nil {
		return visualizationChart{}, err
	}
	if kind == "scatter" && xType != "quantitative" {
		return visualizationChart{}, fmt.Errorf("scatter charts require quantitative x values")
	}
	if kind == "pie" {
		if xType != "nominal" {
			return visualizationChart{}, fmt.Errorf("pie charts require nominal x values")
		}
		if chart.Series != nil {
			return visualizationChart{}, fmt.Errorf("pie charts do not support a series field")
		}
		total := float64(0)
		for rowIndex, row := range chart.Data {
			value, err := finiteNumber(row[y].(json.Number))
			if err != nil {
				return visualizationChart{}, fmt.Errorf("data row %d y field %q: %w", rowIndex+1, y, err)
			}
			if value < 0 {
				return visualizationChart{}, fmt.Errorf("pie chart y field %q must not contain negative values", y)
			}
			total += value
			if math.IsInf(total, 0) || math.IsNaN(total) {
				return visualizationChart{}, fmt.Errorf("pie chart y field %q has a total outside the finite numeric range", y)
			}
		}
		if total <= 0 {
			return visualizationChart{}, fmt.Errorf("pie chart y field %q must have a positive total", y)
		}
	}
	chart.XType = xType
	return chart, nil
}

func requiredString(fields map[string]json.RawMessage, name string, maxLength int, nonEmpty bool) (string, error) {
	value, present, err := optionalString(fields, name, maxLength)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("%s is required", name)
	}
	if nonEmpty && strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return value, nil
}

func requiredFieldName(fields map[string]json.RawMessage, name string) (string, error) {
	value, err := requiredString(fields, name, visualizationMaxFieldName, true)
	if err != nil {
		return "", err
	}
	if err := validateFieldName(name, value); err != nil {
		return "", err
	}
	return value, nil
}

func optionalString(fields map[string]json.RawMessage, name string, maxLength int) (string, bool, error) {
	raw, present := fields[name]
	if !present {
		return "", false, nil
	}
	trimmed := bytes.TrimSpace(raw)
	var value string
	if len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(trimmed, &value) != nil {
		return "", true, fmt.Errorf("%s must be a string", name)
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", true, fmt.Errorf("%s exceeds the %d-character limit", name, maxLength)
	}
	return value, true, nil
}

func validateFieldName(source, name string) error {
	if name == "" {
		return fmt.Errorf("%s field name must not be empty", source)
	}
	switch name {
	case "__proto__", "constructor", "prototype":
		return fmt.Errorf("%s field name %q is reserved", source, name)
	}
	if utf8.RuneCountInString(name) > visualizationMaxFieldName {
		return fmt.Errorf("%s field name exceeds the %d-character limit", source, visualizationMaxFieldName)
	}
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(".[]\\", r) {
			return fmt.Errorf("%s field name %q must be a flat field name without dots, brackets, backslashes, or control characters", source, name)
		}
	}
	return nil
}

func parseCellValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("must be a JSON scalar: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("must be a single JSON scalar")
	}
	switch v := value.(type) {
	case nil, bool:
		return value, nil
	case string:
		if utf8.RuneCountInString(v) > visualizationMaxCellString {
			return nil, fmt.Errorf("string exceeds the %d-character limit", visualizationMaxCellString)
		}
		return value, nil
	case json.Number:
		if _, err := finiteNumber(v); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, fmt.Errorf("must be a scalar string, number, boolean, or null")
	}
}

func finiteNumber(value json.Number) (float64, error) {
	lexeme := value.String()
	n, err := strconv.ParseFloat(lexeme, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("number %q is outside the finite numeric range", value)
	}
	if math.Abs(n) > float64(visualizationMaxSafeInt) {
		return 0, fmt.Errorf("number %q exceeds the JavaScript safe numeric range; represent identifiers as strings", value)
	}
	if n == 0 && nonzeroJSONMantissa(lexeme) {
		return 0, fmt.Errorf("nonzero number %q underflows to zero in JavaScript numeric precision", value)
	}
	shortest := strconv.FormatFloat(n, 'g', -1, 64)
	if !sameDecimalValue(lexeme, shortest) {
		return 0, fmt.Errorf("number %q loses decimal precision as a JavaScript number; round it upstream to a chartable value", value)
	}
	return n, nil
}

type normalizedDecimal struct {
	sign   int
	digits string
	power  int64
}

// sameDecimalValue compares JSON number lexemes by their normalized base-10
// significand and power. It avoids constructing large rational denominators
// for inputs near the request-size limit.
func sameDecimalValue(left, right string) bool {
	a, ok := normalizeDecimal(left)
	if !ok {
		return false
	}
	b, ok := normalizeDecimal(right)
	return ok && a == b
}

func normalizeDecimal(value string) (normalizedDecimal, bool) {
	sign := 1
	if strings.HasPrefix(value, "-") {
		sign = -1
		value = value[1:]
	}
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(value), "e")
	exponent := int64(0)
	if hasExponent {
		parsed, err := strconv.ParseInt(exponentText, 10, 64)
		if err != nil {
			return normalizedDecimal{}, false
		}
		exponent = parsed
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return normalizedDecimal{digits: "0"}, true
	}
	fractionLength := int64(len(fraction))
	if exponent < math.MinInt64+fractionLength {
		return normalizedDecimal{}, false
	}
	power := exponent - fractionLength
	trimmed := strings.TrimRight(digits, "0")
	trailingZeros := int64(len(digits) - len(trimmed))
	if power > math.MaxInt64-trailingZeros {
		return normalizedDecimal{}, false
	}
	power += trailingZeros
	return normalizedDecimal{sign: sign, digits: trimmed, power: power}, true
}

func nonzeroJSONMantissa(value string) bool {
	mantissa, _, _ := strings.Cut(strings.ToLower(value), "e")
	for _, r := range mantissa {
		if r >= '1' && r <= '9' {
			return true
		}
	}
	return false
}

func scalarKind(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	default:
		return "null"
	}
}

func validateXType(field, xType, valueKind string, rows []map[string]any) error {
	switch xType {
	case "quantitative":
		if valueKind != "number" {
			return fmt.Errorf("xType quantitative requires numeric x field %q", field)
		}
	case "temporal":
		if valueKind != "string" {
			return fmt.Errorf("xType temporal requires string x values in field %q", field)
		}
		for i, row := range rows {
			value := row[field].(string)
			if !validChartTime(value) {
				return fmt.Errorf("data row %d x field %q must be an ISO date or RFC3339 timestamp for temporal charts", i+1, field)
			}
		}
	case "nominal":
		// All non-null scalar types are valid nominal categories. The caller
		// already enforces that each row uses the same JSON scalar type.
	default:
		return fmt.Errorf("unsupported xType %q", xType)
	}
	return nil
}

// Go's RFC3339 parser also accepts a few non-RFC spellings browsers reject.
var chartTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)

func validChartTime(value string) bool {
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return true
	}
	if !chartTimestamp.MatchString(value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// rejectDuplicateJSONKeys prevents encoding/json's usual last-key-wins
// behavior from changing a caller's rows or request fields silently.
func rejectDuplicateJSONKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contains trailing JSON data")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}
