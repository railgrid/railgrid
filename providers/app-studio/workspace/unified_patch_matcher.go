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
	"sort"
	"strings"
)

type textLines struct {
	lines       []string
	lineEnding  string
	finalEnding bool
}

func splitPatchText(content string) textLines {
	lineEnding := "\n"
	if strings.Contains(content, "\r\n") {
		lineEnding = "\r\n"
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	finalEnding := strings.HasSuffix(normalized, "\n")
	if finalEnding {
		normalized = strings.TrimSuffix(normalized, "\n")
	}
	if normalized == "" {
		return textLines{lineEnding: lineEnding, finalEnding: finalEnding}
	}
	return textLines{lines: strings.Split(normalized, "\n"), lineEnding: lineEnding, finalEnding: finalEnding}
}

func (t textLines) string() string {
	content := strings.Join(t.lines, t.lineEnding)
	if t.finalEnding {
		content += t.lineEnding
	}
	return content
}

type patchMatchTier uint8

const (
	patchMatchExact patchMatchTier = iota
	patchMatchTrailingWhitespace
	patchMatchWhitespace
	patchMatchUnicode
)

// findUniquePatchSequence mirrors Codex's seek strategy: try exact matching
// first, then trailing-whitespace, fully-trimmed, and punctuation-normalized
// matching. A looser tier is accepted only when it produces one match.
func findUniquePatchSequence(lines, pattern []string, start int, endOfFile bool) (int, int) {
	if start < 0 {
		start = 0
	}
	if len(pattern) == 0 || len(pattern) > len(lines) || start > len(lines)-len(pattern) {
		return -1, 0
	}
	for tier := patchMatchExact; tier <= patchMatchUnicode; tier++ {
		matches := []int{}
		first := start
		last := len(lines) - len(pattern)
		if endOfFile {
			first = last
		}
		for index := first; index <= last; index++ {
			if patchSequenceMatches(lines[index:index+len(pattern)], pattern, tier) {
				matches = append(matches, index)
			}
		}
		if len(matches) > 0 {
			if len(matches) == 1 {
				return matches[0], 1
			}
			return -1, len(matches)
		}
	}
	return -1, 0
}

func patchSequenceMatches(actual, expected []string, tier patchMatchTier) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		left, right := actual[index], expected[index]
		switch tier {
		case patchMatchTrailingWhitespace:
			left, right = strings.TrimRight(left, " \t"), strings.TrimRight(right, " \t")
		case patchMatchWhitespace:
			left, right = strings.TrimSpace(left), strings.TrimSpace(right)
		case patchMatchUnicode:
			left, right = normalizePatchPunctuation(left), normalizePatchPunctuation(right)
		}
		if left != right {
			return false
		}
	}
	return true
}

func normalizePatchPunctuation(value string) string {
	return strings.Map(func(char rune) rune {
		switch char {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			return '-'
		case '\u2018', '\u2019', '\u201a', '\u201b':
			return '\''
		case '\u201c', '\u201d', '\u201e', '\u201f':
			return '"'
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
			return ' '
		default:
			return char
		}
	}, strings.TrimSpace(value))
}

// applyPatchChunks accepts independently matchable hunks in any order. Models
// frequently group edits by concern instead of by source position; the patch
// language does not carry numeric coordinates, so requiring the caller to
// rediscover and reorder already-unique context only creates read/retry loops.
// Try the authored order first to preserve dependent-hunk semantics, then make
// one safe retry in source order when every hunk resolves uniquely against the
// unchanged file.
func applyPatchChunks(filePath, content string, chunks []patchChunk) (string, int, error) {
	next, changed, err := applyPatchChunksInOrder(filePath, content, chunks)
	if err == nil {
		return next, changed, err
	}
	next, changed, ok := applyIndependentPatchChunks(content, chunks)
	if !ok {
		return "", 0, err
	}
	return next, changed, nil
}

type locatedPatchChunk struct {
	chunk patchChunk
	start int
	end   int
	order int
}

func applyIndependentPatchChunks(content string, chunks []patchChunk) (string, int, bool) {
	text := splitPatchText(content)
	located := make([]locatedPatchChunk, 0, len(chunks))
	normalizedContext := false
	for index, chunk := range chunks {
		start := 0
		if chunk.anchor != "" {
			anchorIndex, matches := findUniquePatchSequence(text.lines, []string{chunk.anchor}, 0, false)
			if matches == 1 {
				start = anchorIndex + 1
			} else if matches == 0 || len(chunk.oldLines) == 0 {
				return "", 0, false
			}
			// A repeated literal anchor is only a navigation hint. Let the full
			// unchanged hunk body disambiguate it against the immutable source.
		}
		matchIndex := start
		switch {
		case len(chunk.oldLines) > 0:
			var matches int
			matchIndex, matches = findUniquePatchSequence(text.lines, chunk.oldLines, start, chunk.endOfFile)
			if matches == 0 && chunk.anchor != "" && stringSliceContains(chunk.oldLines, chunk.anchor) {
				// Some models repeat the literal anchor in the hunk body even
				// though the contract says not to. The body is authoritative
				// when it resolves uniquely against the original snapshot.
				matchIndex, matches = findUniquePatchSequence(text.lines, chunk.oldLines, 0, chunk.endOfFile)
				normalizedContext = matches == 1
			}
			if matches != 1 {
				return "", 0, false
			}
		case chunk.endOfFile:
			matchIndex = len(text.lines)
		case chunk.anchor == "":
			return "", 0, false
		}
		located = append(located, locatedPatchChunk{
			chunk: chunk,
			start: matchIndex,
			end:   matchIndex + len(chunk.oldLines),
			order: index,
		})
	}

	sort.SliceStable(located, func(left, right int) bool {
		return located[left].start < located[right].start
	})
	changedOrder := false
	for index := range located {
		if located[index].order != index {
			changedOrder = true
		}
		if index > 0 && located[index-1].end > located[index].start {
			return "", 0, false
		}
	}
	if !changedOrder && !normalizedContext {
		return "", 0, false
	}
	changedHunks := 0
	for index := len(located) - 1; index >= 0; index-- {
		item := located[index]
		next := make([]string, 0, len(text.lines)-len(item.chunk.oldLines)+len(item.chunk.newLines))
		next = append(next, text.lines[:item.start]...)
		next = append(next, item.chunk.newLines...)
		next = append(next, text.lines[item.end:]...)
		if !equalStringSlices(item.chunk.oldLines, item.chunk.newLines) {
			changedHunks++
		}
		text.lines = next
	}
	return text.string(), changedHunks, true
}

