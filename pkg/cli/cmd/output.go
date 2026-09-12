/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

// Output formats every list/get command understands. Table is the default;
// wide adds columns; json and yaml print the underlying objects; name prints
// one identifier per line for shell pipelines.
const (
	outputTable = ""
	outputWide  = "wide"
	outputJSON  = "json"
	outputYAML  = "yaml"
	outputName  = "name"
)

// outputFlags is the shared -o/--output flag. Commands declare which formats
// they support; the default set is table, wide, json, yaml and name.
type outputFlags struct {
	format  string
	allowed []string
}

func newOutputFlags(allowed ...string) *outputFlags {
	if len(allowed) == 0 {
		allowed = []string{outputWide, outputJSON, outputYAML, outputName}
	}
	return &outputFlags{allowed: allowed}
}

// addFlag registers -o on cmd and validates the value before RunE.
func (o *outputFlags) addFlag(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.format, "output", "o", "", "Output format: "+strings.Join(o.allowed, ", "))
	_ = cmd.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return o.allowed, cobra.ShellCompDirectiveNoFileComp
	})
	prev := cmd.PreRunE
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if err := o.validate(); err != nil {
			return err
		}
		if prev != nil {
			return prev(cmd, args)
		}
		return nil
	}
}

func (o *outputFlags) validate() error {
	if o.format == outputTable {
		return nil
	}
	for _, a := range o.allowed {
		if o.format == a {
			return nil
		}
	}
	return fmt.Errorf("unsupported output format %q (want one of: %s)", o.format, strings.Join(o.allowed, ", "))
}

// structured reports whether the format prints objects (json/yaml) rather
// than a rendered table.
func (o *outputFlags) structured() bool {
	return o.format == outputJSON || o.format == outputYAML
}

func (o *outputFlags) wide() bool  { return o.format == outputWide }
func (o *outputFlags) names() bool { return o.format == outputName }

// printStructured writes v as JSON or YAML according to the format. v may be
// a json.RawMessage (printed verbatim, re-indented) or any value.
func (o *outputFlags) printStructured(w io.Writer, v any) error {
	switch o.format {
	case outputJSON:
		return printJSON(w, v)
	case outputYAML:
		var b []byte
		var err error
		if raw, ok := v.(json.RawMessage); ok {
			b, err = yaml.JSONToYAML(raw)
		} else {
			b, err = yaml.Marshal(v)
		}
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	default:
		return fmt.Errorf("format %q is not structured", o.format)
	}
}

// printNames writes one name per line.
func printNames(w io.Writer, names []string) error {
	for _, n := range names {
		if _, err := fmt.Fprintln(w, n); err != nil {
			return err
		}
	}
	return nil
}

// table is a minimal column printer over tabwriter: headers once, then rows.
// Empty cells render as "-" so columns stay aligned and readable.
type table struct {
	headers []string
	rows    [][]string
}

func (t *table) add(cols ...string) {
	for i := range cols {
		if cols[i] == "" {
			cols[i] = "-"
		}
	}
	t.rows = append(t.rows, cols)
}

func (t *table) write(w io.Writer) error {
	tw := newTabWriter(w)
	printRow(tw, t.headers...)
	for _, r := range t.rows {
		printRow(tw, r...)
	}
	return tw.Flush()
}
