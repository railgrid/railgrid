// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

import "encoding/json"

// InitialState returns the attempt coordinates needed to cancel a dispatch
// before the runner has returned its first event checkpoint. It deliberately
// contains no credential or workspace data and is not by itself a resumable
// event snapshot; callers persist it before invoking Runner.Start.
func (b *Backend) InitialState() (json.RawMessage, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	state := State{
		TaskID: b.cfg.TaskID, AttemptID: b.cfg.AttemptID, Epoch: b.cfg.Epoch,
		BackendKey: b.cfg.BackendKey, SessionID: b.cfg.SessionID,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
