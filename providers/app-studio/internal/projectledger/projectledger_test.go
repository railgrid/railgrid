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

package projectledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
)

// fakeProjects is a one-object stand-in for the control plane. It enforces the
// one property the ledger depends on — a merge patch carrying a stale
// metadata.resourceVersion is a Conflict — and counts patches so a test can
// see the compare-and-swap retry happen.
type fakeProjects struct {
	project *aiv1alpha1.Project
	gets    int
	patches int
	// beforePatch runs just before each patch is applied, so a test can play
	// the part of the other replica.
	beforePatch func()
	missing     bool
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{project: &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-demo"), ResourceVersion: "1"},
	}}
}

var projectGR = schema.GroupResource{Group: "ai.railgrid.ai", Resource: "projects"}

func (f *fakeProjects) Get(_ context.Context, name string) (*aiv1alpha1.Project, error) {
	f.gets++
	if f.missing {
		return nil, apierrors.NewNotFound(projectGR, name)
	}
	return f.project.DeepCopy(), nil
}

func (f *fakeProjects) PatchStatus(_ context.Context, name string, patch []byte) error {
	if f.beforePatch != nil {
		f.beforePatch()
	}
	f.patches++
	if f.missing {
		return apierrors.NewNotFound(projectGR, name)
	}
	var body struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Status map[string]any `json:"status"`
	}
	if err := json.Unmarshal(patch, &body); err != nil {
		return err
	}
	if body.Metadata.ResourceVersion != f.project.ResourceVersion {
		return apierrors.NewConflict(projectGR, name,
			errors.New("the object has been modified; please apply your changes to the latest version and try again"))
	}
	// Apply just the member the ledger writes, the way a merge patch would.
	raw, err := json.Marshal(body.Status["workspace"])
	if err != nil {
		return err
	}
	if string(raw) == "null" {
		f.project.Status.Workspace = nil
	} else {
		var status aiv1alpha1.ProjectWorkspaceStatus
		if err := json.Unmarshal(raw, &status); err != nil {
			return err
		}
		f.project.Status.Workspace = &status
	}
	rv, _ := strconv.Atoi(f.project.ResourceVersion)
	f.project.ResourceVersion = strconv.Itoa(rv + 1)
	return nil
}

