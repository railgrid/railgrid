// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package sessionlog is the read/write path over a session's event log:
// fold it into state, apply a command, append what it produced. It exists so
// the HTTP layer and the Session reconciler drive the same state machine the
// same way (the store imports session, so these helpers cannot live in
// either).
package sessionlog

import (
	"context"
	"errors"
	"time"

	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/store"
)

// Fold loads and folds one session's full log.
func Fold(ctx context.Context, st store.Store, scope store.Scope, id string) (session.SessionState, error) {
	events, err := st.ListEvents(ctx, scope, id, 0, 0)
	if err != nil {
		return session.SessionState{}, err
	}
	state := session.Fold(events)
	if state.ID == "" && len(events) == 0 {
		return session.SessionState{}, store.ErrNotFound
	}
	return state, nil
}

// Submit folds, applies cmd, and appends the resulting events, retrying on a
// concurrent append (another replica or the HTTP layer got there first).
// allowEmpty lets the caller create the first events of a fresh session.
func Submit(ctx context.Context, st store.Store, scope store.Scope, id string, cmd session.Command, allowEmpty bool) (session.SessionState, error) {
	for attempt := 0; ; attempt++ {
		state, err := Fold(ctx, st, scope, id)
		if err != nil {
			if !(allowEmpty && errors.Is(err, store.ErrNotFound)) {
				return session.SessionState{}, err
			}
			state = session.SessionState{}
		}
		events, err := session.Apply(state, cmd, time.Now())
		if err != nil {
			return session.SessionState{}, err
		}
		last, err := st.AppendEvents(ctx, scope, id, state.LastOrdinal, events)
		if errors.Is(err, store.ErrOrdinalConflict) && attempt < 2 {
			continue
		}
		if err != nil {
			return session.SessionState{}, err
		}
		for i := range events {
			events[i].Ordinal = last - int64(len(events)-1-i)
			state = session.Evolve(state, events[i])
		}
		_ = st.TouchSession(ctx, scope, id, state.Phase, time.Now())
		return state, nil
	}
}

// SetCheckpoint records a checkpoint only when it differs from what the log
// already says, so repeated reconciles don't spam the event stream.
func SetCheckpoint(ctx context.Context, st store.Store, scope store.Scope, id string, cp session.Checkpoint) error {
	state, err := Fold(ctx, st, scope, id)
	if err != nil {
		return err
	}
	if cur, ok := state.Checkpoints[cp.Name]; ok && cur == cp {
		return nil
	}
	_, err = Submit(ctx, st, scope, id, session.CmdCheckpointUpdated{Checkpoint: cp}, false)
	return err
}
