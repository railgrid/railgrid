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

package runner

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (r *Runner) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if !r.authorized(request) {
		writeHTTPError(w, http.StatusUnauthorized, protocolError(ErrorUnauthorized, false, "runner bearer authentication required", nil))
		return
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case request.Method == http.MethodGet && path == "/runner/v1/capabilities":
		writeJSON(w, http.StatusOK, r.Capabilities())
	case request.Method == http.MethodPost && path == "/runner/v1/attempts":
		var body StartRequest
		if err := decodeJSONBody(request, &body, r.cfg.MaxBodyBytes); err != nil {
			writeHTTPError(w, http.StatusBadRequest, protocolError(ErrorInvalidRequest, false, err.Error(), nil))
			return
		}
		receipt, err := r.Start(request.Context(), body)
		if err != nil {
			writeRunnerError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, receipt)
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/runner/v1/attempts/"):
		r.serveAttemptGet(w, request, path)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/runner/v1/attempts/"):
		r.serveAttemptMutation(w, request, path)
	default:
		writeHTTPError(w, http.StatusNotFound, protocolError(ErrorUnavailable, false, "runner route not found", nil))
	}
}

func (r *Runner) authorized(request *http.Request) bool {
	value := request.Header.Get("Authorization")
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return tokenEqual(r.cfg.Token, parts[1])
}

func (r *Runner) serveAttemptGet(w http.ResponseWriter, request *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/runner/v1/attempts/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		receipt, err := r.Inspect(request.Context(), parts[0])
		if err != nil {
			writeRunnerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, receipt)
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "events" {
		r.serveEvents(w, request, parts[0])
		return
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "artifacts" && parts[2] != "" {
		r.serveArtifact(w, request, parts[0], parts[2])
		return
	}
	writeHTTPError(w, http.StatusNotFound, protocolError(ErrorUnavailable, false, "runner route not found", nil))
}

func (r *Runner) serveAttemptMutation(w http.ResponseWriter, request *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/runner/v1/attempts/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeHTTPError(w, http.StatusNotFound, protocolError(ErrorUnavailable, false, "runner route not found", nil))
		return
	}
	var (
		receipt Receipt
		err     error
	)
	switch parts[1] {
	case "cancel":
		var body CancelRequest
		if decodeErr := decodeJSONBody(request, &body, r.cfg.MaxBodyBytes); decodeErr != nil {
			writeHTTPError(w, http.StatusBadRequest, protocolError(ErrorInvalidRequest, false, decodeErr.Error(), nil))
			return
		}
		if body.AttemptID != parts[0] {
			writeHTTPError(w, http.StatusBadRequest, protocolError(ErrorInvalidRequest, false, "attemptID must match the URL", nil))
			return
		}
		receipt, err = r.Cancel(request.Context(), body)
	case "resume":
		var body ResumeRequest
		if decodeErr := decodeJSONBody(request, &body, r.cfg.MaxBodyBytes); decodeErr != nil {
			writeHTTPError(w, http.StatusBadRequest, protocolError(ErrorInvalidRequest, false, decodeErr.Error(), nil))
			return
		}
		if body.AttemptID != parts[0] {
			writeHTTPError(w, http.StatusBadRequest, protocolError(ErrorInvalidRequest, false, "attemptID must match the URL", nil))
			return
		}
		receipt, err = r.Resume(request.Context(), body)
	default:
		writeHTTPError(w, http.StatusNotFound, protocolError(ErrorUnavailable, false, "runner route not found", nil))
		return
	}
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	status := http.StatusAccepted
	if receipt.Phase.IsTerminal() {
		status = http.StatusOK
	}
	writeJSON(w, status, receipt)
}

