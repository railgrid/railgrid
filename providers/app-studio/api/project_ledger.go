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

// Wiring the working-copy ledger onto the request path.
//
// Since §9 Cut D.3 the dirty-path set, the source revision, the commit in
// flight and the settlement receipt live in `Project.status.workspace` rather
// than in JSON beside the tree. The workspace store therefore needs a
// control-plane client for those operations, and WHICH client is a property of
// who is asking: on this side of the provider it is the caller's own, exactly
// as every other tenant write here is.
//
// So the ledger rides the context. It is attached in two places and only two:
// identityFromRequest, which every handler that resolves a caller goes
// through, and the assistant run, whose goroutine outlives the request that
// started it. Both attach a LAZY ledger — no client is built until something
// actually reads or writes the ledger — so a health probe or an SSE stream
// pays nothing for it.

import (
	"context"
	"sync"

	"github.com/railgrid/provider-app-studio/internal/projectledger"
	"github.com/railgrid/provider-app-studio/workspace"
)

// lazyProjectLedger defers building the caller's tenant client until the
// first ledger operation. Most requests never touch the ledger.
type lazyProjectLedger struct {
	once   sync.Once
	build  func() (workspace.Ledger, error)
	ledger workspace.Ledger
	err    error
}

func (l *lazyProjectLedger) resolve() (workspace.Ledger, error) {
	l.once.Do(func() { l.ledger, l.err = l.build() })
	return l.ledger, l.err
}

func (l *lazyProjectLedger) Read(ctx context.Context, scope workspace.Scope) (workspace.LedgerRecord, error) {
	ledger, err := l.resolve()
	if err != nil {
		return workspace.LedgerRecord{}, err
	}
	return ledger.Read(ctx, scope)
}

func (l *lazyProjectLedger) Update(ctx context.Context, scope workspace.Scope, mutate func(*workspace.LedgerRecord) (bool, error)) (workspace.LedgerRecord, error) {
	ledger, err := l.resolve()
	if err != nil {
		return workspace.LedgerRecord{}, err
	}
	return ledger.Update(ctx, scope, mutate)
}

// projectLedgerFor returns the working-copy ledger for one caller, or nil when
// this server has no tenant client to build one from (unit tests that never
// touch a project's files, and REST-only runs without a hub).
func (s *Server) projectLedgerFor(id identity) workspace.Ledger {
	if s == nil {
		return nil
	}
	if s.projectClientFor == nil && id.provider == nil && (s.tenant == nil || id.clusterID == "") {
		return nil
	}
	return &lazyProjectLedger{build: func() (workspace.Ledger, error) {
		c, err := s.clientFor(id)
		if err != nil {
			return nil, err
		}
		ledger := projectledger.FromProjects(c.Projects())
		if ledger == nil {
			return nil, workspace.ErrNoLedger
		}
		return ledger, nil
	}}
}

// withProjectLedger attaches the caller's working-copy ledger to ctx. It is a
// no-op when there is nothing to build one from, so a context that already
// carries a ledger (an assistant run's, a test's) keeps it.
func (s *Server) withProjectLedger(ctx context.Context, id identity) context.Context {
	if _, ok := workspace.LedgerFromContext(ctx); ok {
		return ctx
	}
	ledger := s.projectLedgerFor(id)
	if ledger == nil {
		return ctx
	}
	return workspace.ContextWithLedger(ctx, ledger)
}
