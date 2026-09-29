// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"slices"
	"strings"
)

// sourcesHeading is the marker a turn is asked to put its citations under (see
// the worker preamble a spawned sub-task is given).
const sourcesHeading = "sources:"

// SplitSources separates a trailing "Sources:" block from the body, which is
// how a backend that produces prose fills Outcome.Output and Outcome.Sources.
//
// It lives at the seam because the convention is the provider's — a worker is
// instructed to end with the block, and the parent reads the result as
// structure rather than re-reading prose — so every backend that answers in
// text splits it the same way. A missing or malformed block is not an error:
// the body is returned whole.
func SplitSources(content string) (body string, sources []string) {
	lines := strings.Split(content, "\n")
	// Find the LAST heading: a turn may quote the word earlier in its answer.
	idx := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "**")), sourcesHeading) ||
			strings.EqualFold(strings.TrimSpace(l), "**"+sourcesHeading+"**") {
			idx = i
		}
	}
	if idx < 0 {
		return strings.TrimSpace(content), nil
	}
	for _, l := range lines[idx+1:] {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "-")
		l = strings.TrimPrefix(l, "*")
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Keep only what looks like a locator; a turn sometimes trails prose
		// after the list.
		if !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
			continue
		}
		if fields := strings.Fields(l); len(fields) > 0 {
			l = fields[0]
		}
		if !slices.Contains(sources, l) {
			sources = append(sources, l)
		}
	}
	return strings.TrimSpace(strings.Join(lines[:idx], "\n")), sources
}