func TestLedgerRoundTripsEveryMember(t *testing.T) {
	ctx := context.Background()
	projects := newFakeProjects()
	ledger := New(projects)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid-demo"}

	if record, err := ledger.Read(ctx, scope); err != nil || record.SourceRevision != 0 || len(record.UncommittedPaths) != 0 {
		t.Fatalf("empty ledger = %#v, err=%v", record, err)
	}

	if _, err := ledger.Update(ctx, scope, func(record *workspace.LedgerRecord) (bool, error) {
		record.SourceRevision = 7
		record.UncommittedPaths = []string{"src/b.ts", "src/a.ts"}
		record.PendingCommit = &workspace.PendingCommit{
			Name: "commit-1", RepositoryRef: "demo-repo", WorkspaceDigest: "sha256:abc", Paths: []string{"src/a.ts"},
		}
		record.Settlement = &workspace.CommitSettlement{WorkspaceDigest: "sha256:def", Paths: []string{"src/b.ts"}}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	record, err := ledger.Read(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if record.SourceRevision != 7 {
		t.Fatalf("source revision = %d, want 7", record.SourceRevision)
	}
	// Normalization is the ledger's, so readers never depend on which writer
	// produced the record.
	if strings.Join(record.UncommittedPaths, ",") != "src/a.ts,src/b.ts" {
		t.Fatalf("uncommitted paths = %v, want sorted", record.UncommittedPaths)
	}
	if record.PendingCommit == nil || record.PendingCommit.Name != "commit-1" ||
		record.PendingCommit.RepositoryRef != "demo-repo" || record.PendingCommit.WorkspaceDigest != "sha256:abc" {
		t.Fatalf("pending commit = %#v", record.PendingCommit)
	}
	if record.Settlement == nil || record.Settlement.WorkspaceDigest != "sha256:def" {
		t.Fatalf("settlement = %#v", record.Settlement)
	}
	if projects.project.Status.Workspace.PendingCommit.RequestedAt == nil {
		t.Fatal("pending commit carries no requestedAt")
	}

	// Clearing has to be said out loud in a merge patch, or the members would
	// simply stay.
	if _, err := ledger.Update(ctx, scope, func(record *workspace.LedgerRecord) (bool, error) {
		record.PendingCommit = nil
		record.Settlement = nil
		record.UncommittedPaths = nil
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	record, err = ledger.Read(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if record.PendingCommit != nil || record.Settlement != nil || len(record.UncommittedPaths) != 0 {
		t.Fatalf("cleared ledger = %#v", record)
	}
	if record.SourceRevision != 7 {
		t.Fatalf("clearing the dirty set moved the revision to %d", record.SourceRevision)
	}
}

// TestLedgerUpdateRetriesTheLosingWriter is the property two replicas depend
// on: the loser of a race re-reads and re-applies rather than overwriting.
func TestLedgerUpdateRetriesTheLosingWriter(t *testing.T) {
	ctx := context.Background()
	projects := newFakeProjects()
	ledger := New(projects)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid-demo"}

	// The other replica records its own dirty path immediately before our
	// first patch lands, so ours is written against a stale version.
	raced := false
	projects.beforePatch = func() {
		if raced {
			return
		}
		raced = true
		projects.project.Status.Workspace = &aiv1alpha1.ProjectWorkspaceStatus{
			SourceRevision:   3,
			UncommittedPaths: []string{"other.ts"},
		}
		rv, _ := strconv.Atoi(projects.project.ResourceVersion)
		projects.project.ResourceVersion = strconv.Itoa(rv + 1)
	}

	if _, err := ledger.Update(ctx, scope, func(record *workspace.LedgerRecord) (bool, error) {
		record.UncommittedPaths = append(record.UncommittedPaths, "mine.ts")
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if projects.patches != 2 {
		t.Fatalf("patches = %d, want one rejected and one accepted", projects.patches)
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(record.UncommittedPaths, ",") != "mine.ts,other.ts" {
		t.Fatalf("paths after the race = %v; the losing writer overwrote the winner", record.UncommittedPaths)
	}
	if record.SourceRevision != 3 {
		t.Fatalf("source revision after the race = %d, want the other writer's 3", record.SourceRevision)
	}
}

// TestLedgerRefusesARecreatedProject: a Project name can be reused, and the
// new incarnation's working copy is not the old one's. The scope carries the
// UID precisely so a stale ledger write cannot land on it.
func TestLedgerRefusesARecreatedProject(t *testing.T) {
	ctx := context.Background()
	projects := newFakeProjects()
	projects.project.UID = types.UID("uid-new")
	ledger := New(projects)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid-old"}

	if _, err := ledger.Read(ctx, scope); !errors.Is(err, ErrProjectGone) {
		t.Fatalf("read against a recreated project = %v, want ErrProjectGone", err)
	}
	if _, err := ledger.Update(ctx, scope, func(*workspace.LedgerRecord) (bool, error) { return true, nil }); !errors.Is(err, ErrProjectGone) {
		t.Fatalf("update against a recreated project = %v, want ErrProjectGone", err)
	}
	if projects.patches != 0 {
		t.Fatal("wrote to a recreated project's ledger")
	}
}

func TestLedgerReportsADeletedProject(t *testing.T) {
	ctx := context.Background()
	projects := newFakeProjects()
	projects.missing = true
	ledger := New(projects)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid-demo"}
	if _, err := ledger.Read(ctx, scope); !errors.Is(err, ErrProjectGone) {
		t.Fatalf("read of a deleted project = %v, want ErrProjectGone", err)
	}
}

// TestLedgerWritesNothingWhenTheMutationChangesNothing keeps the reconcile
// storm down: an unchanged ledger must not bump the Project's resourceVersion,
// because every such bump wakes the Project watch.
func TestLedgerWritesNothingWhenTheMutationChangesNothing(t *testing.T) {
	ctx := context.Background()
	projects := newFakeProjects()
	ledger := New(projects)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid-demo"}
	if _, err := ledger.Update(ctx, scope, func(*workspace.LedgerRecord) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if projects.patches != 0 {
		t.Fatalf("patches = %d, want none", projects.patches)
	}
}

// TestLedgerRefusesToSilentlyDropPathsPastTheBound: the CRD's MaxItems is
// derived from the 500-file tree cap, so overflowing it is a bug in some other
// bound. Truncating would mean a file that never reaches git, so it is an
// error instead.
func TestLedgerRefusesToSilentlyDropPathsPastTheBound(t *testing.T) {
	ctx := context.Background()
	projects := newFakeProjects()
	ledger := New(projects)
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid-demo"}
	_, err := ledger.Update(ctx, scope, func(record *workspace.LedgerRecord) (bool, error) {
		for i := 0; i <= MaxUncommittedPaths; i++ {
			record.UncommittedPaths = append(record.UncommittedPaths, fmt.Sprintf("f%05d.ts", i))
		}
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "more than the") {
		t.Fatalf("over-bound update = %v, want a refusal naming the bound", err)
	}
	if projects.patches != 0 {
		t.Fatal("sent a patch the API server would have rejected")
	}
}

func TestLedgerRequiresABackend(t *testing.T) {
	var ledger *Ledger
	if _, err := ledger.Read(context.Background(), workspace.Scope{ProjectName: "demo"}); !errors.Is(err, workspace.ErrNoLedger) {
		t.Fatalf("nil ledger read = %v", err)
	}
	if FromProjects(nil) != nil {
		t.Fatal("FromProjects(nil) built a ledger")
	}
	if FromControllerClient(nil) != nil {
		t.Fatal("FromControllerClient(nil) built a ledger")
	}
}

var _ runtime.Object = &aiv1alpha1.Project{}
