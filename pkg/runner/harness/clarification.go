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

package harness

import "strings"

// The clarification block: how a harness says it has stopped to ask a human
// something. An adapter teaches its model the convention and parses the block
// back out; a CONSUMER needs the same two strings to take them out of the text
// it shows a person.
//
// They live here rather than in one adapter because they have two audiences.
// Keeping them private meant the question was extracted correctly into the
// runner's receipt while the raw markers still reached the transcript, which is
// what a person then read.
const (
	// ClarificationOpen and ClarificationClose delimit the question, and only
	// the text between them reaches the human.
	ClarificationOpen  = "<<<RAILGRID_CLARIFICATION>>>"
	ClarificationClose = "<<<END_RAILGRID_CLARIFICATION>>>"
)

// StripClarification removes a clarification block from text meant for a
// person, leaving whatever the model wrote around it.
//
// It is deliberately forgiving in a way the PARSER is not. The parser decides
// whether the harness is really waiting on an answer, so it refuses anything
// malformed rather than parking work on a question nobody asked. This only
// decides what a person reads, where a leftover marker is the worse outcome —
// so an unterminated opener takes the rest of the text with it, which is the
// question the model was in the middle of writing.
func StripClarification(text string) string {
	for {
		open := strings.Index(text, ClarificationOpen)
		if open < 0 {
			return strings.TrimSpace(text)
		}
		rest := text[open+len(ClarificationOpen):]
		closer := strings.Index(rest, ClarificationClose)
		if closer < 0 {
			return strings.TrimSpace(text[:open])
		}
		text = text[:open] + rest[closer+len(ClarificationClose):]
	}
}
