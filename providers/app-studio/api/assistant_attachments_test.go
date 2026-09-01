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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/faroshq/provider-app-studio/store"
)

func TestParseAttachmentDraftRejectsPermanentUpload(t *testing.T) {
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("X-Faros-Attachment-Draft", "false")
	if _, err := parseAttachmentDraft(request); err == nil || !strings.Contains(err.Error(), "permanent attachment upload is not supported") {
		t.Fatalf("permanent upload error = %v", err)
	}
}

type projectAssistantAttachmentReaderTestDouble struct {
	contents map[string][]byte
}

func (r projectAssistantAttachmentReaderTestDouble) ReadAttachment(_ context.Context, _ store.Scope, receipt projectAssistantAttachmentReceipt, _ string, offset int64, limit int) (projectAssistantAttachmentRead, error) {
	content := r.contents[receipt.ID]
	if offset >= int64(len(content)) {
		return projectAssistantAttachmentRead{Complete: true}, nil
	}
	end := min(int64(len(content)), offset+int64(limit))
	return projectAssistantAttachmentRead{
		Content:  append([]byte(nil), content[offset:end]...),
		Complete: end == int64(len(content)),
	}, nil
}

func attachmentReceiptForTest(id, filename, contentType string, content []byte) projectAssistantAttachmentReceipt {
	sum := sha256.Sum256(content)
	return projectAssistantAttachmentReceipt{
		ID: id, Filename: filename, ContentType: contentType, SizeBytes: int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]), CreatedAt: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC),
	}
}

func TestProjectAssistantAttachmentReceiptCanonicalizesPortalShape(t *testing.T) {
	var part projectAssistantContentPart
	if err := json.Unmarshal([]byte(`{"type":"attachment","attachment":{"id":"upload-1","filename":"notes.md","contentType":"TEXT/MARKDOWN","sizeBytes":5,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","createdAt":"2026-08-31T00:00:00Z"}}`), &part); err != nil {
		t.Fatalf("decode attachment: %v", err)
	}
	parts, _, derived, err := normalizeProjectAssistantContentParts([]projectAssistantContentPart{part}, nil, nil)
	if err != nil {
		t.Fatalf("normalize attachment: %v", err)
	}
	if len(parts) != 1 || parts[0].Attachment == nil || parts[0].Attachment.ContentType != "text/markdown" || derived != "[@attachment:upload-1]" {
		t.Fatalf("canonical attachment = %#v, derived=%q", parts, derived)
	}
	encoded, err := json.Marshal(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "filename", "contentType", "sizeBytes", "sha256", "createdAt"} {
		if !strings.Contains(string(encoded), `"`+key+`"`) {
			t.Fatalf("canonical receipt omitted %q: %s", key, encoded)
		}
	}
	if strings.Contains(string(encoded), "decoded") || strings.Contains(string(encoded), "sizeSet") {
		t.Fatalf("private receipt state leaked: %s", encoded)
	}
	if err := json.Unmarshal([]byte(`{"type":"attachment","attachment":{"id":"upload-1","filename":"x","contentType":"text/plain","sizeBytes":1,"sha256":"a","createdAt":"now","unknown":true}}`), &part); err == nil {
		t.Fatal("unknown attachment receipt field was accepted")
	}
}

func TestProjectAssistantAttachmentMessagesKeepImagesMultimodalAndTextBounded(t *testing.T) {
	image := []byte("PNG bytes")
	text := []byte("small text")
	large := []byte(strings.Repeat("x", projectAssistantAttachmentInlineTextMaxBytes+1))
	parts := []projectAssistantContentPart{
		projectAssistantContentPartAttachment(attachmentReceiptForTest("image-1", "screen.png", "image/png", image)),
		projectAssistantContentPartAttachment(attachmentReceiptForTest("text-1", "notes.txt", "text/plain", text)),
		projectAssistantContentPartAttachment(attachmentReceiptForTest("large-1", "large.txt", "text/plain", large)),
	}
	state := newProjectEinoAssistantRunState()
	state.SetContentParts(parts)
	req := projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{
			"image-1": image, "text-1": text, "large-1": large,
		}},
	}
	messages, err := projectAssistantAttachmentMessages(context.Background(), req, state)
	if err != nil {
		t.Fatalf("build attachment messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("attachment message count = %d, want image + inline text", len(messages))
	}
	if messages[0].Role != schema.User || len(messages[0].UserInputMultiContent) != 2 || messages[0].UserInputMultiContent[1].Image == nil {
		t.Fatalf("image message = %#v", messages[0])
	}
	if got := messages[0].UserInputMultiContent[1].Image.Base64Data; got == nil || *got != base64.StdEncoding.EncodeToString(image) {
		t.Fatalf("image bytes = %#v", got)
	}
	if !strings.Contains(messages[1].Content, string(text)) || strings.Contains(messages[1].Content, string(large)) {
		t.Fatalf("inline text projection = %q", messages[1].Content)
	}
	if projectEinoAssistantAttachmentMessage(messages[0]) == false || projectEinoAssistantAttachmentMessage(messages[1]) == false {
		t.Fatal("synthetic attachment messages were not marked")
	}
	if projected := projectEinoMessagesToChat(messages); len(projected) != 0 {
		t.Fatalf("synthetic attachment messages entered durable projection: %#v", projected)
	}
	// A second sample (the same shape used after a checkpoint/resume) gets a
	// fresh image message, while the first synthetic message is not retained in
	// the durable model history.
	second, err := projectAssistantAttachmentMessages(context.Background(), req, state)
	if err != nil || len(second) != 2 || len(second[0].UserInputMultiContent) != 2 {
		t.Fatalf("resume attachment messages = %#v, err=%v", second, err)
	}
}

