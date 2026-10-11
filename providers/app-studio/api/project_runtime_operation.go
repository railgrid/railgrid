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
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

var errProjectRuntimeOperationTargetChanged = errors.New("project changed while the runtime operation was waiting; reload and retry")
var errProjectRuntimeOperationContention = errors.New("project runtime operation is reserved by another operation")
var errProjectRuntimeOperationWaitTimeout = errors.New("timed out waiting for the project runtime operation")

// projectRuntimeOperation is a process-local serialization token. The
// supervisor's durable project owner is the cross-replica authority; this map
// only prevents overlapping operations on one API instance.
type projectRuntimeOperation struct {
	token      chan struct{}
	references int
}

type projectRuntimeOwnerContextKey struct{}

// contextWithProjectRuntimeOwner is used only within an already-reserved
// destructive operation. The context must not outlive that operation's lease.
func contextWithProjectRuntimeOwner(ctx context.Context, scope workspace.Scope) context.Context {
	return context.WithValue(ctx, projectRuntimeOwnerContextKey{}, scope)
}

// refreshProjectRuntimeOperationProject reads the current Project and checks
// its immutable UID to fence a deleted and recreated Project with the same
// name. The current object carries the binding and repository settings the
// operation must use; callers coordinate with runtime mutations separately.
func refreshProjectRuntimeOperationProject(ctx context.Context, c *asclient.Client, expected *aiv1alpha1.Project) (*aiv1alpha1.Project, error) {
	if expected == nil || expected.Name == "" || expected.UID == "" {
		return nil, fmt.Errorf("%w: project name and UID are required", errProjectRuntimeOperationTargetChanged)
	}
	if c == nil {
		return nil, errors.New("project client is not configured")
	}
	current, err := c.Projects().Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: project %q no longer exists: %v", errProjectRuntimeOperationTargetChanged, expected.Name, err)
		}
		return nil, err
	}
	if current.UID != expected.UID {
		return nil, fmt.Errorf("%w: project %q was replaced", errProjectRuntimeOperationTargetChanged, expected.Name)
	}
	if current.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: project %q is being deleted", errProjectRuntimeOperationTargetChanged, expected.Name)
	}
	return current, nil
}

// acquireProjectRuntimeOperation serializes runtime replacement and preview
// activation under the existing project workspace owner. It never holds the
// FileStore mutation lock while waiting on infrastructure.
func (s *Server) acquireProjectRuntimeOperation(ctx context.Context, scope workspace.Scope) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	releaseOwner := func() {}
	ownedScope, alreadyOwned := ctx.Value(projectRuntimeOwnerContextKey{}).(workspace.Scope)
	if s.store != nil && (!alreadyOwned || ownedScope != scope) {
		var err error
		releaseOwner, err = s.projectAssistantSupervisor().ReserveMutation(ctx, store.Scope{
			OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID,
			ProjectName: scope.ProjectName, ProjectUID: scope.ProjectUID,
		})
		if err != nil {
			return nil, err
		}
	}
	s.projectRuntimeOperationsMu.Lock()
	if s.projectRuntimeOperations == nil {
		s.projectRuntimeOperations = map[workspace.Scope]*projectRuntimeOperation{}
	}
	op := s.projectRuntimeOperations[scope]
	if op == nil {
		op = &projectRuntimeOperation{token: make(chan struct{}, 1)}
		s.projectRuntimeOperations[scope] = op
	}
	op.references++
	s.projectRuntimeOperationsMu.Unlock()
	drop := func() {
		s.projectRuntimeOperationsMu.Lock()
		defer s.projectRuntimeOperationsMu.Unlock()
		op.references--
		if op.references == 0 {
			delete(s.projectRuntimeOperations, scope)
		}
	}
	select {
	case op.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-op.token
			drop()
			releaseOwner()
			return nil, err
		}
	case <-ctx.Done():
		drop()
		releaseOwner()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-op.token; drop(); releaseOwner() }) }, nil
}
