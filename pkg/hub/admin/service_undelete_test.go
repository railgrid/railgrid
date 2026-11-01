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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/railgrid/pkg/hub/kcp"
)

// fakeWorkspaceOps holds one workspace's soft-delete stamp and scripts how
// the clear behaves: `conflicts` is how many conflicts to return before the
// clear succeeds, and `onConflict` mutates the stamp between the conflict
// and the handler's re-read (a concurrent newer delete).
type fakeWorkspaceOps struct {
	stamp      *time.Time
	forced     bool
	conflicts  int
	onConflict func(f *fakeWorkspaceOps)
	clearCalls int
}

func (f *fakeWorkspaceOps) ListChildTeamWorkspaces(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f *fakeWorkspaceOps) GetWorkspaceDisplayName(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeWorkspaceOps) GetChildWorkspaceClusterName(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeWorkspaceOps) ListProviderAPIBindings(context.Context, string, string) (map[string]kcp.ProviderBinding, error) {
	return nil, nil
}
func (f *fakeWorkspaceOps) GetWorkspaceDeletionRequestedAt(context.Context, string, string) (*time.Time, bool, error) {
	if f.stamp == nil {
		return nil, false, nil
	}
	t := *f.stamp
	return &t, true, nil
}
func (f *fakeWorkspaceOps) GetWorkspaceDeletionForced(context.Context, string, string) (bool, error) {
	return f.forced, nil
}
func (f *fakeWorkspaceOps) ClearWorkspaceDeletionAnnotation(context.Context, string, string) error {
	f.clearCalls++
	if f.forced {
		return kcp.ErrWorkspaceDeletionForced
	}
	if f.conflicts > 0 {
		f.conflicts--
		if f.onConflict != nil {
			f.onConflict(f)
		}
		return apierrors.NewConflict(schema.GroupResource{Resource: "workspaces"}, "ws-1", errors.New("stale"))
	}
	f.stamp = nil
	return nil
}

func TestUndeleteOrgWorkspace(t *testing.T) {
	recent := time.Now().UTC().Add(-time.Hour)
	old := time.Now().UTC().Add(-48 * time.Hour)
	newer := time.Now().UTC()

	cases := []struct {
		name       string
		ops        *fakeWorkspaceOps
		grace      time.Duration
		wantErr    error // nil: success
		wantErrMsg string
		wantStamp  *time.Time
		wantClears int
	}{
		{
			name:       "not stamped is a no-op",
			ops:        &fakeWorkspaceOps{},
			grace:      24 * time.Hour,
			wantClears: 0,
		},
		{
			name:       "inside window clears",
			ops:        &fakeWorkspaceOps{stamp: &recent},
			grace:      24 * time.Hour,
			wantClears: 1,
		},
		{
			name:      "past configured window refused",
			ops:       &fakeWorkspaceOps{stamp: &old},
			grace:     24 * time.Hour,
			wantErr:   ErrGracePeriodExpired,
			wantStamp: &old,
		},
		{
			name:       "forced deletion is never restorable",
			ops:        &fakeWorkspaceOps{stamp: &recent, forced: true},
			grace:      24 * time.Hour,
			wantErr:    kcp.ErrWorkspaceDeletionForced,
			wantStamp:  &recent,
			wantClears: 1,
		},
		{
			name:       "conflict with the same stamp retries and clears",
			ops:        &fakeWorkspaceOps{stamp: &recent, conflicts: 2},
			grace:      24 * time.Hour,
			wantClears: 3,
		},
		{
			name: "conflict from a newer delete stops without clearing",
			ops: &fakeWorkspaceOps{stamp: &recent, conflicts: 1, onConflict: func(f *fakeWorkspaceOps) {
				n := newer
				f.stamp = &n
			}},
			grace:      24 * time.Hour,
			wantErrMsg: "deletion changed while restoring",
			wantStamp:  &newer,
			wantClears: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{bootstrapper: tc.ops}
			err := svc.UndeleteOrgWorkspace(context.Background(), "org-a", "ws-1", tc.grace)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case tc.wantErrMsg != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErrMsg)
				}
			default:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			}
			if tc.ops.clearCalls != tc.wantClears {
				t.Errorf("clear calls = %d, want %d", tc.ops.clearCalls, tc.wantClears)
			}
			switch {
			case tc.wantStamp == nil && tc.ops.stamp != nil:
				t.Errorf("stamp still set: %v", tc.ops.stamp)
			case tc.wantStamp != nil && (tc.ops.stamp == nil || !tc.ops.stamp.Equal(*tc.wantStamp)):
				t.Errorf("stamp = %v, want %v", tc.ops.stamp, tc.wantStamp)
			}
		})
	}
}

// The handler maps the service outcomes onto 204 / 409 / 500.
func TestUndeleteOrgWorkspaceHandlerStatus(t *testing.T) {
	recent := time.Now().UTC().Add(-time.Hour)
	old := time.Now().UTC().Add(-48 * time.Hour)
	cases := []struct {
		name       string
		ops        *fakeWorkspaceOps
		wantStatus int
	}{
		{"inside window", &fakeWorkspaceOps{stamp: &recent}, http.StatusNoContent},
		{"not stamped", &fakeWorkspaceOps{}, http.StatusNoContent},
		{"forced", &fakeWorkspaceOps{stamp: &recent, forced: true}, http.StatusConflict},
		{"expired", &fakeWorkspaceOps{stamp: &old}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mux.NewRouter()
			sub := r.PathPrefix("/api/admin").Subrouter()
			sub.Use(Middleware(
				UserResolverFunc(func(*http.Request) (string, error) { return "root", nil }),
				AdminCheckerFunc(func(context.Context, string) bool { return true }),
			))
			NewHandler(&Service{bootstrapper: tc.ops}, nil, nil).WithSoftDeleteGracePeriod(24 * time.Hour).Register(sub)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/organizations/org-a/workspaces/ws-1/undelete", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
