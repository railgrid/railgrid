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

package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

type relationWorkspaceOps struct {
	fakeWorkspaceOps
	bindings   map[string]kcp.ProviderBinding
	bindingErr error
}

func (f *relationWorkspaceOps) ListChildTeamWorkspaces(context.Context, string) ([]string, error) {
	return []string{"workspace"}, nil
}
func (f *relationWorkspaceOps) ListProviderAPIBindings(context.Context, string, string) (map[string]kcp.ProviderBinding, error) {
	return f.bindings, f.bindingErr
}

func TestWorkspaceReferencesPreserveOwnershipAndBindingState(t *testing.T) {
	ops := &relationWorkspaceOps{bindings: map[string]kcp.ProviderBinding{
		"code":  {ExportPath: "root:railgrid:providers:code"},
		"vault": {ExportPath: "root:railgrid:tenants:org:providers:vault", SelfHosted: true, Terminating: true, DeletionBlocked: "Finalizers remain"},
	}}
	svc := &Service{bootstrapper: ops}
	rows, err := svc.ListOrgWorkspaces(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	refs := rows[0].ProviderReferences
	if len(refs) != 2 || refs[0].Name != "code" || refs[0].Scope != "platform" || refs[1].Scope != "organization" || !refs[1].Terminating || refs[1].DeletionBlocked != "Finalizers remain" || refs[1].ExportPath != ops.bindings["vault"].ExportPath {
		t.Fatalf("incorrect binding references: %#v", refs)
	}
	ops.bindingErr = errors.New("unavailable")
	rows, err = svc.ListOrgWorkspaces(context.Background(), "org")
	if err != nil || !rows[0].ProvidersUnavailable || len(rows[0].ProviderReferences) != 0 {
		t.Fatalf("failed read should stay unknown: %#v, %v", rows, err)
	}
}
