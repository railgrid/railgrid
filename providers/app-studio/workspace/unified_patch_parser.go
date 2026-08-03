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

package workspace

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Keep these markers in one place. The parser intentionally follows Codex's
// whitespace-tolerant boundary/header behavior and strict Add File body
// grammar while retaining App Studio's stronger mutation preflight and
// rollback guarantees.
const (
	patchBeginMarker   = "*** Begin Patch"
	patchEndMarker     = "*** End Patch"
	patchAddMarker     = "*** Add File: "
	patchDeleteMarker  = "*** Delete File: "
	patchUpdateMarker  = "*** Update File: "
	patchMoveMarker    = "*** Move to: "
	patchEndFileMarker = "*** End of File"
)

var standardUnifiedDiffHunkHeader = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+[0-9]+(?:,[0-9]+)? @@(?: .*)?$`)

type patchOperationKind uint8

const (
	patchOperationAdd patchOperationKind = iota + 1
	patchOperationDelete
	patchOperationUpdate
)

type parsedPatch struct {
	operations []patchOperation
}

type patchOperation struct {
	kind     patchOperationKind
	path     string
	movePath string
	content  string
	chunks   []patchChunk
}

type patchChunk struct {
	anchor    string
	oldLines  []string
	newLines  []string
	endOfFile bool
}

// PatchPaths parses a patch without touching the workspace and returns every
// canonical source and destination path it can affect. Authorization layers
// must approve every returned path before invoking ApplyPatch.
func PatchPaths(patch string) ([]string, error) {
	parsed, err := parseUnifiedPatch(patch)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0, len(parsed.operations)*2)
	for _, operation := range parsed.operations {
		for _, candidate := range []string{operation.path, operation.movePath} {
			if candidate == "" {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			paths = append(paths, candidate)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// PatchReadPaths returns the canonical existing-file paths whose current
// contents a patch relies on. Add File targets are intentionally excluded.
func PatchReadPaths(patch string) ([]string, error) {
	parsed, err := parseUnifiedPatch(patch)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(parsed.operations))
	for _, operation := range parsed.operations {
		if operation.kind != patchOperationAdd {
			paths = append(paths, operation.path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// ValidateCommittablePatch verifies that an assistant patch is syntactically
// valid before it reaches the workspace mutation boundary. The repository
// bridge supports every parsed operation, including deletion and move.
func ValidateCommittablePatch(patch string) error {
	_, err := parseUnifiedPatch(patch)
	return err
}

func parseUnifiedPatch(raw string) (parsedPatch, error) {
	if len([]byte(raw)) > MaxUnifiedPatchBytes {
		return parsedPatch{}, newPatchError(
			PatchErrorInvalidPatch,
			"",
			0,
			0,
			"patch is too large: %d > %d bytes",
			len([]byte(raw)),
			MaxUnifiedPatchBytes,
		)
	}
	if !validTextContent(raw) {
		return parsedPatch{}, newPatchError(PatchErrorInvalidPatch, "", 0, 0, "patch must be UTF-8 text without NUL bytes")
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.TrimSpace(normalized)
	lines := strings.Split(normalized, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != patchBeginMarker {
		return parsedPatch{}, newPatchError(PatchErrorInvalidPatch, "", 0, 0, "the first line must be '*** Begin Patch'")
	}
	if strings.TrimSpace(lines[len(lines)-1]) != patchEndMarker {
		return parsedPatch{}, newPatchError(PatchErrorInvalidPatch, "", 0, 0, "the last line must be '*** End Patch'")
	}

	parsed := parsedPatch{}
	for lineIndex := 1; lineIndex < len(lines)-1; {
		line := lines[lineIndex]
		if strings.TrimSpace(line) == "" {
			lineIndex++
			continue
		}
		switch patchOperationMarker(strings.TrimSpace(line)) {
		case patchAddMarker:
			filePath, err := parsePatchMarkerPath(line, patchAddMarker, lineIndex+1)
			if err != nil {
				return parsedPatch{}, err
			}
			lineIndex++
			added := []string{}
			for lineIndex < len(lines)-1 && !isPatchFileMarker(lines[lineIndex]) {
				// Add File body lines follow Codex's grammar: the leading '+' is
				// required and is stripped before writing the new file content.
				// Structural headers are checked by the loop condition first, so
				// whitespace-padded headers remain valid section boundaries while
				// '+ *** Update File: example' remains literal file content.
				if !strings.HasPrefix(lines[lineIndex], "+") {
					return parsedPatch{}, invalidPatchLine(
						lineIndex+1,
						"Add File content lines must begin with '+'; encode literal marker-looking content as '+ *** Update File: example'",
					)
				}
				contentLine := lines[lineIndex][1:]
				added = append(added, contentLine)
				lineIndex++
			}
			if len(added) == 0 {
				return parsedPatch{}, invalidPatchLine(lineIndex+1, "Add File requires at least one content line")
			}
			parsed.operations = append(parsed.operations, patchOperation{
				kind:    patchOperationAdd,
				path:    filePath,
				content: strings.Join(added, "\n") + "\n",
			})

		case patchDeleteMarker:
			filePath, err := parsePatchMarkerPath(line, patchDeleteMarker, lineIndex+1)
			if err != nil {
				return parsedPatch{}, err
			}
			lineIndex++
			if lineIndex < len(lines)-1 && !isPatchFileMarker(lines[lineIndex]) {
				return parsedPatch{}, invalidPatchLine(lineIndex+1, "Delete File cannot contain patch lines")
			}
			parsed.operations = append(parsed.operations, patchOperation{kind: patchOperationDelete, path: filePath})

		case patchUpdateMarker:
			operation, next, err := parseUpdateOperation(lines, lineIndex)
			if err != nil {
				return parsedPatch{}, err
			}
			parsed.operations = append(parsed.operations, operation)
			lineIndex = next

		default:
			return parsedPatch{}, invalidPatchLine(lineIndex+1, "expected Add File, Delete File, or Update File header")
		}
	}
	if len(parsed.operations) == 0 {
		return parsedPatch{}, newPatchError(PatchErrorInvalidPatch, "", 0, 0, "patch must contain at least one file operation")
	}
	if err := validatePatchOperationPaths(parsed.operations); err != nil {
		return parsedPatch{}, err
	}
	return parsed, nil
}

func parseUpdateOperation(lines []string, start int) (patchOperation, int, error) {
	filePath, err := parsePatchMarkerPath(lines[start], patchUpdateMarker, start+1)
	if err != nil {
		return patchOperation{}, start, err
	}
	operation := patchOperation{kind: patchOperationUpdate, path: filePath}
	lineIndex := start + 1
	if lineIndex < len(lines)-1 && isPatchMarkerWithTrailingWhitespace(lines[lineIndex], patchMoveMarker) {
		movePath, err := parsePatchMarkerPath(lines[lineIndex], patchMoveMarker, lineIndex+1)
		if err != nil {
			return patchOperation{}, start, err
		}
		operation.movePath = movePath
		lineIndex++
	}

	var current *patchChunk
	flush := func() {
		if current != nil {
			operation.chunks = append(operation.chunks, *current)
			current = nil
		}
	}
	for lineIndex < len(lines)-1 && !isPatchFileMarkerInUpdate(lines[lineIndex]) {
		line := lines[lineIndex]
		structuralLine := strings.TrimRight(line, " \t")
		switch {
		case standardUnifiedDiffHunkHeader.MatchString(structuralLine):
			return patchOperation{}, start, invalidPatchLine(lineIndex+1, "numeric unified-diff hunk headers are not supported; use exactly '@@' or '@@ <literal source line>'")
		case structuralLine == "@@" || strings.HasPrefix(structuralLine, "@@ "):
			flush()
			current = &patchChunk{anchor: strings.TrimSpace(strings.TrimPrefix(structuralLine, "@@"))}
		case structuralLine == patchEndFileMarker:
			if current == nil {
				return patchOperation{}, start, invalidPatchLine(lineIndex+1, "End of File requires a preceding hunk")
			}
			current.endOfFile = true
			lineIndex++
			if lineIndex < len(lines)-1 && !isPatchFileMarkerInUpdate(lines[lineIndex]) {
				return patchOperation{}, start, invalidPatchLine(lineIndex+1, "End of File must finish the current file update")
			}
			continue
		case strings.HasPrefix(line, " "), strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			if current == nil {
				current = &patchChunk{}
			}
			text := line[1:]
			switch line[0] {
			case ' ':
				current.oldLines = append(current.oldLines, text)
				current.newLines = append(current.newLines, text)
			case '+':
				current.newLines = append(current.newLines, text)
			case '-':
				current.oldLines = append(current.oldLines, text)
			}
		default:
			// Preserve the established App Studio leniency for omitted
			// context markers. The immutable preflight still verifies this
			// exact line before any mutation is allowed.
			if current == nil {
				current = &patchChunk{}
			}
			current.oldLines = append(current.oldLines, line)
			current.newLines = append(current.newLines, line)
		}
		lineIndex++
	}
	flush()
	if len(operation.chunks) == 0 && operation.movePath == "" {
		return patchOperation{}, start, newPatchError(PatchErrorInvalidPatch, filePath, 0, 0, "Update File requires at least one hunk or Move to")
	}
	return operation, lineIndex, nil
}

func parsePatchMarkerPath(line, marker string, lineNumber int) (string, error) {
	trimmed := strings.TrimSpace(line)
	rawPath := strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
	if rawPath == "" || rawPath == trimmed {
		return "", invalidPatchLine(lineNumber, fmt.Sprintf("%s requires a relative path", strings.TrimSpace(marker)))
	}
	clean, err := cleanProjectPath(rawPath)
	if err != nil {
		return "", newPatchError(PatchErrorInvalidPatch, rawPath, 0, 0, "invalid path on line %d: %v", lineNumber, err)
	}
	return clean, nil
}

func invalidPatchLine(lineNumber int, message string) *PatchError {
	return newPatchError(PatchErrorInvalidPatch, "", 0, 0, "line %d: %s", lineNumber, message)
}

// patchOperationMarker classifies a marker after trimming both sides. It is
// used only where Codex treats a line as a structural header (envelope, Add,
// and Delete states). Update hunk bodies deliberately use the stricter
// isPatchFileMarkerInUpdate helper so indented marker-looking source remains
// literal context.
func patchOperationMarker(line string) string {
	for _, marker := range []string{patchAddMarker, patchDeleteMarker, patchUpdateMarker} {
		if strings.HasPrefix(line, marker) {
			return marker
		}
	}
	return ""
}

func isPatchFileMarker(line string) bool {
	return patchOperationMarker(strings.TrimSpace(line)) != ""
}

func isPatchFileMarkerInUpdate(line string) bool {
	return patchOperationMarker(strings.TrimRight(line, " \t")) != ""
}

func isPatchMarkerWithTrailingWhitespace(line, marker string) bool {
	return strings.HasPrefix(strings.TrimRight(line, " \t"), marker)
}

func validatePatchOperationPaths(operations []patchOperation) error {
	touched := make(map[string]string, len(operations)*2)
	for _, operation := range operations {
		if previous, exists := touched[operation.path]; exists {
			return newPatchError(PatchErrorInvalidPatch, operation.path, 0, 0, "path is touched more than once (%s and another operation)", previous)
		}
		touched[operation.path] = "source"
		if operation.movePath == "" {
			continue
		}
		if operation.movePath == operation.path {
			return newPatchError(PatchErrorNoChanges, operation.path, 0, 0, "Move to path is the same as the source path")
		}
		if previous, exists := touched[operation.movePath]; exists {
			return newPatchError(PatchErrorInvalidPatch, operation.movePath, 0, 0, "move destination is also used as a %s path", previous)
		}
		touched[operation.movePath] = "destination"
	}
	return nil
}
