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
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// streamRecord is the subset of one `--output-format stream-json` line this
// adapter reads. Unknown record types are ignored rather than rejected: the
// stream gains record types between Claude Code releases, and a runner that
// failed a turn because it saw a new one would be useless.
type streamRecord struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	IsError   bool            `json:"is_error"`
	NumTurns  int             `json:"num_turns"`
	Result    string          `json:"result"`
	Message   json.RawMessage `json:"message"`
	Model     string          `json:"model"`
}

// assistantMessage is the Anthropic message envelope carried by assistant and
// user records.
type assistantMessage struct {
	Role    string           `json:"role"`
	Content []messageContent `json:"content"`
}

type messageContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
	ID   string `json:"id"`
}

// runState accumulates what the stream said and turns it into one Result.
type runState struct {
	emit       harness.Emit
	sessionID  string
	credential credential

	// result is set by the terminal `result` record. Its absence after a clean
	// stream is itself a failure: a turn that produced no result is not a turn
	// that succeeded.
	result   *harness.Result
	lastText string
	total    int
}

// consume parses the stream line by line. Both the per-line and the total
// stream size are bounded: a harness that streams without end must not be able
// to exhaust the runner's memory.
func (s *runState) consume(stdout io.Reader) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxWireLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		s.total += len(line)
		if s.total > maxStreamBytes {
			return errors.New("the Claude Code event stream exceeded its oversized-stream bound")
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !utf8.Valid(line) {
			continue
		}
		var record streamRecord
		if err := json.Unmarshal(line, &record); err != nil {
			// A non-JSON line on stdout is noise from a wrapper script, not a
			// protocol violation. Dropping it is safer than failing the turn.
			continue
		}
		if err := s.handle(record, line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return errors.New("a Claude Code stream line exceeded its oversized-line bound")
		}
		return fmt.Errorf("reading the Claude Code event stream: %w", s.credential.redactError(err))
	}
	return nil
}

func (s *runState) handle(record streamRecord, raw []byte) error {
	if err := s.observeSessionID(record.SessionID); err != nil {
		return err
	}
	switch record.Type {
	case "system":
		if record.Subtype != "init" {
			return nil
		}
		return s.emitEvent(harness.Event{
			Type:      "session",
			SessionID: s.sessionID,
			Message:   "Claude Code session ready",
			Data:      s.boundedData(raw),
		})
	case "assistant":
		return s.handleAssistant(record)
	case "result":
		return s.handleResult(record, raw)
	default:
		return nil
	}
}

// handleAssistant emits the model's prose and a NAME-ONLY summary of each tool
// use. Tool inputs are deliberately not forwarded: they routinely contain file
// contents and command lines, and an event stream is replayed to callers.
func (s *runState) handleAssistant(record streamRecord) error {
	var message assistantMessage
	if len(record.Message) == 0 || json.Unmarshal(record.Message, &message) != nil {
		return nil
	}
	for _, content := range message.Content {
		switch content.Type {
		case "text":
			text := strings.TrimSpace(content.Text)
			if text == "" {
				continue
			}
			s.lastText = text
			if err := s.emitEvent(harness.Event{
				Type:      "message",
				SessionID: s.sessionID,
				Message:   boundedText(redact(text, s.credential)),
			}); err != nil {
				return err
			}
		case "tool_use":
			name := strings.TrimSpace(content.Name)
			if name == "" {
				name = "tool"
			}
			if err := s.emitEvent(harness.Event{
				Type:      "tool_use",
				SessionID: s.sessionID,
				Message:   boundedText(redact(name, s.credential)),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// handleResult maps the terminal record. The session id is carried in every
// outcome so a caller can resume instead of starting a second conversation.
func (s *runState) handleResult(record streamRecord, raw []byte) error {
	final := strings.TrimSpace(record.Result)
	if final == "" {
		final = s.lastText
	}
	result := harness.Result{SessionID: s.sessionID}

	switch {
	case record.IsError || record.Subtype == "error_during_execution":
		result.Phase = "failed"
		result.Blocker = boundedText(redact(firstNonEmpty(final, "Claude Code reported an error"), s.credential))
	case record.Subtype == "error_max_turns":
		// Not a failure of the work: the turn budget ran out and the session
		// can be resumed, which is what needs_input means here.
		result.Phase = "needs_input"
		result.Blocker = "Claude Code reached its turn limit before finishing"
	default:
		result.Phase = "completed"
	}

	// A clarification marker in the final message outranks "completed": the
	// model is asking, not answering.
	if clarification := parseClarification(s.sessionID, final); clarification != nil && result.Phase == "completed" {
		result.Phase = "needs_input"
		result.Clarification = clarification
		result.Blocker = "Claude Code asked a product question"
		if err := s.emitEvent(harness.Event{
			Type:          "needs_input",
			SessionID:     s.sessionID,
			Message:       "Claude Code asked a product question",
			Clarification: clarification,
		}); err != nil {
			return err
		}
	}

	s.result = &result
	return s.emitEvent(harness.Event{
		Type:      "result",
		SessionID: s.sessionID,
		Message:   boundedText(redact(final, s.credential)),
		Data:      s.boundedData(raw),
	})
}

// observeSessionID pins the session identity. A record naming a different
// session than the one we resumed is a protocol violation, not a surprise to
// absorb: it would mean the turn ran against somebody else's conversation.
func (s *runState) observeSessionID(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if s.sessionID == "" {
		s.sessionID = sessionID
		return nil
	}
	if s.sessionID != sessionID {
		return fmt.Errorf("a Claude Code event referenced foreign session %q", sessionID)
	}
	return nil
}

func (s *runState) emitEvent(event harness.Event) error {
	if s.emit == nil {
		return nil
	}
	return s.emit(event)
}

// terminalResult returns the parsed result, defaulting the session id.
func (s *runState) terminalResult() harness.Result {
	result := *s.result
	if result.SessionID == "" {
		result.SessionID = s.sessionID
	}
	return result
}

// boundedData forwards the raw record when it is small enough to be useful and
// a size marker when it is not. The credential cannot appear in a record the
// harness produced, but it is redacted anyway — cheap, and the alternative is
// reasoning about every future record type.
func (s *runState) boundedData(raw []byte) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if len(raw) > maxEventData {
		return json.RawMessage(fmt.Sprintf(`{"truncated":true,"bytes":%d}`, len(raw)))
	}
	cleaned := redact(string(raw), s.credential)
	if !json.Valid([]byte(cleaned)) {
		return json.RawMessage(`{"redacted":true}`)
	}
	return json.RawMessage(cleaned)
}

// failed builds a failure Result that still carries the session id, so a caller
// can inspect or resume the conversation that failed.
func failed(state *runState, err error) harness.Result {
	return harness.Result{
		Phase:     "failed",
		SessionID: state.sessionID,
		Blocker:   boundedText(redact(state.credential.redactError(err).Error(), state.credential)),
	}
}

func boundedText(text string) string {
	if len(text) <= maxMessageText {
		return text
	}
	// Cut on a rune boundary so the message stays valid UTF-8.
	cut := maxMessageText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…[truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
