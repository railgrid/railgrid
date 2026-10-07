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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// Eino's agenticopenai v0.2.3 drops Reasoning.EncryptedContent on output_item.done and emits
// only response metadata on response.completed. Its non-streaming converter
// preserves the same field. It also reduces structured error events to plain
// errors, losing the code needed for retry and context-compaction decisions.
// Observe opaque continuation metadata and structured errors until the native
// converter preserves them; Eino still owns model output, SSE parsing, tool
// conversion, and completion.
// https://github.com/cloudwego/eino-ext/blob/components/model/agenticopenai/v0.2.3/components/model/agenticopenai/responses_event_convertor.go
// The bound matches openai-go's SSE scanner limit (64 KiB << 9).
const projectEinoResponsesMetadataLimit = 32 << 20

type projectEinoResponsesStreamMetadataKey struct{}

type projectEinoResponsesTransport struct {
	base http.RoundTripper
}

func (t *projectEinoResponsesTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	metadata, _ := request.Context().Value(projectEinoResponsesStreamMetadataKey{}).(*projectEinoResponsesStreamMetadata)
	if metadata != nil && response.Body != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		response.Body = &projectEinoResponsesMetadataBody{ReadCloser: response.Body, metadata: metadata}
	}
	return response, nil
}

type projectEinoResponsesMetadataBody struct {
	io.ReadCloser
	metadata *projectEinoResponsesStreamMetadata
}

func (b *projectEinoResponsesMetadataBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if captureErr := b.metadata.consume(p[:n]); captureErr != nil {
		return n, captureErr
	}
	return n, err
}

type projectEinoResponsesStreamMetadata struct {
	mu         sync.Mutex
	line       []byte
	data       []byte
	reasoning  []projectEinoResponsesOpaqueReasoning
	totalBytes int
	err        error
	failure    *projectEinoResponsesFailureError
}

func (m *projectEinoResponsesStreamMetadata) consume(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		count := len(data)
		if end >= 0 {
			count = end
		}
		if len(m.line)+len(m.data)+count > projectEinoResponsesMetadataLimit {
			m.err = errors.New("OpenAI Responses streaming metadata exceeds the event limit")
			return m.err
		}
		m.line = append(m.line, data[:count]...)
		if end < 0 {
			break
		}
		line := bytes.TrimSuffix(m.line, []byte{'\r'})
		switch {
		case len(line) == 0:
			m.captureEvent()
			m.data = m.data[:0]
		case bytes.HasPrefix(line, []byte("data:")):
			value := bytes.TrimPrefix(line[len("data:"):], []byte{' '})
			m.data = append(m.data, value...)
			m.data = append(m.data, '\n')
		}
		m.line = m.line[:0]
		if m.err != nil {
			return m.err
		}
		data = data[end+1:]
	}
	return nil
}

func (m *projectEinoResponsesStreamMetadata) captureEvent() {
	if !bytes.Contains(m.data, []byte("encrypted_content")) && !bytes.Contains(m.data, []byte("\"code\"")) {
		return
	}
	type item struct {
		Type             string `json:"type"`
		ID               string `json:"id"`
		EncryptedContent string `json:"encrypted_content"`
	}
	var event struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Item     item `json:"item"`
		Response struct {
			Output []item `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(m.data, &event) != nil {
		// The native decoder reports invalid events; this observer never repairs
		// or accepts malformed model output on its behalf.
		return
	}
	if event.Type == "error" && event.Code != "" {
		m.failure = &projectEinoResponsesFailureError{Code: event.Code, Message: event.Message}
	} else if (event.Type == "" || event.Type == "error") && event.Error != nil && event.Error.Code != "" {
		m.failure = &projectEinoResponsesFailureError{Code: event.Error.Code, Message: event.Error.Message}
	}
	var items []item
	switch event.Type {
	case "response.output_item.done":
		items = []item{event.Item}
	case "response.completed":
		items = event.Response.Output
	}
	for _, item := range items {
		if item.Type != "reasoning" || item.ID == "" || item.EncryptedContent == "" {
			continue
		}
		index := len(m.reasoning)
		for i, previous := range m.reasoning {
			if previous.ID == item.ID {
				index = i
				m.totalBytes -= len(previous.ID) + len(previous.EncryptedContent)
				break
			}
		}
		m.totalBytes += len(item.ID) + len(item.EncryptedContent)
		if m.totalBytes > projectEinoResponsesMetadataLimit {
			m.err = errors.New("OpenAI Responses encrypted continuation exceeds the metadata limit")
			return
		}
		value := projectEinoResponsesOpaqueReasoning{ID: item.ID, EncryptedContent: item.EncryptedContent}
		if index == len(m.reasoning) {
			m.reasoning = append(m.reasoning, value)
		} else {
			m.reasoning[index] = value
		}
	}
}

func (m *projectEinoResponsesStreamMetadata) wrapError(err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure == nil || m.err != nil {
		return err
	}
	return &projectEinoResponsesFailureError{Code: m.failure.Code, Message: m.failure.Message, Err: err}
}

func (m *projectEinoResponsesStreamMetadata) restore(message *schema.AgenticMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	for _, item := range m.reasoning {
		found := false
		for _, block := range message.ContentBlocks {
			if block != nil && block.Reasoning != nil && projectEinoResponsesItemID(block) == item.ID {
				block.Reasoning.Signature = item.EncryptedContent
				found = true
				break
			}
		}
		if !found {
			message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
				Type: schema.ContentBlockTypeReasoning, Reasoning: &schema.Reasoning{Signature: item.EncryptedContent},
				Extra: map[string]any{"openai-item-id": item.ID},
			})
		}
	}
	return projectEinoResponsesValidateContinuation(message)
}
