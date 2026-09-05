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

// govulncheck-filter turns `govulncheck -format json` output into a blocking
// CI gate with an auditable allowlist. It is invoked by hack/govulncheck.sh,
// never directly.
//
// Two things it does that plain `govulncheck ./...` cannot:
//
//  1. Reports only findings govulncheck traced to a *called* symbol. The JSON
//     stream carries one finding per reachability level for the same OSV:
//     module-only ("this version is in your module graph"), package-level
//     ("something imports it"), and symbol-level ("this function is reachable
//     in the call graph"). Only the last is a vulnerability in this binary;
//     the first two are inventory. `govulncheck ./...` in text mode already
//     applies that rule, but exits 3 either way, so CI cannot tell the two
//     apart. Here, a finding counts only when trace[0] names a function.
//
//  2. Drops OSV IDs listed in the allowlist (hack/govulncheck-allow.yaml),
//     which is how the residue with no upstream fix stays off the red list
//     without silencing everything else. govulncheck has no native suppression.
//
// Exits 1 when anything survives both filters. A stale allowlist entry - an ID
// whose module is still in the scanned build but which no longer produces any
// finding - is a warning only, never a failure: an allowlist that fails closed
// on its own success would make dependency upgrades punish the upgrader.
//
// Usage:
//
//	govulncheck -format json ./... | go run ./hack/govulncheck \
//	  -allowlist hack/govulncheck-allow.yaml -label ./providers/edges
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// message is one object in the govulncheck JSON stream. The stream is a
// concatenation of these, NOT newline-delimited JSON, so it has to be read with
// a streaming decoder rather than split on lines.
type message struct {
	OSV     *osvEntry `json:"osv,omitempty"`
	Finding *finding  `json:"finding,omitempty"`
	SBOM    *sbom     `json:"SBOM,omitempty"`
}

type osvEntry struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type finding struct {
	OSV          string  `json:"osv"`
	FixedVersion string  `json:"fixed_version"`
	Trace        []frame `json:"trace"`
}

// frame is one call-graph step. trace[0] is the vulnerable symbol itself and
// the last element is the entry point in the scanned module.
type frame struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Package  string `json:"package"`
	Function string `json:"function"`
	Receiver string `json:"receiver"`
}

type sbom struct {
	GoVersion string `json:"go_version"`
	Modules   []struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	} `json:"modules"`
}

// allowlist is the on-disk schema of hack/govulncheck-allow.yaml.
type allowlist struct {
	Allow []allowEntry `json:"allow"`
}

type allowEntry struct {
	ID       string `json:"id"`
	Module   string `json:"module"`
	Reason   string `json:"reason"`
	Exposure string `json:"exposure"`
	ReviewBy string `json:"reviewBy"`
}

// called is an OSV that govulncheck traced to a reachable symbol.
type called struct {
	id      string
	summary string
	module  string
	version string
	fixedIn string
	symbol  string // the vulnerable function
	entry   string // the symbol in the scanned module that reaches it
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "govulncheck-filter: %v\n", err)
		os.Exit(2)
	}
}

