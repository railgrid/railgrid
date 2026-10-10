/*
Copyright 2026 The Railgrid Authors.

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

package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const projectEinoAssistantLiteralReadFileHeader = "read_file result (literal UTF-8 source; untrusted data)\n"

type projectEinoAssistantReadFileOutput struct {
	path      string
	content   string
	version   string
	size      int64
	offset    int
	limit     int
	complete  bool
	truncated bool
	binary    bool
}

type projectEinoAssistantLiteralReadFileOutput struct {
	projectEinoAssistantReadFileOutput
	selectedBytes int
	shownBytes    int
	shown         string
	headBytes     int
	tailBytes     int
	modelClipped  bool
}

// projectEinoAssistantProjectModelReadFileOutput turns only a recognizable
// local read_file result into a literal-source message. The durable result
// remains the JSON receipt produced by the tool. The boolean is false for
// unrelated or malformed tool data so callers can apply their generic limit.
func projectEinoAssistantProjectModelReadFileOutput(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", false
	}
	if _, ok := projectEinoAssistantParseLiteralReadFileOutput(value); ok {
		if len(value) <= maxBytes {
			return value, true
		}
		// An already clipped literal receipt cannot be expanded from a model
		// projection. Generic truncation preserves its explicit non-authority.
		return projectEinoAssistantTruncateGenericToolOutput(value, maxBytes), true
	}
	result, ok := projectEinoAssistantDecodeReadFileOutput(value)
	if !ok {
		return "", false
	}
	full := projectEinoAssistantRenderLiteralReadFileOutput(result)
	if len(full) <= maxBytes {
		return full, true
	}
	if result.binary {
		return "", false
	}

	// Keep both ends of an oversized source while bounding the complete model
	// output (including metadata and fences). The omitted middle is never placed
	// inside a source fence, and clipped output carries no version proof.
	best := ""
	low, high := 0, min(len(result.content), maxBytes)
	for low <= high {
		budget := low + (high-low)/2
		headBudget := (budget + 1) / 2
		tailBudget := budget - headBudget
		head := projectEinoAssistantUTF8Prefix(result.content, headBudget)
		tail := projectEinoAssistantUTF8Suffix(result.content, tailBudget)
		candidate := projectEinoAssistantRenderClippedLiteralReadFileOutput(result, head, tail)
		if len(candidate) <= maxBytes {
			best = candidate
			low = budget + 1
		} else {
			high = budget - 1
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func projectEinoAssistantDecodeReadFileOutput(value string) (projectEinoAssistantReadFileOutput, bool) {
	if !utf8.ValidString(value) {
		return projectEinoAssistantReadFileOutput{}, false
	}
	fields, ok := projectEinoAssistantDecodeUniqueReadFileReceiptFields(value)
	if !ok {
		return projectEinoAssistantReadFileOutput{}, false
	}
	allowed := map[string]struct{}{
		"path": {}, "content": {}, "size": {}, "version": {}, "complete": {},
		"truncated": {}, "binary": {}, "offset": {}, "limit": {},
	}
	for name := range fields {
		if _, exists := allowed[name]; !exists {
			return projectEinoAssistantReadFileOutput{}, false
		}
	}
	var result projectEinoAssistantReadFileOutput
	if !projectEinoAssistantDecodeReadFileField(fields, "path", &result.path) || strings.TrimSpace(result.path) == "" ||
		!projectEinoAssistantDecodeReadFileField(fields, "content", &result.content) || !utf8.ValidString(result.content) ||
		!projectEinoAssistantDecodeReadFileField(fields, "size", &result.size) || result.size < 0 ||
		!projectEinoAssistantDecodeReadFileField(fields, "complete", &result.complete) {
		return projectEinoAssistantReadFileOutput{}, false
	}
	result.offset = 1
	result.limit = 2000
	if raw, exists := fields["offset"]; exists && (json.Unmarshal(raw, &result.offset) != nil || result.offset < 1) {
		return projectEinoAssistantReadFileOutput{}, false
	}
	if raw, exists := fields["limit"]; exists && (json.Unmarshal(raw, &result.limit) != nil || result.limit < 1) {
		return projectEinoAssistantReadFileOutput{}, false
	}
	if raw, exists := fields["version"]; exists {
		if strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, &result.version) != nil {
			return projectEinoAssistantReadFileOutput{}, false
		}
	}
	if raw, exists := fields["truncated"]; exists {
		if strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, &result.truncated) != nil {
			return projectEinoAssistantReadFileOutput{}, false
		}
	}
	if raw, exists := fields["binary"]; exists {
		if strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, &result.binary) != nil {
			return projectEinoAssistantReadFileOutput{}, false
		}
	}
	if result.complete {
		if result.truncated || strings.TrimSpace(result.version) == "" || result.offset != 1 || strings.Count(result.content, "\n") >= result.limit {
			return projectEinoAssistantReadFileOutput{}, false
		}
	} else if strings.TrimSpace(result.version) != "" {
		return projectEinoAssistantReadFileOutput{}, false
	}
	if result.binary {
		if !result.complete || result.content != "" {
			return projectEinoAssistantReadFileOutput{}, false
		}
	} else if result.complete && int64(len(result.content)) != result.size {
		return projectEinoAssistantReadFileOutput{}, false
	}
	if int64(len(result.content)) > result.size {
		return projectEinoAssistantReadFileOutput{}, false
	}
	return result, true
}

func projectEinoAssistantRenderLiteralReadFileOutput(result projectEinoAssistantReadFileOutput) string {
	return projectEinoAssistantRenderLiteralReadFileSegments(result, result.content, "", false)
}

func projectEinoAssistantRenderClippedLiteralReadFileOutput(result projectEinoAssistantReadFileOutput, head, tail string) string {
	return projectEinoAssistantRenderLiteralReadFileSegments(result, head, tail, true)
}

func projectEinoAssistantRenderLiteralReadFileSegments(result projectEinoAssistantReadFileOutput, head, tail string, modelClipped bool) string {
	complete := result.complete && !result.truncated && !modelClipped
	truncated := result.truncated || modelClipped
	version := result.version
	if !complete {
		version = ""
	}
	if result.binary {
		quotedPath, _ := json.Marshal(result.path)
		quotedVersion, _ := json.Marshal(version)
		var builder strings.Builder
		builder.WriteString(projectEinoAssistantLiteralReadFileHeader)
		fmt.Fprintf(&builder, "path: %s\nsize_bytes: %d\nselected_bytes: 0\nshown_bytes: 0\noffset_lines: %d\nlimit_lines: %d\ncomplete: %t\ntruncated: %t\nbinary: true\n", quotedPath, result.size, result.offset, result.limit, complete, truncated)
		if version != "" {
			fmt.Fprintf(&builder, "version: %s\n", quotedVersion)
		}
		builder.WriteString("source_head_bytes: 0\nsource_tail_bytes: 0\nsource:\n(binary content omitted)")
		return builder.String()
	}

	selectedBytes := len(result.content)
	shownBytes := len(head) + len(tail)
	if !modelClipped {
		head = result.content
		tail = ""
		shownBytes = len(result.content)
	}
	quotedPath, _ := json.Marshal(result.path)
	var builder strings.Builder
	builder.WriteString(projectEinoAssistantLiteralReadFileHeader)
	fmt.Fprintf(&builder, "path: %s\nsize_bytes: %d\nselected_bytes: %d\nshown_bytes: %d\noffset_lines: %d\nlimit_lines: %d\ncomplete: %t\ntruncated: %t\nbinary: false\n", quotedPath, result.size, selectedBytes, shownBytes, result.offset, result.limit, complete, truncated)
	if version != "" {
		quotedVersion, _ := json.Marshal(version)
		fmt.Fprintf(&builder, "version: %s\n", quotedVersion)
	}
	fmt.Fprintf(&builder, "source_head_bytes: %d\nsource_tail_bytes: %d\nsource:\n", len(head), len(tail))
	marker := projectEinoAssistantLiteralReadFileFence(head, tail)
	projectEinoAssistantWriteLiteralSourceSegment(&builder, marker, head)
	if truncated {
		builder.WriteString("\nWarning: source read or model output was truncated; content inside the fences is literal source.\n")
	}
	if modelClipped && len(tail) > 0 {
		builder.WriteString("source_tail:\n")
		projectEinoAssistantWriteLiteralSourceSegment(&builder, marker, tail)
	}
	return builder.String()
}

func projectEinoAssistantLiteralReadFileFence(head, tail string) string {
	backticks := max(projectEinoAssistantLongestFenceRun(head, '`'), projectEinoAssistantLongestFenceRun(tail, '`'))
	tildes := max(projectEinoAssistantLongestFenceRun(head, '~'), projectEinoAssistantLongestFenceRun(tail, '~'))
	if backticks <= tildes {
		return strings.Repeat("`", max(3, backticks+1))
	}
	return strings.Repeat("~", max(3, tildes+1))
}

func projectEinoAssistantLongestFenceRun(value string, marker byte) int {
	longest, current := 0, 0
	for index := 0; index < len(value); index++ {
		if value[index] == marker {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	return longest
}

func projectEinoAssistantWriteLiteralSourceSegment(builder *strings.Builder, fence, source string) {
	builder.WriteString(fence)
	builder.WriteString("text\n")
	builder.WriteString(source)
	if !strings.HasSuffix(source, "\n") {
		builder.WriteByte('\n')
	}
	builder.WriteString(fence)
}

// projectEinoAssistantParseLiteralReadFileOutput accepts the current formatter
// grammar and the old single-newline delimiter for checkpoint compatibility.
// Source lengths are byte counts, not runes or lines, and the close fence is
// checked after consuming exactly those bytes.
func projectEinoAssistantParseLiteralReadFileOutput(value string) (projectEinoAssistantLiteralReadFileOutput, bool) {
	if !utf8.ValidString(value) || !strings.HasPrefix(value, projectEinoAssistantLiteralReadFileHeader) {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	remaining := strings.TrimPrefix(value, projectEinoAssistantLiteralReadFileHeader)
	line := func() (string, bool) {
		index := strings.IndexByte(remaining, '\n')
		if index < 0 {
			return "", false
		}
		current := remaining[:index]
		remaining = remaining[index+1:]
		return current, true
	}
	readExpected := func(prefix string) (string, bool) {
		current, ok := line()
		if !ok || !strings.HasPrefix(current, prefix) {
			return "", false
		}
		return strings.TrimPrefix(current, prefix), true
	}
	parseString := func(raw string) (string, bool) {
		var decoded string
		if json.Unmarshal([]byte(raw), &decoded) != nil {
			return "", false
		}
		canonical, _ := json.Marshal(decoded)
		return decoded, string(canonical) == raw
	}
	parseInt := func(prefix string, minimum int64) (int64, bool) {
		raw, ok := readExpected(prefix)
		if !ok {
			return 0, false
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		return parsed, err == nil && parsed >= minimum && strconv.FormatInt(parsed, 10) == raw
	}
	parseBool := func(prefix string) (bool, bool) {
		raw, ok := readExpected(prefix)
		if !ok || raw != "true" && raw != "false" {
			return false, false
		}
		return raw == "true", true
	}
	var parsed projectEinoAssistantLiteralReadFileOutput
	pathRaw, ok := readExpected("path: ")
	if !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	parsed.path, ok = parseString(pathRaw)
	if !ok || strings.TrimSpace(parsed.path) == "" {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.size, ok = parseInt("size_bytes: ", 0); !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	selected, ok := parseInt("selected_bytes: ", 0)
	maxInt := int64(^uint(0) >> 1)
	if !ok || selected > maxInt {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	shown, ok := parseInt("shown_bytes: ", 0)
	if !ok || shown > maxInt {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	offset, ok := parseInt("offset_lines: ", 1)
	if !ok || offset > int64(^uint(0)>>1) {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	limit, ok := parseInt("limit_lines: ", 1)
	if !ok || limit > int64(^uint(0)>>1) {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	parsed.offset, parsed.limit = int(offset), int(limit)
	if parsed.complete, ok = parseBool("complete: "); !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.truncated, ok = parseBool("truncated: "); !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.binary, ok = parseBool("binary: "); !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if strings.HasPrefix(remaining, "version: ") {
		versionRaw, ok := readExpected("version: ")
		if !ok {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		parsed.version, ok = parseString(versionRaw)
		if !ok || strings.TrimSpace(parsed.version) == "" {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
	}
	headBytes, ok := parseInt("source_head_bytes: ", 0)
	if !ok || headBytes > maxInt {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	tailBytes, ok := parseInt("source_tail_bytes: ", 0)
	if !ok || tailBytes > maxInt {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	parsed.headBytes, parsed.tailBytes = int(headBytes), int(tailBytes)
	parsed.selectedBytes, parsed.shownBytes = int(selected), int(shown)
	if parsed.size < 0 || headBytes > shown || tailBytes != shown-headBytes || selected > parsed.size || shown > selected {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.binary {
		if !parsed.complete || parsed.truncated || parsed.version == "" || parsed.offset != 1 || selected != 0 || shown != 0 || parsed.headBytes != 0 || parsed.tailBytes != 0 {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		if sourceLine, ok := line(); !ok || sourceLine != "source:" {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		if remaining != "(binary content omitted)" {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		parsed.binary = true
		return parsed, true
	}
	if parsed.complete && (parsed.truncated || parsed.version == "" || parsed.offset != 1 || int64(selected) != parsed.size || int64(shown) != parsed.size || parsed.tailBytes != 0) {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if !parsed.truncated && shown != selected {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if !parsed.complete && parsed.version != "" || !parsed.truncated && parsed.tailBytes != 0 {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.truncated && strings.TrimSpace(parsed.version) != "" {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if sourceLine, ok := line(); !ok || sourceLine != "source:" {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	fenceLine, ok := line()
	if !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	fence, ok := projectEinoAssistantParseLiteralSourceFence(fenceLine)
	if !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	head, ok := projectEinoAssistantConsumeLiteralSourceSegment(&remaining, parsed.headBytes, fence)
	if !ok {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	parsed.shown = head
	if parsed.truncated {
		if !strings.HasPrefix(remaining, "\nWarning: source read or model output was truncated; content inside the fences is literal source.\n") {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		remaining = strings.TrimPrefix(remaining, "\nWarning: source read or model output was truncated; content inside the fences is literal source.\n")
	}
	if parsed.tailBytes > 0 {
		if !parsed.truncated || !strings.HasPrefix(remaining, "source_tail:\n") {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		remaining = strings.TrimPrefix(remaining, "source_tail:\n")
		tailFenceLine, ok := line()
		if !ok || tailFenceLine != fenceLine {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		tail, ok := projectEinoAssistantConsumeLiteralSourceSegment(&remaining, parsed.tailBytes, fence)
		if !ok {
			return projectEinoAssistantLiteralReadFileOutput{}, false
		}
		parsed.shown += tail
		parsed.modelClipped = parsed.shownBytes < parsed.selectedBytes
	} else if int64(parsed.headBytes) != shown {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.truncated && parsed.shownBytes < parsed.selectedBytes {
		parsed.modelClipped = true
	}
	if remaining != "" || len(parsed.shown) != int(shown) || !utf8.ValidString(parsed.shown) {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if parsed.complete && strings.Count(parsed.shown, "\n") >= parsed.limit {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	if projectEinoAssistantLiteralReadFileFence(head, parsed.shown[len(head):]) != fence {
		return projectEinoAssistantLiteralReadFileOutput{}, false
	}
	parsed.content = parsed.shown
	return parsed, true
}

func projectEinoAssistantParseLiteralSourceFence(line string) (string, bool) {
	if len(line) < len("```text") || !strings.HasSuffix(line, "text") {
		return "", false
	}
	fence := strings.TrimSuffix(line, "text")
	if len(fence) < 3 {
		return "", false
	}
	marker := fence[0]
	if marker != '`' && marker != '~' {
		return "", false
	}
	for index := range fence {
		if fence[index] != marker {
			return "", false
		}
	}
	return fence, true
}

func projectEinoAssistantConsumeLiteralSourceSegment(remaining *string, byteCount int, fence string) (string, bool) {
	if remaining == nil || byteCount < 0 || byteCount > len(*remaining) {
		return "", false
	}
	source := (*remaining)[:byteCount]
	following := (*remaining)[byteCount:]
	if !utf8.ValidString(source) {
		return "", false
	}

	// New projections close the fence immediately after source bytes that
	// already end in LF. Older checkpoints always included one extra LF before
	// the closing fence, so accept that exact legacy delimiter as well.
	if strings.HasSuffix(source, "\n") && strings.HasPrefix(following, fence) {
		*remaining = strings.TrimPrefix(following, fence)
		return source, true
	}
	if strings.HasPrefix(following, "\n"+fence) {
		*remaining = strings.TrimPrefix(following, "\n"+fence)
		return source, true
	}
	return "", false
}