func TestProjectAssistantImageReceiptsRemainAttachedToTheirConversationMessage(t *testing.T) {
	image := []byte("image")
	receipt := attachmentReceiptForTest("image-history", "screen.png", "image/png", image)
	conversation := []chatMessage{
		{Role: "user", Content: "what is in this screenshot?", Attachments: []projectAssistantAttachmentReceipt{receipt}},
		{Role: "assistant", Content: "I will inspect it."},
		{Role: "user", Content: "what changed?"},
	}
	messages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		t.Fatalf("project conversation to Eino: %v", err)
	}
	if len(messages) != 4 || messages[0].Content != conversation[0].Content || !projectEinoAssistantHistoricalAttachmentMessage(messages[1]) {
		t.Fatalf("conversation messages = %#v, want user, historical image placeholder, assistant, user", messages)
	}
	roundTrip := projectEinoMessagesToChat(messages)
	if len(roundTrip) != len(conversation) || len(roundTrip[0].Attachments) != 1 || roundTrip[0].Attachments[0].ID != receipt.ID {
		t.Fatalf("round-trip conversation = %#v, want receipt on original user message", roundTrip)
	}
}

func TestProjectEinoAssistantHistoricalImagesRemainAtOriginalPositionsAcrossLaterTurns(t *testing.T) {
	image := []byte("historical image bytes")
	receipt := attachmentReceiptForTest("image-original-position", "screen.png", "image/png", image)
	conversation := []chatMessage{
		{Role: "user", Content: "inspect this screenshot", Attachments: []projectAssistantAttachmentReceipt{receipt}},
		{Role: "assistant", Content: "I inspected the screenshot."},
		{Role: "user", Content: "now update the page"},
		{Role: "assistant", Content: "The page is updated."},
		{Role: "user", Content: "what else should we improve?"},
	}
	messages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		t.Fatalf("project conversation to Eino: %v", err)
	}
	state := &adk.ChatModelAgentState{Messages: messages}
	runState := newProjectEinoAssistantRunState()
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: image}},
	}, runState).(*projectEinoAssistantLifecycle)
	for boundary := 0; boundary < 2; boundary++ {
		if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
			t.Fatalf("model boundary %d: %v", boundary, err)
		}
		if len(state.Messages) != len(messages) || state.Messages[0].Content != conversation[0].Content {
			t.Fatalf("boundary %d moved the original user message: %#v", boundary, state.Messages)
		}
		if len(state.Messages[1].UserInputMultiContent) != 2 || state.Messages[1].UserInputMultiContent[1].Image == nil {
			t.Fatalf("boundary %d historical image input = %#v", boundary, state.Messages[1])
		}
		if got := state.Messages[1].UserInputMultiContent[1].Image.Base64Data; got == nil || *got != base64.StdEncoding.EncodeToString(image) {
			t.Fatalf("boundary %d historical image bytes = %#v", boundary, got)
		}
		if events := projectAssistantModelInputEvents(state.Messages, boundary+1); len(events) != 0 {
			t.Fatalf("boundary %d emitted progress for historical image: %#v", boundary, events)
		}
		if _, _, err := lifecycle.AfterModelRewriteState(context.Background(), state, nil); err != nil {
			t.Fatalf("strip boundary %d historical image: %v", boundary, err)
		}
		projected := projectEinoMessagesToChat(state.Messages)
		if len(projected) != len(conversation) || len(projected[0].Attachments) != 1 || projected[0].Attachments[0].ID != receipt.ID {
			t.Fatalf("boundary %d durable projection = %#v", boundary, projected)
		}
		encoded, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), string(image)) || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(image)) {
			t.Fatalf("boundary %d durable projection leaked image bytes: %s", boundary, encoded)
		}
	}
}

