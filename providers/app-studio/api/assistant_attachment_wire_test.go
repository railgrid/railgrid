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

package api

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
)

type assistantAttachmentCaptureTransport struct {
	body []byte
}

func (t *assistantAttachmentCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var err error
	t.body, err = io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"id":"response-1","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"I can see it."},"finish_reason":"stop"}],"usage":{}}`)),
		Request:    request,
	}, nil
}

func TestProjectEinoAssistantOpenAIWirePayloadCarriesImageDataURL(t *testing.T) {
	capture := &assistantAttachmentCaptureTransport{}
	base, err := openaimodel.NewChatModel(context.Background(), &openaimodel.ChatModelConfig{
		APIKey:     "test-key",
		BaseURL:    "https://llm.example.test/v1",
		Model:      "test-model",
		HTTPClient: &http.Client{Transport: capture},
	})
	if err != nil {
		t.Fatalf("create OpenAI model: %v", err)
	}
	model := &projectEinoAssistantOpenAIPayloadModel{BaseChatModel: base}
	encoded := base64.StdEncoding.EncodeToString([]byte("PNG bytes"))
	message := schema.UserMessage("")
	message.UserInputMultiContent = []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "The user attached image \"screen.png\"."},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &encoded, MIMEType: "image/png"}}},
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{message}); err != nil {
		t.Fatalf("generate OpenAI request: %v", err)
	}
	if len(capture.body) == 0 {
		t.Fatal("OpenAI transport did not capture a request body")
	}
	want := "data:image/png;base64," + encoded
	if !strings.Contains(string(capture.body), want) {
		t.Fatalf("OpenAI wire payload omitted image data URL %q: %s", want, capture.body)
	}
}
