// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// A context alone cannot interrupt an HTTP body read. Set the connection read
// deadline too; closing a server request body without this can itself block.
func readActionRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, limit int64, timeout time.Duration) (Request, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	body := r.Body
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = controller.SetReadDeadline(time.Now())
		_ = body.Close()
	})
	defer func() {
		if !stop() {
			<-done
		}
		_ = controller.SetReadDeadline(time.Time{})
	}()
	decoder := json.NewDecoder(http.MaxBytesReader(w, body, limit))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Request{}, errors.New("unexpected trailing action input")
	}
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	return request, nil
}