func TestProjectEinoAssistantHistoricalImagesDoNotUseCurrentTurnBound(t *testing.T) {
	image := []byte("historical image bytes")
	conversation := make([]chatMessage, 0, (projectAssistantCurrentImageMaxCount+1)*2)
	contents := make(map[string][]byte, projectAssistantCurrentImageMaxCount+1)
	for index := 0; index <= projectAssistantCurrentImageMaxCount; index++ {
		receipt := attachmentReceiptForTest(fmt.Sprintf("image-history-%d", index), "screen.png", "image/png", image)
		conversation = append(conversation,
			chatMessage{Role: "user", Content: fmt.Sprintf("inspect screenshot %d", index), Attachments: []projectAssistantAttachmentReceipt{receipt}},
			chatMessage{Role: "assistant", Content: "I inspected it."},
		)
		contents[receipt.ID] = image
	}
	messages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		t.Fatalf("project historical image conversation to Eino: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: contents},
	}, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: messages}
	if err := lifecycle.rehydrateAttachmentMessages(context.Background(), state); err != nil {
		t.Fatalf("rehydrate %d historical images: %v", projectAssistantCurrentImageMaxCount+1, err)
	}
	var rehydrated int
	for _, message := range state.Messages {
		if !projectEinoAssistantHistoricalAttachmentMessage(message) {
			continue
		}
		if len(message.UserInputMultiContent) != 2 || message.UserInputMultiContent[1].Image == nil {
			t.Fatalf("historical image placeholder was not rehydrated: %#v", message)
		}
		rehydrated++
	}
	if rehydrated != projectAssistantCurrentImageMaxCount+1 {
		t.Fatalf("rehydrated historical images = %d, want %d", rehydrated, projectAssistantCurrentImageMaxCount+1)
	}
}

func TestProjectEinoAssistantHistoricalAttachmentMetadataFailsClosed(t *testing.T) {
	receipt := attachmentReceiptForTest("image-malformed-history", "screen.png", "image/png", []byte("image"))
	message := projectAssistantAttachmentPlaceholderMessage(receipt)
	message.Extra[projectAssistantAttachmentMessageReceiptsKey] = []map[string]any{{"id": receipt.ID}}
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, newProjectEinoAssistantRunState()).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("inspect this"), message}}
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err == nil || !strings.Contains(err.Error(), "historical attachment receipt") {
		t.Fatalf("malformed historical receipt error = %v, want fail-closed validation", err)
	}
}

func TestProjectEinoAssistantCurrentImageStillReportsModelInputProgress(t *testing.T) {
	image := []byte("current image bytes")
	receipt := attachmentReceiptForTest("image-current-turn", "current.png", "image/png", image)
	conversation, err := projectChatMessagesToEino([]chatMessage{{
		Role: "user", Content: "inspect this new image", Attachments: []projectAssistantAttachmentReceipt{receipt},
	}})
	if err != nil {
		t.Fatalf("project current conversation to Eino: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	var events []projectAssistantModelInputEvent
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: image}},
		StreamCallbacks: projectAssistantStreamCallbacks{OnModelInput: func(event projectAssistantModelInputEvent) {
			events = append(events, event)
		}},
	}, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: conversation}
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("current image model boundary: %v", err)
	}
	for _, message := range state.Messages {
		if len(message.UserInputMultiContent) > 0 && message.Content != "" {
			t.Fatalf("rehydrated image message sets both Content and UserInputMultiContent: %#v", message)
		}
	}
	runState.NextModelCallOrdinal()
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{Messages: state.Messages})
	handler.OnEnd(ctx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("I can see it.", nil)})
	if len(events) != 2 || events[0].Status != "started" || events[1].Status != "completed" || events[0].ID != "image-input-"+receipt.ID {
		t.Fatalf("current image lifecycle events = %#v", events)
	}
}

func TestProjectEinoAssistantMultipleCurrentImagesEmitOneOrderedLifecycleWithoutHistoricalProgress(t *testing.T) {
	oldBytes := []byte("old image bytes")
	firstBytes := []byte("first current image bytes")
	secondBytes := []byte("second current image bytes")
	old := attachmentReceiptForTest("image-history-lifecycle", "old.png", "image/png", oldBytes)
	first := attachmentReceiptForTest("image-current-lifecycle-1", "first.png", "image/png", firstBytes)
	second := attachmentReceiptForTest("image-current-lifecycle-2", "second.png", "image/png", secondBytes)
	conversation, err := projectChatMessagesToEino([]chatMessage{
		{Role: "user", Content: "inspect the old image", Attachments: []projectAssistantAttachmentReceipt{old}},
		{Role: "assistant", Content: "I inspected it."},
		{Role: "user", Content: "compare these new images", Attachments: []projectAssistantAttachmentReceipt{first, second}},
	})
	if err != nil {
		t.Fatalf("project mixed image conversation: %v", err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{
		projectAssistantContentPartAttachment(first),
		projectAssistantContentPartAttachment(second),
	})
	var events []projectAssistantModelInputEvent
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{
			old.ID: oldBytes, first.ID: firstBytes, second.ID: secondBytes,
		}},
		StreamCallbacks: projectAssistantStreamCallbacks{OnModelInput: func(event projectAssistantModelInputEvent) {
			events = append(events, event)
		}},
	}, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: conversation}
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("mixed image model boundary: %v", err)
	}
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	callbackCtx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{Messages: state.Messages})
	handler.OnEnd(callbackCtx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("Compared them.", nil)})

	if len(events) != 4 {
		t.Fatalf("mixed image lifecycle events = %#v, want one start/end pair per current image", events)
	}
	wantIDs := []string{"image-input-" + first.ID, "image-input-" + second.ID, "image-input-" + first.ID, "image-input-" + second.ID}
	wantStatuses := []string{"started", "started", "completed", "completed"}
	for index, event := range events {
		if event.ID != wantIDs[index] || event.Status != wantStatuses[index] {
			t.Fatalf("mixed image event %d = %#v, want id %q/status %q", index, event, wantIDs[index], wantStatuses[index])
		}
		if event.Filename == old.Filename || strings.Contains(event.ID, old.ID) {
			t.Fatalf("historical image emitted lifecycle event: %#v", event)
		}
	}
}

