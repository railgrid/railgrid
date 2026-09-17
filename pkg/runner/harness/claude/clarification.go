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

package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// Headless Claude Code has NO structured "ask the user" channel. The Codex
// adapter's clarification support is specific to the app-server protocol's
// `item/tool/requestUserInput` request, which has no counterpart here — and
// this adapter runs with `--permission-prompts none`, so anything that would
// have prompted is denied rather than surfaced.
//
// So the convention is a marker, and it is a CONVENTION rather than a protocol:
// the adapter prepends promptPreamble to every turn's instructions, telling the
// model that if — and only if — it cannot proceed without a product decision,
// its final message must be exactly one delimited block:
//
//	<<<RAILGRID_CLARIFICATION>>>
//	the question
//	<<<END_RAILGRID_CLARIFICATION>>>
//
// The block is recognized only in the FINAL message of a turn, only when it is
// the whole message, and only once. Ordinary prose that happens to discuss a
// question is never a clarification; a model that emits the marker mid-answer
// and then keeps working is ignored, because the turn completed.
const (
	clarificationOpen  = "<<<RAILGRID_CLARIFICATION>>>"
	clarificationClose = "<<<END_RAILGRID_CLARIFICATION>>>"

	maxClarificationText = 8 << 10
)

// promptPreamble teaches the convention above. It is prepended to the caller's
// instructions on every turn — including resumes, where the model may have
// compacted the original — and is deliberately short: it competes for context
// with the actual task.
const promptPreamble = "Runner protocol: if you cannot proceed without a decision only a human can make, " +
	"end your turn with exactly one block, nothing before or after it:\n" +
	clarificationOpen + "\n<your question>\n" + clarificationClose + "\n" +
	"Do not use that block for anything else, and do not use it to report progress or ask for permission.\n\n" +
	"Task:\n"

// parseClarification recognizes the marker in a turn's final message. It
// returns nil for anything that is not exactly one well-formed, bounded block,
// which is the safe direction: a missed clarification completes the attempt,
// while a false one would park work forever on a question nobody asked.
func parseClarification(sessionID, final string) *harness.Clarification {
	text := strings.TrimSpace(final)
	if !strings.HasPrefix(text, clarificationOpen) || !strings.HasSuffix(text, clarificationClose) {
		return nil
	}
	body := strings.TrimSuffix(strings.TrimPrefix(text, clarificationOpen), clarificationClose)
	// Exactly one block: a second opener means the model emitted prose
	// containing the marker rather than the marker itself.
	if strings.Contains(body, clarificationOpen) || strings.Contains(body, clarificationClose) {
		return nil
	}
	question := strings.TrimSpace(body)
	if question == "" || len(question) > maxClarificationText || !utf8.ValidString(question) {
		return nil
	}
	return &harness.Clarification{
		ID:   stableClarificationID(sessionID, question),
		Text: question,
	}
}

// stableClarificationID makes a repeated identical question resolve to the same
// ID, so a replayed resume is recognized as the same one rather than a new one.
func stableClarificationID(sessionID, question string) string {
	digest := sha256.Sum256([]byte(sessionID + "\x00" + question))
	return "clarification-" + hex.EncodeToString(digest[:])
}
