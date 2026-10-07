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
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type projectResponsesFixtureRoundTripper func(*http.Request) (*http.Response, error)

func (f projectResponsesFixtureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type projectResponsesFixtureBody struct {
	*strings.Reader
	closed bool
}

func (b *projectResponsesFixtureBody) Close() error {
	b.closed = true
	return nil
}

func TestProjectResponsesStreamMetadataIsRequestScopedAndPreservesBody(t *testing.T) {
	const wire = ": comment\r\nevent: response.output_item.done\r\ndata: {\"type\":\"response.output_item.done\",\r\ndata: \"item\":{\"type\":\"reasoning\",\"id\":\"rs_%s\",\"encrypted_content\":\"old_%s\",\"summary\":[{\"text\":\"private summary\"}]}}\r\n\r\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"reasoning\",\"id\":\"rs_%s\",\"encrypted_content\":\"signature_%s\"}]}}\r\n\r\n"
	firstWire := fmt.Sprintf(wire, "first", "first", "first", "first")
	secondWire := fmt.Sprintf(wire, "second", "second", "second", "second")
	sources := map[string]*projectResponsesFixtureBody{
		"/first":  {Reader: strings.NewReader(firstWire)},
		"/second": {Reader: strings.NewReader(secondWire)},
	}
	transport := &projectEinoResponsesTransport{base: projectResponsesFixtureRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: sources[req.URL.Path]}, nil
	})}
	metadata := []*projectEinoResponsesStreamMetadata{{}, {}}
	responses := make([]*http.Response, 2)
	for i, path := range []string{"/first", "/second"} {
		ctx := context.WithValue(context.Background(), projectEinoResponsesStreamMetadataKey{}, metadata[i])
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.test"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		responses[i], err = transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	var received [2]strings.Builder
	done := [2]bool{}
	// Interleave reads from two requests one byte at a time. This forces every
	// possible frame/token boundary and proves continuation belongs to its request.
	for !done[0] || !done[1] {
		for i, response := range responses {
			if done[i] {
				continue
			}
			var value [1]byte
			n, err := response.Body.Read(value[:])
			if n > 0 {
				received[i].Write(value[:n])
			}
			if err == io.EOF {
				done[i] = true
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, name := range []string{"first", "second"} {
		if got, want := received[i].String(), []string{firstWire, secondWire}[i]; got != want {
			t.Fatalf("%s response body changed during observation", name)
		}
		message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
		if err := metadata[i].restore(message); err != nil {
			t.Fatal(err)
		}
		if len(message.ContentBlocks) != 1 {
			t.Fatalf("%s reasoning items = %d, want one after duplicate events", name, len(message.ContentBlocks))
		}
		block := message.ContentBlocks[0]
		if block.Reasoning == nil || block.Reasoning.Signature != "signature_"+name || block.Reasoning.Text != "" || projectEinoResponsesItemID(block) != "rs_"+name {
			t.Fatalf("%s recovered opaque reasoning = %#v", name, block)
		}
		if err := responses[i].Body.Close(); err != nil {
			t.Fatal(err)
		}
		if !sources["/"+name].closed {
			t.Fatalf("%s underlying HTTP body was not closed", name)
		}
	}
}