func TestProjectEinoAssistantCompactionKeepsReceiptOnlyWithRetainedUserMessage(t *testing.T) {
	image := []byte("compaction image bytes")
	receipt := attachmentReceiptForTest("image-compaction", "compaction.png", "image/png", image)
	original, err := projectChatMessagesToEino([]chatMessage{
		{Role: "user", Content: "keep this screenshot context", Attachments: []projectAssistantAttachmentReceipt{receipt}},
		{Role: "assistant", Content: "not retained by user-only compaction"},
		{Role: "user", Content: "latest request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	retained := projectEinoAssistantRecentUserMessages(original, projectEinoAssistantHistoricalImageTokenEstimate+100)
	projected := projectEinoMessagesToChat(retained)
	if len(projected) != 2 || len(projected[0].Attachments) != 1 || projected[0].Attachments[0].ID != receipt.ID {
		t.Fatalf("retained compaction history = %#v, want receipt with original user message", projected)
	}
	retained = projectEinoAssistantRecentUserMessages(original, 1)
	projected = projectEinoMessagesToChat(retained)
	for _, message := range projected {
		if len(message.Attachments) != 0 {
			t.Fatalf("dropped user message retained attachment receipt: %#v", projected)
		}
	}
}

func TestProjectEinoAssistantCompactionDropsOldImagesAndKeepsCurrentTurnImage(t *testing.T) {
	oldReceipt := attachmentReceiptForTest("image-old-compaction", "old.png", "image/png", []byte("old"))
	currentReceipt := attachmentReceiptForTest("image-current-compaction", "current.png", "image/png", []byte("current"))
	messages, err := projectChatMessagesToEino([]chatMessage{
		{Role: "user", Content: "old image", Attachments: []projectAssistantAttachmentReceipt{oldReceipt}},
		{Role: "assistant", Content: "old response"},
		{Role: "user", Content: "current image", Attachments: []projectAssistantAttachmentReceipt{currentReceipt}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(currentReceipt)})
	filtered := projectEinoAssistantRetainCurrentCompactionImages(messages, runState)
	projected := projectEinoMessagesToChat(filtered)
	if len(projected) != 3 {
		t.Fatalf("compacted projection = %#v, want all text messages", projected)
	}
	if len(projected[0].Attachments) != 0 {
		t.Fatalf("old compacted image receipt survived: %#v", projected[0].Attachments)
	}
	if len(projected[2].Attachments) != 1 || projected[2].Attachments[0].ID != currentReceipt.ID {
		t.Fatalf("current image receipt = %#v, want %q", projected[2].Attachments, currentReceipt.ID)
	}
}

func TestProjectEinoAssistantCompactionFiltersMixedReceiptPlaceholderPerReceipt(t *testing.T) {
	old := attachmentReceiptForTest("image-mixed-old", "old.png", "image/png", []byte("old"))
	current := attachmentReceiptForTest("image-mixed-current", "current.png", "image/png", []byte("current"))
	text := attachmentReceiptForTest("text-mixed", "notes.txt", "text/plain", []byte("notes"))
	mixed := projectAssistantAttachmentPlaceholderMessage(old)
	mixed.Extra[projectAssistantAttachmentMessageReceiptsKey] = []projectAssistantAttachmentReceipt{old, current, text}
	messages := []*schema.Message{schema.UserMessage("mixed receipts"), mixed, schema.UserMessage("latest request")}
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(current)})
	filtered := projectEinoAssistantRetainCurrentCompactionImages(messages, runState)
	projected := projectEinoMessagesToChat(filtered)
	if len(projected) != 2 {
		t.Fatalf("mixed receipt compaction projection = %#v, want original user plus latest request", projected)
	}
	if len(projected[0].Attachments) != 2 || projected[0].Attachments[0].ID != current.ID || projected[0].Attachments[1].ID != text.ID {
		t.Fatalf("mixed receipt compaction attachments = %#v, want current image then text receipt", projected[0].Attachments)
	}
	for _, receipt := range projected[0].Attachments {
		if receipt.ID == old.ID {
			t.Fatalf("old image receipt survived mixed placeholder filtering: %#v", projected[0].Attachments)
		}
	}
}

func TestProjectAssistantModelImageBoundsDeduplicateAndFailClosed(t *testing.T) {
	image := []byte("image")
	duplicate := attachmentReceiptForTest("image-duplicate", "screen.png", "image/png", image)
	parts, err := projectAssistantAttachmentReceiptsForModel([]projectAssistantContentPart{
		projectAssistantContentPartAttachment(duplicate),
		projectAssistantContentPartAttachment(duplicate),
	})
	if err != nil || len(parts) != 1 || parts[0].ID != duplicate.ID {
		t.Fatalf("deduplicated image receipts = %#v, err=%v", parts, err)
	}
	overCount := make([]projectAssistantContentPart, 0, projectAssistantCurrentImageMaxCount+1)
	for index := 0; index <= projectAssistantCurrentImageMaxCount; index++ {
		receipt := attachmentReceiptForTest(fmt.Sprintf("image-%d", index), "screen.png", "image/png", image)
		overCount = append(overCount, projectAssistantContentPartAttachment(receipt))
	}
	if _, err := projectAssistantAttachmentReceiptsForModel(overCount); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("more than %d images", projectAssistantCurrentImageMaxCount)) {
		t.Fatalf("image count bounds error = %v", err)
	}
	overBytes := []projectAssistantContentPart{
		projectAssistantContentPartAttachment(attachmentReceiptForTest("image-big-1", "screen.png", "image/png", image)),
		projectAssistantContentPartAttachment(attachmentReceiptForTest("image-big-2", "screen.png", "image/png", image)),
	}
	overBytes[0].Attachment.SizeBytes = projectAssistantCurrentImageMaxBytes
	overBytes[1].Attachment.SizeBytes = 1
	if _, err := projectAssistantAttachmentReceiptsForModel(overBytes); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("image aggregate bounds error = %v", err)
	}

	state := newProjectEinoAssistantRunState()
	missing := attachmentReceiptForTest("image-missing", "missing.png", "image/png", image)
	state.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(missing)})
	_, err = projectAssistantAttachmentMessages(context.Background(), projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{}},
	}, state)
	if err == nil || !strings.Contains(err.Error(), "not returned as one complete bounded object") {
		t.Fatalf("missing retained bytes error = %v", err)
	}
}