func (r *Runner) serveEvents(w http.ResponseWriter, request *http.Request, attemptID string) {
	after, err := parseCursor(request.URL.Query().Get("after"))
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, protocolError(ErrorInvalidRequest, false, err.Error(), nil))
		return
	}
	r.mu.Lock()
	attempt, ok := r.state.Attempts[attemptID]
	if !ok {
		r.mu.Unlock()
		writeHTTPError(w, http.StatusNotFound, protocolError(ErrorUnavailable, false, "attempt not found", nil))
		return
	}
	events := append([]Event(nil), r.state.Events[attemptID]...)
	if err := cursorGap(events, after); err != nil {
		receipt := receiptForResponse(attempt.Receipt)
		r.mu.Unlock()
		err.Receipt = &receipt
		status := statusForCode(err.Code)
		if err.Code == ErrorCursorExpired {
			err.SnapshotRequired = true
		}
		writeHTTPError(w, status, err)
		return
	}
	updates := make(chan Event, 64)
	if r.subscribers[attemptID] == nil {
		r.subscribers[attemptID] = map[chan Event]struct{}{}
	}
	r.subscribers[attemptID][updates] = struct{}{}
	terminal := attempt.Receipt.Phase.IsTerminal()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.subscribers[attemptID], updates)
		r.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	last := after
	for _, event := range events {
		if event.Cursor <= after {
			continue
		}
		if err := writeSSE(w, event); err != nil {
			return
		}
		last = event.Cursor
		if flusher != nil {
			flusher.Flush()
		}
	}
	if terminal {
		return
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-timer.C:
			return
		case event := <-updates:
			if event.Cursor <= last {
				continue
			}
			if err := writeSSE(w, event); err != nil {
				return
			}
			last = event.Cursor
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (r *Runner) serveArtifact(w http.ResponseWriter, request *http.Request, attemptID, artifactID string) {
	r.mu.Lock()
	attempt, attemptOK := r.state.Attempts[attemptID]
	artifact, ok := r.state.Artifacts[artifactID]
	if !attemptOK || !ok || artifact.Artifact.ID != artifactID || !artifactBelongs(attempt, artifact.Artifact) {
		r.mu.Unlock()
		writeHTTPError(w, http.StatusNotFound, protocolError(ErrorUnavailable, false, "artifact not found", nil))
		return
	}
	path := artifact.Path
	metadata := artifact.Artifact
	stateDir := r.cfg.StateDir
	r.mu.Unlock()
	if !pathWithin(stateDir, path) {
		writeHTTPError(w, http.StatusGone, protocolError(ErrorUnavailable, true, "artifact path is outside runner state", nil))
		return
	}
	contents, err := readImmutableArtifact(path, metadata)
	if err != nil {
		writeHTTPError(w, http.StatusGone, protocolError(ErrorUnavailable, true, "artifact is unavailable", nil))
		return
	}
	w.Header().Set("Content-Type", metadata.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Length, 10))
	w.Header().Set("Digest", metadata.Digest)
	w.Header().Set("ETag", fmt.Sprintf("%q", metadata.Digest))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

func readImmutableArtifact(path string, metadata Artifact) ([]byte, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	contents, err := io.ReadAll(io.LimitReader(file, metadata.Length+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) != metadata.Length {
		return nil, errors.New("artifact length changed")
	}
	digest := sha256.Sum256(contents)
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != metadata.Digest {
		return nil, errors.New("artifact digest changed")
	}
	return contents, nil
}

func artifactBelongs(attempt *attemptRecord, artifact Artifact) bool {
	for _, item := range attempt.Receipt.Artifacts {
		if item.ID == artifact.ID && item.Digest == artifact.Digest && item.Length == artifact.Length {
			return true
		}
	}
	return false
}

func writeSSE(w http.ResponseWriter, event Event) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(w)
	if _, err := fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Cursor, event.Type, b); err != nil {
		return err
	}
	return writer.Flush()
}

func parseCursor(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("after must be a nonnegative event cursor")
	}
	return cursor, nil
}

func cursorGap(events []Event, after uint64) *Error {
	if len(events) == 0 {
		return nil
	}
	first := events[0].Cursor
	last := events[len(events)-1].Cursor
	if after > last {
		return &Error{Code: ErrorInvalidRequest, Retryable: false, Message: "after cursor is ahead of the attempt"}
	}
	if after+1 < first {
		return &Error{Code: ErrorCursorExpired, Retryable: true, Message: "event cursor expired; inspect the attempt before replaying"}
	}
	return nil
}

func decodeJSONBody(request *http.Request, target any, maxBytes int) error {
	if request.Body == nil {
		return errors.New("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, int64(maxBytes)+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxBytes {
		return errors.New("request body exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return fmt.Errorf("decode trailing request body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeHTTPError(w http.ResponseWriter, status int, err *Error) {
	writeJSON(w, status, err)
}

func writeRunnerError(w http.ResponseWriter, err error) {
	var protocolErr *Error
	if errors.As(err, &protocolErr) {
		status := statusForCode(protocolErr.Code)
		writeHTTPError(w, status, protocolErr)
		return
	}
	writeHTTPError(w, http.StatusInternalServerError, protocolError(ErrorUnavailable, true, err.Error(), nil))
}

func statusForCode(code string) int {
	switch code {
	case ErrorUnauthorized:
		return http.StatusUnauthorized
	case ErrorForbidden:
		return http.StatusForbidden
	case ErrorBusy:
		return http.StatusConflict
	case ErrorStaleAttempt, ErrorIdempotencyConflict:
		return http.StatusConflict
	case ErrorCheckpointUnavailable:
		return http.StatusConflict
	case ErrorCursorExpired:
		return http.StatusGone
	case ErrorUnsupportedCapability, ErrorUnsupportedVersion:
		return http.StatusBadRequest
	case ErrorUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}