func stringSliceContains(values []string, candidate string) bool {
	for _, value := range values {
		if patchSequenceMatches([]string{value}, []string{candidate}, patchMatchUnicode) {
			return true
		}
	}
	return false
}

func applyPatchChunksInOrder(filePath, content string, chunks []patchChunk) (string, int, error) {
	text := splitPatchText(content)
	cursor := 0
	changedHunks := 0
	for chunkIndex, chunk := range chunks {
		hunkNumber := chunkIndex + 1
		if chunk.anchor != "" {
			anchorIndex, matches := findUniquePatchSequence(text.lines, []string{chunk.anchor}, cursor, false)
			switch {
			case matches == 0:
				return "", 0, withPatchErrorContext(
					newPatchError(PatchErrorContextNotFound, filePath, hunkNumber, 0, "hunk anchor %q was not found after line %d", chunk.anchor, cursor),
					chunk.anchor,
					patchActualLinesPreview(text.lines, cursor, 1),
				)
			case matches > 1 && len(chunk.oldLines) == 0:
				return "", 0, withPatchErrorContext(
					newPatchError(PatchErrorContextAmbiguous, filePath, hunkNumber, matches, "hunk anchor %q is not unique; include more context", chunk.anchor),
					chunk.anchor,
					patchActualLinesPreview(text.lines, cursor, 1),
				)
			case matches > 1:
				// Codex-style @@ anchors are navigation hints. When a literal
				// anchor repeats, the full unchanged body remains authoritative.
				anchorIndex = -1
			}
			if anchorIndex >= 0 {
				cursor = anchorIndex + 1
			}
		}
		if len(chunk.oldLines) == 0 && len(chunk.newLines) == 0 {
			continue
		}
		matchIndex := -1
		matches := 0
		if len(chunk.oldLines) == 0 {
			switch {
			case chunk.endOfFile:
				matchIndex, matches = len(text.lines), 1
			case chunk.anchor != "":
				matchIndex, matches = cursor, 1
			default:
				return "", 0, newPatchError(PatchErrorInvalidPatch, filePath, hunkNumber, 0, "an insertion requires context, an @@ anchor, or End of File")
			}
		} else {
			matchIndex, matches = findUniquePatchSequence(text.lines, chunk.oldLines, cursor, chunk.endOfFile)
		}
		switch {
		case matches == 0:
			return "", 0, withPatchErrorContext(newPatchError(
				PatchErrorContextNotFound,
				filePath,
				hunkNumber,
				0,
				"failed to find the expected lines after line %d:\n%s",
				cursor,
				patchExpectedLinesPreview(chunk.oldLines),
			), patchExpectedLinesPreview(chunk.oldLines), patchActualLinesPreview(text.lines, cursor, len(chunk.oldLines)))
		case matches > 1:
			return "", 0, withPatchErrorContext(
				newPatchError(PatchErrorContextAmbiguous, filePath, hunkNumber, matches, "hunk context matched %d locations; include more surrounding context or an @@ anchor", matches),
				patchExpectedLinesPreview(chunk.oldLines),
				patchActualLinesPreview(text.lines, cursor, len(chunk.oldLines)),
			)
		}
		next := make([]string, 0, len(text.lines)-len(chunk.oldLines)+len(chunk.newLines))
		next = append(next, text.lines[:matchIndex]...)
		next = append(next, chunk.newLines...)
		next = append(next, text.lines[matchIndex+len(chunk.oldLines):]...)
		if !equalStringSlices(chunk.oldLines, chunk.newLines) {
			changedHunks++
		}
		text.lines = next
		cursor = matchIndex + len(chunk.newLines)
	}
	return text.string(), changedHunks, nil
}

func patchExpectedLinesPreview(lines []string) string {
	const (
		maxLines     = 12
		maxLineRunes = 240
	)
	count := len(lines)
	if count > maxLines {
		count = maxLines
	}
	preview := make([]string, 0, count+1)
	for _, line := range lines[:count] {
		runes := []rune(line)
		if len(runes) > maxLineRunes {
			line = string(runes[:maxLineRunes]) + "…"
		}
		preview = append(preview, line)
	}
	if len(lines) > count {
		preview = append(preview, fmt.Sprintf("… (%d more lines)", len(lines)-count))
	}
	return strings.Join(preview, "\n")
}

func patchActualLinesPreview(lines []string, cursor, expectedLines int) string {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(lines) {
		cursor = len(lines)
	}
	count := max(expectedLines, 3)
	if count > 12 {
		count = 12
	}
	end := min(cursor+count, len(lines))
	return patchExpectedLinesPreview(lines[cursor:end])
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