func TestProjectEinoAssistantLifecycleRehydratesVerifiedImageAfterRewrite(t *testing.T) {
	oldImage := []byte("stale image")
	newImage := []byte("fresh image")
	receipt := attachmentReceiptForTest("image-rewrite", "screen.png", "image/png", newImage)
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	reader := projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: newImage}}
	staleBase64 := base64.StdEncoding.EncodeToString(oldImage)
	stale := schema.UserMessage("")
	stale.Extra = map[string]any{
		projectAssistantAttachmentMessageKindKey:     true,
		projectAssistantAttachmentMessageIDKey:       receipt.ID,
		projectAssistantAttachmentMessageFilenameKey: receipt.Filename,
	}
	stale.UserInputMultiContent = []schema.MessageInputPart{{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &staleBase64, MIMEType: receipt.ContentType}},
	}}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("authoritative context"), stale, schema.UserMessage("what is in the photo?"),
	}}
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: reader,
	}, runState).(*projectEinoAssistantLifecycle)
	if err := lifecycle.rehydrateAttachmentMessages(context.Background(), state); err != nil {
		t.Fatalf("rehydrate image message: %v", err)
	}
	var fresh *schema.Message
	for _, message := range state.Messages {
		if !projectEinoAssistantAttachmentMessage(message) {
			continue
		}
		if fresh != nil {
			t.Fatal("rehydration retained more than one synthetic image message")
		}
		fresh = message
	}
	if fresh == nil || len(fresh.UserInputMultiContent) != 2 || fresh.UserInputMultiContent[1].Image == nil || fresh.UserInputMultiContent[1].Image.Base64Data == nil {
		t.Fatalf("rehydrated image message = %#v", fresh)
	}
	if *fresh.UserInputMultiContent[1].Image.Base64Data != base64.StdEncoding.EncodeToString(newImage) {
		t.Fatalf("rehydrated image bytes = %q, want fresh bytes", *fresh.UserInputMultiContent[1].Image.Base64Data)
	}
	if *fresh.UserInputMultiContent[1].Image.Base64Data == staleBase64 {
		t.Fatal("rehydration retained stale image bytes")
	}
	if _, _, err := lifecycle.AfterModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("strip model-only image message: %v", err)
	}
	for _, message := range state.Messages {
		if projectEinoAssistantAttachmentMessage(message) {
			t.Fatal("model-only image message remained in Eino state after model response")
		}
	}
	projected := projectEinoMessagesToChat(state.Messages)
	if len(projected) != 2 || strings.Contains(fmt.Sprint(projected), staleBase64) || strings.Contains(fmt.Sprint(projected), string(newImage)) {
		t.Fatalf("durable projection leaked image bytes: %#v", projected)
	}
}