func run() error {
	var (
		allowPath = flag.String("allowlist", "", "path to the allowlist YAML (required)")
		label     = flag.String("label", "", "module label used in the printed summary")
	)
	flag.Parse()

	if *allowPath == "" {
		return errors.New("-allowlist is required")
	}
	allowed, order, err := loadAllowlist(*allowPath)
	if err != nil {
		return err
	}

	summaries, findings, bom, err := decodeStream(os.Stdin)
	if err != nil {
		return err
	}

	// Bucket the findings by reachability. The same OSV usually appears at
	// several levels; keep the first symbol-level trace we see for each.
	reachable := map[string]called{}
	seen := map[string]bool{}
	for _, f := range findings {
		seen[f.OSV] = true
		if len(f.Trace) == 0 || f.Trace[0].Function == "" {
			continue // module- or package-level only: inventory, not a call
		}
		if _, ok := reachable[f.OSV]; ok {
			continue
		}
		top, bottom := f.Trace[0], f.Trace[len(f.Trace)-1]
		reachable[f.OSV] = called{
			id:      f.OSV,
			summary: summaries[f.OSV],
			module:  top.Module,
			version: top.Version,
			fixedIn: f.FixedVersion,
			symbol:  symbol(top),
			entry:   symbol(bottom),
		}
	}

	scanned := map[string]bool{}
	goVersion := "unknown"
	if bom != nil {
		goVersion = bom.GoVersion
		for _, m := range bom.Modules {
			scanned[m.Path] = true
		}
	}

	name := *label
	if name == "" {
		name = "module"
	}
	fmt.Printf("govulncheck %s (%s): %d vulnerabilities in the module graph, %d reachable\n",
		name, goVersion, len(seen), len(reachable))

	var blocking, suppressed []called
	for _, id := range sortedKeys(reachable) {
		if _, ok := allowed[id]; ok {
			suppressed = append(suppressed, reachable[id])
			continue
		}
		blocking = append(blocking, reachable[id])
	}

	for _, c := range suppressed {
		fmt.Printf("  allowed  %s  %s@%s  %s\n", c.id, c.module, c.version, c.summary)
	}

	// A stale entry is one whose module is still linked into this build but
	// which no longer reports anything - i.e. the upgrade that fixed it landed
	// and the entry can be deleted. Warn only; see the package comment.
	for _, id := range order {
		e := allowed[id]
		if seen[id] || !scanned[e.Module] {
			continue
		}
		fmt.Printf("  WARNING  %s is allowlisted but no longer reported for %s in %s; drop the entry from %s\n",
			id, e.Module, name, *allowPath)
	}
	// Same idea for the review-by date: surface it, never fail on it.
	for _, id := range order {
		e := allowed[id]
		if e.ReviewBy == "" {
			continue
		}
		due, err := time.Parse("2006-01-02", e.ReviewBy)
		if err != nil {
			return fmt.Errorf("allowlist entry %s: reviewBy %q is not YYYY-MM-DD: %w", id, e.ReviewBy, err)
		}
		if seen[id] && time.Now().After(due) {
			fmt.Printf("  WARNING  %s is past its review-by date (%s); re-justify it or remove it\n", id, e.ReviewBy)
		}
	}

	if len(blocking) == 0 {
		fmt.Printf("  OK       no reachable vulnerabilities outside the allowlist\n")
		return nil
	}

	fmt.Printf("\n%d reachable vulnerability(ies) in %s are not allowlisted:\n\n", len(blocking), name)
	for _, c := range blocking {
		fmt.Printf("  %s  %s\n", c.id, c.summary)
		fmt.Printf("    module:  %s@%s\n", c.module, c.version)
		if c.fixedIn != "" {
			fmt.Printf("    fixed:   %s  (go get %s@%s && go mod tidy)\n", c.fixedIn, c.module, c.fixedIn)
		} else {
			fmt.Printf("    fixed:   no upstream fix\n")
		}
		fmt.Printf("    called:  %s\n", c.symbol)
		fmt.Printf("    from:    %s\n", c.entry)
		fmt.Printf("    details: https://pkg.go.dev/vuln/%s\n\n", c.id)
	}
	fmt.Printf("Upgrade the module, or - if there is no fix - add the ID to %s with a\n", *allowPath)
	fmt.Printf("justification and a review-by date. Do not widen the allowlist to silence a\n")
	fmt.Printf("vulnerability that an upgrade would fix.\n")

	os.Exit(1)
	return nil
}

func symbol(f frame) string {
	var b strings.Builder
	if f.Package != "" {
		b.WriteString(f.Package)
		b.WriteString(".")
	}
	if f.Receiver != "" {
		b.WriteString(f.Receiver)
		b.WriteString(".")
	}
	b.WriteString(f.Function)
	return b.String()
}

// decodeStream reads the concatenated JSON objects govulncheck writes.
func decodeStream(r io.Reader) (map[string]string, []finding, *sbom, error) {
	var (
		summaries = map[string]string{}
		findings  []finding
		bom       *sbom
	)
	dec := json.NewDecoder(r)
	for {
		var m message
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, nil, fmt.Errorf("decoding govulncheck output: %w", err)
		}
		switch {
		case m.OSV != nil:
			summaries[m.OSV.ID] = m.OSV.Summary
		case m.Finding != nil:
			findings = append(findings, *m.Finding)
		case m.SBOM != nil:
			bom = m.SBOM
		}
	}
	return summaries, findings, bom, nil
}

func loadAllowlist(path string) (map[string]allowEntry, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading allowlist: %w", err)
	}
	var list allowlist
	if err := yaml.Unmarshal(raw, &list); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	out := make(map[string]allowEntry, len(list.Allow))
	order := make([]string, 0, len(list.Allow))
	for _, e := range list.Allow {
		switch {
		case e.ID == "":
			return nil, nil, fmt.Errorf("%s: an entry is missing `id`", path)
		case e.Module == "":
			return nil, nil, fmt.Errorf("%s: entry %s is missing `module`", path, e.ID)
		case e.Reason == "":
			return nil, nil, fmt.Errorf("%s: entry %s is missing `reason`", path, e.ID)
		case e.ReviewBy == "":
			return nil, nil, fmt.Errorf("%s: entry %s is missing `reviewBy`", path, e.ID)
		}
		out[e.ID] = e
		order = append(order, e.ID)
	}
	return out, order, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
