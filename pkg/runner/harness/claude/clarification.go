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
// its final message must END with one delimited block:
//
//	<<<RAILGRID_CLARIFICATION>>>
//	the question
//	<<<END_RAILGRID_CLARIFICATION>>>
//
// The block is recognized only in the FINAL message of a turn, only once, and
// only when it CLOSES that message — nothing but whitespace may follow the
// closer. Prose BEFORE the block is allowed and ignored: models routinely
// explain why they are stuck and only then ask, and dropping those questions
// left the human with neither an answer nor a question. Only the text between
// the delimiters is the question; the explanation is not part of it, and
// harness.Clarification has nowhere to carry it.
//
// Everything else is still refused. Ordinary prose that happens to discuss a
// question is never a clarification; a second opener or closer anywhere in the
// message means the model wrote about the marker rather than emitting it; and
// a block in the MIDDLE of a message is ignored, because the turn continued
// past it and therefore did not stop to ask.
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
	"end your turn with exactly one block, as the very last thing in your message:\n" +
	clarificationOpen + "\n<your question>\n" + clarificationClose + "\n" +
	"You may explain the situation before the block, but write nothing after it, use the block only once, " +
	"and put the whole question inside it — only that text reaches the human.\n" +
	"Do not use that block for anything else, and do not use it to report progress or ask for permission.\n\n" +
	"Task:\n"

// parseClarification recognizes the marker at the end of a turn's final
// message. It returns nil for anything that is not exactly one well-formed,
// bounded block closing that message, which is the safe direction: a false
// clarification would park work forever on a question nobody asked.
func parseClarification(sessionID, final string) *harness.Clarification {
	text := strings.TrimSpace(final)
	// Exactly one block: a second opener or closer anywhere means the model
	// emitted prose containing the marker rather than the marker itself.
	if strings.Count(text, clarificationOpen) != 1 || strings.Count(text, clarificationClose) != 1 {
		return nil
	}
	open := strings.Index(text, clarificationOpen)
	bodyStart := open + len(clarificationOpen)
	closer := strings.Index(text, clarificationClose)
	if closer < bodyStart {
		return nil
	}
	// The block must END the turn. A block followed by more work means the
	// model kept going, so it was not waiting on an answer.
	if strings.TrimSpace(text[closer+len(clarificationClose):]) != "" {
		return nil
	}
	// The question is what is between the delimiters. Whatever the model wrote
	// before the opener is explanation, not the question.
	question := strings.TrimSpace(text[bodyStart:closer])
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