func TestProjectEinoAssistantLifecycleReportsImageRehydrateFailureBeforeModelCallback(t *testing.T) {
	image := []byte("image bytes")
	receipt := attachmentReceiptForTest("image-pre-callback-failure", "screen.png", "image/png", image)
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	var events []projectAssistantModelInputEvent
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{}},
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
		},
	}, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("what is in the image?")}}
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err == nil {
		t.Fatal("expected model-input rehydration failure")
	}
	if len(events) != 1 {
		t.Fatalf("pre-callback image failure events = %#v, want one", events)
	}
	event := events[0]
	if event.ID != "image-input-"+receipt.ID || event.Filename != receipt.Filename || event.ContentType != receipt.ContentType || event.Status != "failed" {
		t.Fatalf("pre-callback image failure event = %#v", event)
	}
	if event.Error != "image attachment could not be included in model input" || strings.Contains(event.Error, receipt.SHA256) {
		t.Fatalf("pre-callback image failure error = %q", event.Error)
	}
	// Re-entering the same failed boundary must not append another failure.
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err == nil {
		t.Fatal("second model-input rehydration unexpectedly succeeded")
	}
	if len(events) != 1 {
		t.Fatalf("repeated pre-callback image failure events = %#v, want one", events)
	}
}

func TestProjectEinoAssistantLifecycleReportsLaterRehydrateFailureAfterPriorImageWasViewed(t *testing.T) {
	image := []byte("image bytes")
	receipt := attachmentReceiptForTest("image-later-pre-callback-failure", "screen.png", "image/png", image)
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	reader := projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: image}}
	var events []projectAssistantModelInputEvent
	lifecycle := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{
		AttachmentReader: reader,
		StreamCallbacks: projectAssistantStreamCallbacks{
			OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
		},
	}, runState).(*projectEinoAssistantLifecycle)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("what is in the image?")}}
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err != nil {
		t.Fatalf("first model boundary: %v", err)
	}
	handler := newProjectEinoAssistantModelCallbackHandler(projectAssistantStreamCallbacks{
		OnModelInput: func(event projectAssistantModelInputEvent) { events = append(events, event) },
	}, runState, nil)
	ctx := handler.OnStart(context.Background(), nil, &einomodel.CallbackInput{Messages: state.Messages})
	handler.OnEnd(ctx, nil, &einomodel.CallbackOutput{Message: schema.AssistantMessage("I can see it.", nil)})
	if len(events) != 2 || events[0].Status != "started" || events[1].Status != "completed" {
		t.Fatalf("first image lifecycle events = %#v, want started then completed", events)
	}

	// The next lifecycle boundary runs rehydration before allocating its next
	// model-call ordinal. A prior callback's ordinal must not suppress this
	// boundary's failure evidence.
	lifecycle.req.AttachmentReader = projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{}}
	if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), state, nil); err == nil {
		t.Fatal("second model boundary unexpectedly succeeded")
	}
	if len(events) != 3 || events[2].Status != "failed" || events[2].ID != events[0].ID {
		t.Fatalf("second image lifecycle events = %#v, want a failed event for the same image", events)
	}
}

func TestProjectEinoAssistantInputMessagesKeepAttachmentBytesOutOfEinoRootInput(t *testing.T) {
	image := []byte("root input image")
	receipt := attachmentReceiptForTest("image-root-input", "screen.png", "image/png", image)
	runState := newProjectEinoAssistantRunState()
	runState.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	messages, err := projectEinoAssistantInputMessages(context.Background(), projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: image}},
		Conversation:     []chatMessage{{Role: "user", Content: "what is in the photo?"}},
	}, runState, false)
	if err != nil {
		t.Fatalf("build Eino root input: %v", err)
	}
	for _, message := range messages {
		if projectEinoAssistantAttachmentMessage(message) {
			t.Fatal("attachment bytes were placed in Eino root input; they must be added only at model boundary")
		}
	}
}

func TestProjectAssistantReadAttachmentToolReadsOnlySelectedBoundedText(t *testing.T) {
	// This is intentionally larger than the inline threshold: the model must
	// use read_attachment for large text, while the tool still returns only the
	// requested bounded range.
	content := []byte(strings.Repeat("0123456789", 4096))
	receipt := attachmentReceiptForTest("text-1", "notes.txt", "text/plain", content)
	state := newProjectEinoAssistantRunState()
	state.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	result, err := projectAssistantReadAttachmentTool(context.Background(), projectAssistantToolCallRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{"text-1": content}},
		AttachmentScope:  store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace"},
		RunState:         state,
		Arguments:        map[string]any{"attachmentID": "text-1", "offset": float64(2), "limit": float64(4)},
	})
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["content"] != "2345" || envelope["offset"] != float64(2) || envelope["complete"] != false {
		t.Fatalf("attachment read envelope = %#v", envelope)
	}
	_, err = projectAssistantReadAttachmentTool(context.Background(), projectAssistantToolCallRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{"text-1": content}},
		RunState:         newProjectEinoAssistantRunState(),
		Arguments:        map[string]any{"attachmentID": "text-1"},
	})
	if err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatalf("unselected attachment error = %v", err)
	}
}

