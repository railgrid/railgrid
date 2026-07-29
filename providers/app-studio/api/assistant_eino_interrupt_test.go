/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
)

func TestProjectEinoAssistantCheckpointStoreDeletesCheckpoint(t *testing.T) {
	store := newProjectEinoAssistantCheckpointStoreWithCheckpoint("checkpoint-1", []byte("state"))
	if err := store.Delete(context.Background(), "checkpoint-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := store.Get(context.Background(), "checkpoint-1"); err != nil || ok {
		t.Fatalf("Get after Delete = ok=%v, err=%v", ok, err)
	}
}

var _ adk.CheckPointDeleter = (*projectEinoAssistantCheckpointStore)(nil)