func TestProjectAssistantHistoricalTextReceiptSurvivesCheckpointAndRemainsReadable(t *testing.T) {
	content := []byte(strings.Repeat("historical text ", 4096))
	receipt := attachmentReceiptForTest("text-history-resume", "notes.txt", "text/plain", content)
	conversation := []chatMessage{{Role: "user", Content: "inspect the notes", Attachments: []projectAssistantAttachmentReceipt{receipt}}}
	modelMessages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		t.Fatalf("project historical text receipt: %v", err)
	}
	if len(modelMessages) != 2 || modelMessages[1].Content != projectAssistantHistoricalTextAttachmentModelText(receipt) {
		t.Fatalf("historical text placeholder = %#v, want bounded attachment marker", modelMessages)
	}
	if !strings.Contains(modelMessages[1].Content, "call read_attachment") || !strings.Contains(modelMessages[1].Content, receipt.ID) {
		t.Fatalf("historical text placeholder = %q, want actionable tool guidance", modelMessages[1].Content)
	}
	checkpointMessages := projectEinoAssistantMessagesWithoutAttachments(modelMessages)
	encoded, err := json.Marshal(checkpointMessages)
	if err != nil {
		t.Fatalf("encode metadata-only checkpoint: %v", err)
	}
	if strings.Contains(string(encoded), string(content)) {
		t.Fatal("historical text bytes were inlined into checkpoint state")
	}
	resumedConversation := projectEinoMessagesToChat(checkpointMessages)
	if len(resumedConversation) != 1 || len(resumedConversation[0].Attachments) != 1 || resumedConversation[0].Attachments[0].ID != receipt.ID {
		t.Fatalf("resumed historical text conversation = %#v, want receipt on originating user", resumedConversation)
	}
	reader := projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: content}}
	request := projectAssistantRunRequest{AttachmentReader: reader, Conversation: resumedConversation}
	prompt := projectAssistantHistoricalTextAttachmentsPrompt(resumedConversation)
	if !strings.Contains(prompt, receipt.ID) || !strings.Contains(prompt, receipt.Filename) || !strings.Contains(prompt, "call read_attachment") {
		t.Fatalf("historical text live prompt = %q, want exact actionable receipt guidance", prompt)
	}
	if !projectAssistantAttachmentSelectionAvailable(request, newProjectEinoAssistantRunState()) {
		t.Fatal("historical text receipt did not keep read_attachment available on a later turn")
	}
	result, err := projectAssistantReadAttachmentTool(context.Background(), projectAssistantToolCallRequest{
		AttachmentReader: reader,
		Conversation:     resumedConversation,
		Arguments:        map[string]any{"attachmentID": receipt.ID, "offset": float64(7), "limit": float64(16)},
	})
	if err != nil {
		t.Fatalf("read historical text receipt after resume: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result), &envelope); err != nil {
		t.Fatalf("decode historical text read result: %v", err)
	}
	if envelope["content"] != string(content[7:23]) || envelope["offset"] != float64(7) {
		t.Fatalf("historical text read envelope = %#v, want bounded resumed range", envelope)
	}
}

func TestProjectEinoAssistantHistoricalSmallTextRehydratesOnlyAtModelBoundary(t *testing.T) {
	content := []byte("LIVE-TILT-START-4821\nsmall historical attachment\nLIVE-TILT-END-7390")
	receipt := attachmentReceiptForTest("text-history-inline", "pasted-text.txt", "text/plain", content)
	conversation := []chatMessage{{Role: "user", Content: "inspect the pasted text", Attachments: []projectAssistantAttachmentReceipt{receipt}}}
	messages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		t.Fatalf("project historical text receipt: %v", err)
	}
	checkpoint := projectEinoAssistantMessagesWithoutAttachments(messages)
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("encode metadata-only checkpoint: %v", err)
	}
	if strings.Contains(string(encoded), string(content)) {
		t.Fatal("historical text bytes were inlined into checkpoint state")
	}
	lifecycle := &projectEinoAssistantLifecycle{req: projectAssistantRunRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{receipt.ID: content}},
		Conversation:     conversation,
	}}
	historicalIDs, err := lifecycle.rehydrateHistoricalAttachmentMessages(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("rehydrate historical text: %v", err)
	}
	if _, ok := historicalIDs[receipt.ID]; !ok {
		t.Fatalf("historical IDs = %#v, want %q", historicalIDs, receipt.ID)
	}
	if !strings.Contains(messages[1].Content, "LIVE-TILT-END-7390") || !strings.Contains(messages[1].Content, "untrusted data") {
		t.Fatalf("historical model content = %q, want transient guarded text", messages[1].Content)
	}
	checkpointAfterSample := projectEinoAssistantMessagesWithoutAttachments(messages)
	checkpointAfterSampleJSON, err := json.Marshal(checkpointAfterSample)
	if err != nil {
		t.Fatalf("encode checkpoint after model sample: %v", err)
	}
	if strings.Contains(string(checkpointAfterSampleJSON), string(content)) || !strings.Contains(string(checkpointAfterSampleJSON), receipt.ID) {
		t.Fatalf("checkpoint after model sample = %s, want receipt metadata without attachment bytes", checkpointAfterSampleJSON)
	}
	projected := projectEinoMessagesToChat(checkpointAfterSample)
	projectedJSON, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("encode projected history: %v", err)
	}
	if strings.Contains(string(projectedJSON), string(content)) {
		t.Fatal("rehydrated historical text leaked into durable chat projection")
	}

	currentMessages, err := projectChatMessagesToEino(conversation)
	if err != nil {
		t.Fatalf("project current text receipt: %v", err)
	}
	currentIDs := map[string]struct{}{receipt.ID: {}}
	currentHistoricalIDs, err := lifecycle.rehydrateHistoricalAttachmentMessages(context.Background(), currentMessages, currentIDs)
	if err != nil {
		t.Fatalf("exclude current text from historical rehydration: %v", err)
	}
	if len(currentHistoricalIDs) != 0 || currentMessages[1].Content != "" {
		t.Fatalf("current text historical projection = ids %#v content %q, want metadata-only placeholder", currentHistoricalIDs, currentMessages[1].Content)
	}
}

func TestProjectAssistantReadAttachmentToolAlignsMultibyteRange(t *testing.T) {
	content := []byte("a€b")
	receipt := attachmentReceiptForTest("text-utf8", "notes.txt", "text/plain", content)
	state := newProjectEinoAssistantRunState()
	state.SetContentParts([]projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)})
	result, err := projectAssistantReadAttachmentTool(context.Background(), projectAssistantToolCallRequest{
		AttachmentReader: projectAssistantAttachmentReaderTestDouble{contents: map[string][]byte{"text-utf8": content}},
		RunState:         state,
		Arguments:        map[string]any{"attachmentID": "text-utf8", "offset": float64(1), "limit": float64(1)},
	})
	if err != nil {
		t.Fatalf("read multibyte attachment range: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["content"] != "€" || envelope["offset"] != float64(1) || envelope["nextOffset"] != float64(4) || envelope["complete"] != false {
		t.Fatalf("multibyte attachment range = %#v", envelope)
	}
}

func TestProjectAssistantReadAttachmentContentIsTransient(t *testing.T) {
	raw := `{"attachmentID":"text-1","filename":"notes.txt","contentType":"text/plain","sizeBytes":18,"offset":0,"nextOffset":18,"content":"private plan text","complete":true}`
	state := newProjectEinoAssistantRunState()
	placeholder := state.RegisterTransientToolResult(projectToolReadAttachment, raw)
	if strings.Contains(placeholder, "private plan text") || !strings.Contains(placeholder, "contentSHA256") {
		t.Fatalf("persistent placeholder leaked content: %s", placeholder)
	}
	expanded := state.ExpandTransientToolMessages([]*schema.Message{{
		Role: schema.Tool, ToolName: projectToolReadAttachment, ToolCallID: "call-1", Content: placeholder,
	}})
	if len(expanded) != 1 || expanded[0].Content != raw {
		t.Fatalf("immediate model input did not recover transient content: %#v", expanded)
	}
	state.RecordModelInput([]chatMessage{{Role: "tool", Name: projectToolReadAttachment, ToolCallID: "call-1", Content: raw}})
	checkpoint := state.CheckpointState()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private plan text") || !strings.Contains(string(encoded), "contentSHA256") {
		t.Fatalf("checkpoint persisted raw attachment content: %s", encoded)
	}
}

func TestProjectAssistantAttachmentContentPartsEnforceTurnBounds(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", sha256.Size*2)
	parts := make([]projectAssistantContentPart, 0, projectAssistantMaxAttachmentsPerTurn+1)
	for index := 0; index <= projectAssistantMaxAttachmentsPerTurn; index++ {
		parts = append(parts, projectAssistantContentPartAttachment(projectAssistantAttachmentReceipt{
			ID: "att-" + string(rune('a'+index)), Filename: "screen.png", ContentType: "image/png",
			SizeBytes: 1, SHA256: digest, CreatedAt: now,
		}))
	}
	if _, _, _, err := normalizeProjectAssistantContentParts(parts, nil, nil); err == nil || !strings.Contains(err.Error(), "at most 8 attachments") {
		t.Fatalf("attachment count validation error = %v", err)
	}
	parts = parts[:3]
	for index := range parts {
		parts[index].Attachment.SizeBytes = 7 << 20
	}
	if _, _, _, err := normalizeProjectAssistantContentParts(parts, nil, nil); err == nil || !strings.Contains(err.Error(), "total at most") {
		t.Fatalf("attachment aggregate validation error = %v", err)
	}
}
