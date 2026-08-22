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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantApprovedPlanVersionWorkspaceMutation = 2
	projectAssistantCapabilityWorkspaceMutate            = "workspace.mutate"
)

const projectEinoAssistantMaxTrackedReads = 128

type projectAssistantApprovedPlan struct {
	Goal               string    `json:"goal,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	Steps              []string  `json:"steps,omitempty"`
	TargetPaths        []string  `json:"targetPaths,omitempty"`
	Version            int       `json:"version,omitempty"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	AcceptanceCriteria []string  `json:"acceptanceCriteria,omitempty"`
	ApprovedAt         time.Time `json:"approvedAt,omitempty"`
	ApprovalTool       string    `json:"approvalTool,omitempty"`
	// RunLocal marks authorization that is valid only for the current Eino
	// run/checkpoint and must never be promoted into the cross-turn grant.
	RunLocal bool `json:"runLocal,omitempty"`
	// AllowAllWrites is the run-local, unbounded source-edit authority derived
	// only from an explicit fresh-project creation request.
	AllowAllWrites bool `json:"allowAllWrites,omitempty"`
}

type projectEinoAssistantRunState struct {
	mu         sync.Mutex
	callbackMu sync.Mutex

	messages                         []chatMessage
	lastToolMessages                 []chatMessage
	toolEvidence                     []chatMessage
	toolCalls                        []chatToolCall
	seenToolCalls                    map[string]int
	turn                             int
	turnPolicy                       projectAssistantTurnPolicy
	projectRepositoryRef             string
	toolPrompt                       string
	toolDiscovery                    *projectEinoAssistantToolDiscovery
	agentOptimizationMode            string
	dynamicToolCatalogDigest         string
	selectedDynamicToolNames         map[string]struct{}
	skillSnapshot                    *appskills.Snapshot
	catalogDigest                    string
	selectedSkillReceipts            map[string]projectAssistantSkillReceipt
	loadedSkillReceipts              map[string]projectAssistantSkillReceipt
	selectedContextResourceReceipts  []projectAssistantContextResourceReceipt
	contentParts                     []projectAssistantContentPart
	sessionSnapshot                  *projectEinoAssistantSessionSnapshot
	rolloutBudget                    *projectEinoAssistantRolloutBudget
	restoredRolloutBudget            *projectAssistantRolloutBudgetState
	permissionBarrier                bool
	approvedPlan                     *projectAssistantApprovedPlan
	executionPlan                    *projectAssistantApprovedPlan
	planProgress                     projectAssistantPlanSnapshot
	sourceMutationRevision           uint64
	verifiedMutationRevision         uint64
	commitRequired                   bool
	committedMutationRevision        uint64
	commitAttemptedRevision          uint64
	verifiedWorkspaceDigest          string
	committedWorkspaceDigest         string
	checkedMutationRevision          uint64
	verificationAttempted            bool
	verificationOutcome              string
	verificationSummary              string
	verificationBlockers             []string
	previewEvidence                  projectAssistantPreviewEvidence
	developmentSyncRevision          uint64
	developmentSyncStatus            string
	developmentSyncFailure           string
	developmentSyncRetry             uint64
	developmentSyncChanged           chan struct{}
	completedReadCalls               map[string]uint64
	observedReadFilePaths            map[string]struct{}
	readFileVersions                 map[string]string
	successfulMutationPaths          map[string]struct{}
	mutationRecoveryAttempts         map[string]projectAssistantMutationRecoveryAttempt
	mutationRecoveryRefs             map[string]struct{}
	mutationRecoveryIdentities       map[string]projectAssistantMutationRecoveryIdentity
	readFileCoverage                 map[string][]projectEinoAssistantLineRange
	repeatedActionSignature          string
	repeatedActionToolName           string
	repeatedActionCount              int
	runtimeWarmupAttempts            int
	modelCallOrdinal                 int
	transientToolResults             map[string]string
	transientPreviewImages           map[string]projectEinoAssistantTransientPreviewImage
	transientToolResultCount         uint64
	lastProgressMessage              string
	acceptedProgressCount            int
	lastAcceptedProgressModelCall    int
	progressReminder                 *projectEinoAssistantProgressReminder
	progressReminderAttempts         int
	progressReminderSilenceTriggered bool
	deferSteeringOnce                bool
	// contextGeneration identifies the active model-visible history window.
	// Compaction increments it after replacing history so lifecycle context
	// reconstruction cannot rely on a digest from the previous window.
	contextGeneration  uint64
	sandbox            *projectAssistantRunSandbox
	sandboxMetadata    *projectAssistantRunSandboxMetadata
	sandboxEligibility *CodingSandboxEligibility
	sandboxInitializer func(context.Context) (*projectAssistantRunSandbox, func(), error)
	sandboxInitErr     error
	sandboxRelease     func()
	sandboxInitContext context.Context
	sandboxInitAttempt *projectAssistantSandboxInitAttempt
	sandboxInitSuccess bool
}

// projectAssistantSandboxInitAttempt is the single shared setup operation for
// one lazy sandbox initialization attempt. The result belongs to the attempt,
// rather than the run state, so a waiter that wakes after a later retry starts
// still observes the result it waited for.
type projectAssistantSandboxInitAttempt struct {
	done    chan struct{}
	sandbox *projectAssistantRunSandbox
	err     error
}

// projectAssistantMutationRecoveryIdentity is server-owned metadata for a
// failed mutation action reference. It is persisted only in the run
// checkpoint and never grants mutation authority. Target is the canonical
// logical path; move_file references its source path rather than destination.
type projectAssistantMutationRecoveryIdentity struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
}

// projectAssistantMutationRecoveryAttempt is durable, server-owned retry
// state for one canonical mutation target. It does not grant mutation
// authority; it only bounds repeated failures at the same source revision.
type projectAssistantMutationRecoveryAttempt struct {
	Operation      string `json:"operation"`
	Target         string `json:"target"`
	SourceRevision uint64 `json:"sourceRevision"`
	Failures       int    `json:"failures"`
	Reread         bool   `json:"reread"`
	Blocked        bool   `json:"blocked"`
}

type projectEinoAssistantLineRange struct {
	start int
	end   int
}

const projectEinoAssistantReadThroughEOF = 1<<31 - 1

func (s *projectEinoAssistantRunState) EmitToolCall(
	callback func(projectToolCallStreamEvent),
	event projectToolCallStreamEvent,
) {
	if callback == nil {
		return
	}
	if s == nil {
		callback(event)
		return
	}
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	callback(event)
}

func newProjectEinoAssistantRunState() *projectEinoAssistantRunState {
	return &projectEinoAssistantRunState{
		seenToolCalls:              map[string]int{},
		selectedDynamicToolNames:   map[string]struct{}{},
		selectedSkillReceipts:      map[string]projectAssistantSkillReceipt{},
		loadedSkillReceipts:        map[string]projectAssistantSkillReceipt{},
		completedReadCalls:         map[string]uint64{},
		readFileVersions:           map[string]string{},
		readFileCoverage:           map[string][]projectEinoAssistantLineRange{},
		successfulMutationPaths:    map[string]struct{}{},
		mutationRecoveryAttempts:   map[string]projectAssistantMutationRecoveryAttempt{},
		mutationRecoveryRefs:       map[string]struct{}{},
		mutationRecoveryIdentities: map[string]projectAssistantMutationRecoveryIdentity{},
		transientToolResults:       map[string]string{},
		transientPreviewImages:     map[string]projectEinoAssistantTransientPreviewImage{},
		developmentSyncChanged:     make(chan struct{}),
		turnPolicy:                 projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging),
	}
}

func (s *projectEinoAssistantRunState) SetSandbox(sandbox *projectAssistantRunSandbox) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sandbox = sandbox
	if sandbox != nil {
		metadata := sandbox.metadataSnapshot()
		s.sandboxMetadata = &metadata
	}
	s.mu.Unlock()
}

func (s *projectEinoAssistantRunState) Sandbox() *projectAssistantRunSandbox {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandbox
}

func (s *projectEinoAssistantRunState) ConfigureSandboxCapability(eligibility CodingSandboxEligibility, initializer func(context.Context) (*projectAssistantRunSandbox, func(), error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configureSandboxCapabilityLocked(nil, eligibility, initializer)
}

// ConfigureSandboxCapabilityWithContext configures lazy sandbox setup and the
// run/segment context that owns that setup. Tool-call contexts are intentionally
// not used for the shared initializer: a canceled waiter must not cancel setup
// for the run's other tool calls.
func (s *projectEinoAssistantRunState) ConfigureSandboxCapabilityWithContext(runCtx context.Context, eligibility CodingSandboxEligibility, initializer func(context.Context) (*projectAssistantRunSandbox, func(), error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configureSandboxCapabilityLocked(runCtx, eligibility, initializer)
}

func (s *projectEinoAssistantRunState) configureSandboxCapabilityLocked(runCtx context.Context, eligibility CodingSandboxEligibility, initializer func(context.Context) (*projectAssistantRunSandbox, func(), error)) {
	copy := eligibility
	s.sandboxEligibility = &copy
	s.sandboxInitializer = initializer
	if runCtx == nil {
		s.sandboxInitContext = nil
	} else {
		s.sandboxInitContext = runCtx
	}
}

func (s *projectEinoAssistantRunState) SandboxEligibility() *CodingSandboxEligibility {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandboxEligibility == nil {
		return nil
	}
	copy := *s.sandboxEligibility
	return &copy
}

func (s *projectEinoAssistantRunState) SandboxRemoteEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandboxEligibility != nil && s.sandboxEligibility.Eligible && s.sandboxInitializer != nil
}

func (s *projectEinoAssistantRunState) EnsureSandbox(ctx context.Context) (*projectAssistantRunSandbox, error) {
	if s == nil {
		return nil, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.sandboxInitSuccess {
		sandbox, err := s.sandbox, s.sandboxInitErr
		s.mu.Unlock()
		return sandbox, err
	}
	if attempt := s.sandboxInitAttempt; attempt != nil {
		s.mu.Unlock()
		select {
		case <-attempt.done:
			return attempt.sandbox, attempt.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	initializer := s.sandboxInitializer
	initCtx := s.sandboxInitContext
	if initializer == nil {
		sandbox := s.sandbox
		s.mu.Unlock()
		return sandbox, nil
	}
	if initCtx == nil {
		initCtx = ctx
	}
	attempt := &projectAssistantSandboxInitAttempt{done: make(chan struct{})}
	s.sandboxInitAttempt = attempt
	s.mu.Unlock()

	sandbox, release, err := initializer(initCtx)

	s.mu.Lock()
	// SetSandbox is allowed during setup so the initializer can make the
	// sandbox visible to its own setup helpers. The returned value remains the
	// publication authority for the lazy initializer itself.
	if err == nil {
		s.sandbox = sandbox
		s.sandboxRelease = release
		s.sandboxInitErr = nil
		s.sandboxInitSuccess = true
		if sandbox != nil {
			metadata := sandbox.metadataSnapshot()
			s.sandboxMetadata = &metadata
		}
	} else {
		s.sandboxInitErr = err
	}
	attempt.sandbox = s.sandbox
	attempt.err = err
	s.sandboxInitAttempt = nil
	close(attempt.done)
	sandboxResult, setupErr := attempt.sandbox, attempt.err
	s.mu.Unlock()
	return sandboxResult, setupErr
}

func (s *projectEinoAssistantRunState) SandboxRelease() func() {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandboxRelease
}

func (s *projectEinoAssistantRunState) SetSandboxMetadata(metadata projectAssistantRunSandboxMetadata) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxMetadata = &metadata
}

func (s *projectEinoAssistantRunState) SandboxMetadata() *projectAssistantSandboxCheckpoint {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandboxMetadata == nil {
		return nil
	}
	return &projectAssistantSandboxCheckpoint{Metadata: *s.sandboxMetadata}
}

func (s *projectEinoAssistantRunState) ConfigureSkillSnapshot(snapshot appskills.Snapshot, selected, loaded []projectAssistantSkillReceipt) error {
	if s == nil {
		return errors.New("assistant run state is unavailable")
	}
	selectedMap := make(map[string]projectAssistantSkillReceipt, len(selected))
	loadedMap := make(map[string]projectAssistantSkillReceipt, len(selected)+len(loaded))
	validate := func(receipt projectAssistantSkillReceipt) error {
		entry, err := snapshot.Get(receipt.ID)
		if err != nil || !entry.Enabled || entry.QualifiedName != receipt.ID || entry.Name != receipt.Name || entry.Scope != receipt.Scope || entry.PackagePath != receipt.PackagePath || entry.Digest != receipt.Digest || entry.ContentDigest != receipt.ContentDigest {
			projectAssistantSkillMetric("drift", "detected")
			return errProjectAssistantSkillCatalogDrift
		}
		return nil
	}
	for _, receipt := range selected {
		if err := validate(receipt); err != nil {
			return err
		}
		selectedMap[receipt.ID] = receipt
		loadedMap[receipt.ID] = receipt
	}
	for _, receipt := range loaded {
		if err := validate(receipt); err != nil {
			return err
		}
		loadedMap[receipt.ID] = receipt
	}
	if len(selectedMap) > projectAssistantMaxSkills || len(loadedMap) > projectAssistantMaxSkills {
		return errors.New("assistant skill receipt limit exceeded")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshotCopy := snapshot
	s.skillSnapshot = &snapshotCopy
	s.catalogDigest = snapshot.CatalogDigest
	s.selectedSkillReceipts = selectedMap
	s.loadedSkillReceipts = loadedMap
	return nil
}

func (s *projectEinoAssistantRunState) SkillPrompt() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillSnapshot == nil {
		return ""
	}
	loaded := make([]projectAssistantSkillReceipt, 0, len(s.loadedSkillReceipts))
	for _, receipt := range s.loadedSkillReceipts {
		loaded = append(loaded, receipt)
	}
	return projectAssistantSkillsPrompt(*s.skillSnapshot, cloneProjectAssistantSkillReceipts(loaded))
}

func (s *projectEinoAssistantRunState) LoadSkill(id string) (appskills.Entry, error) {
	if s == nil {
		return appskills.Entry{}, errors.New("assistant run state is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillSnapshot == nil {
		projectAssistantSkillMetric("load", "rejected")
		return appskills.Entry{}, errors.New("assistant skill catalog is unavailable")
	}
	entry, err := s.skillSnapshot.Get(id)
	if err != nil || !entry.Enabled || entry.QualifiedName != id {
		projectAssistantSkillMetric("load", "rejected")
		return appskills.Entry{}, appskills.ErrSkillNotFound
	}
	receipt := projectAssistantSkillReceiptForEntry(entry)
	if s.loadedSkillReceipts == nil {
		s.loadedSkillReceipts = map[string]projectAssistantSkillReceipt{}
	}
	if _, loaded := s.loadedSkillReceipts[id]; !loaded && len(s.loadedSkillReceipts) >= projectAssistantMaxSkills {
		projectAssistantSkillMetric("load", "rejected")
		return appskills.Entry{}, errors.New("assistant skill load limit exceeded")
	}
	s.loadedSkillReceipts[id] = receipt
	projectAssistantSkillMetric("load", "success")
	return entry, nil
}

func (s *projectEinoAssistantRunState) ReadSkillResource(ctx context.Context, id, resourcePath string, opts appskills.ResourceReadOptions) (appskills.ResourceReadResult, error) {
	if s == nil {
		return appskills.ResourceReadResult{}, errors.New("assistant run state is unavailable")
	}
	s.mu.Lock()
	if s.skillSnapshot == nil {
		s.mu.Unlock()
		projectAssistantSkillMetric("resource", "rejected")
		return appskills.ResourceReadResult{}, errors.New("assistant skill catalog is unavailable")
	}
	if _, ok := s.loadedSkillReceipts[id]; !ok {
		s.mu.Unlock()
		projectAssistantSkillMetric("resource", "rejected")
		return appskills.ResourceReadResult{}, errors.New("skill must be loaded before reading its resources")
	}
	snapshot := *s.skillSnapshot
	s.mu.Unlock()
	result, err := snapshot.ReadResource(ctx, id, resourcePath, opts)
	if err != nil {
		projectAssistantSkillMetric("resource", "failure")
		return appskills.ResourceReadResult{}, err
	}
	projectAssistantSkillMetric("resource", "success")
	return result, nil
}

type projectEinoAssistantTransientPreviewImage struct {
	Base64Data string
	MIMEType   string
}

const projectEinoAssistantPreviewImageIntro = "Screenshot captured by the preceding development preview inspection."

func (s *projectEinoAssistantRunState) RegisterTransientPreviewImage(result, base64Data, mimeType string) string {
	return s.RegisterTransientPreviewImageForTool(projectToolInspectDevelopmentPreview, result, base64Data, mimeType)
}

func (s *projectEinoAssistantRunState) RegisterTransientPreviewImageForTool(toolName, result, base64Data, mimeType string) string {
	persistent := projectEinoAssistantPersistentToolResult(toolName, result)
	if s == nil || strings.TrimSpace(base64Data) == "" || mimeType != "image/png" {
		return persistent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transientToolResultCount++
	digest := sha256.Sum256([]byte(fmt.Sprintf("preview\x00%d\x00%s", s.transientToolResultCount, base64Data)))
	reference := hex.EncodeToString(digest[:16])
	if s.transientPreviewImages == nil || len(s.transientPreviewImages) >= 4 {
		s.transientPreviewImages = map[string]projectEinoAssistantTransientPreviewImage{}
	}
	s.transientPreviewImages[reference] = projectEinoAssistantTransientPreviewImage{Base64Data: base64Data, MIMEType: mimeType}
	var placeholder map[string]any
	if err := json.Unmarshal([]byte(persistent), &placeholder); err != nil {
		placeholder = map[string]any{"status": "unavailable", "summary": "transient preview image omitted from persistence"}
	}
	placeholder["transientImageReference"] = reference
	encoded, err := json.Marshal(placeholder)
	if err != nil {
		return persistent
	}
	return string(encoded)
}

func (s *projectEinoAssistantRunState) AcceptProgressMessage(message string) bool {
	if s == nil {
		return true
	}
	message = strings.TrimSpace(message)
	s.mu.Lock()
	defer s.mu.Unlock()
	if message == "" || message == s.lastProgressMessage {
		return false
	}
	s.lastProgressMessage = message
	if s.acceptedProgressCount < projectEinoAssistantProgressReminderMaxAcceptedCount {
		s.acceptedProgressCount++
	}
	s.lastAcceptedProgressModelCall = s.modelCallOrdinal
	// An accepted update satisfies every reminder that was queued before this
	// tool call. In particular, a concurrent write_todos/verification call in
	// the same model batch must not leave a stale reminder for the next sample.
	s.progressReminder = nil
	s.progressReminderAttempts = 0
	s.progressReminderSilenceTriggered = false
	return true
}

func (s *projectEinoAssistantRunState) AcceptedProgressCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acceptedProgressCount
}

func (s *projectEinoAssistantRunState) queueProgressReminderLocked(reminder projectEinoAssistantProgressReminder) bool {
	if !projectEinoAssistantProgressReminderKindValid(reminder.Kind) || s.progressReminder != nil {
		return false
	}
	if reminder.Kind == projectEinoAssistantProgressReminderVerification &&
		s.acceptedProgressCount > 0 && s.lastAcceptedProgressModelCall == s.modelCallOrdinal {
		return false
	}
	reminder.Detail = strings.TrimSpace(reminder.Detail)
	s.progressReminder = &reminder
	s.progressReminderAttempts = 0
	return true
}

func (s *projectEinoAssistantRunState) QueueProgressReminder(kind, detail string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
		Kind:   strings.TrimSpace(kind),
		Detail: strings.TrimSpace(detail),
	})
}

func (s *projectEinoAssistantRunState) QueuePlanProgressReminder(previous, next projectAssistantPlanSnapshot) bool {
	if s == nil || !projectEinoAssistantPlanPhaseTransition(previous, next) {
		return false
	}
	active := ""
	for _, step := range next.Steps {
		if step.Status == "in_progress" {
			active = strings.TrimSpace(step.ActiveForm)
			if active == "" {
				active = strings.TrimSpace(step.Content)
			}
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acceptedProgressCount > 0 && s.lastAcceptedProgressModelCall == s.modelCallOrdinal {
		return false
	}
	return s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
		Kind:   projectEinoAssistantProgressReminderPlan,
		Detail: active,
	})
}

func (s *projectEinoAssistantRunState) QueueVerificationProgressReminder(detail string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
		Kind:   projectEinoAssistantProgressReminderVerification,
		Detail: strings.TrimSpace(detail),
	})
}

func (s *projectEinoAssistantRunState) TakeProgressReminder(available bool) (projectEinoAssistantProgressReminder, bool) {
	if s == nil {
		return projectEinoAssistantProgressReminder{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !available || s.permissionBarrier || s.progressReminder == nil {
		if !available || s.permissionBarrier {
			s.progressReminder = nil
			s.progressReminderAttempts = 0
		}
		return projectEinoAssistantProgressReminder{}, false
	}
	reminder := *s.progressReminder
	s.progressReminderAttempts++
	if s.progressReminderAttempts >= projectEinoAssistantProgressReminderMaxAttempts {
		s.progressReminder = nil
		s.progressReminderAttempts = 0
	}
	return reminder, true
}

func (s *projectEinoAssistantRunState) progressReminderPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progressReminder != nil
}

func (s *projectEinoAssistantRunState) RegisterTransientToolResult(name, result string) string {
	persistent := projectEinoAssistantPersistentToolResult(name, result)
	if s == nil || projectToolBaseName(name) != projectToolGetPreviewConsoleLogs {
		return persistent
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.transientToolResultCount++
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", s.transientToolResultCount, result)))
	reference := hex.EncodeToString(digest[:16])
	if s.transientToolResults == nil {
		s.transientToolResults = map[string]string{}
	} else if len(s.transientToolResults) >= 8 {
		// Transient evidence exists only to bridge the immediately following
		// model call. Older snapshots remain as safe placeholders.
		s.transientToolResults = map[string]string{}
	}
	s.transientToolResults[reference] = result

	var placeholder map[string]any
	if err := json.Unmarshal([]byte(persistent), &placeholder); err != nil {
		placeholder = map[string]any{
			"status":         "unavailable",
			"summary":        "transient preview console result omitted from persistence",
			"transientEvent": true,
		}
	}
	placeholder["transientReference"] = reference
	encoded, err := json.Marshal(placeholder)
	if err != nil {
		return `{"status":"unavailable","summary":"transient preview console result omitted from persistence"}`
	}
	return string(encoded)
}

func (s *projectEinoAssistantRunState) ExpandTransientToolMessages(input []*schema.Message) []*schema.Message {
	if s == nil || len(input) == 0 {
		return input
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.transientToolResults) == 0 && len(s.transientPreviewImages) == 0 {
		return input
	}

	expanded := make([]*schema.Message, 0, len(input))
	var pendingImages []*schema.Message
	changed := false
	// A tool group must reach the provider unbroken: every tool message
	// answering an assistant tool-call message has to precede any other role.
	// Screenshots therefore queue up and flush once the group closes.
	flushImages := func() {
		if len(pendingImages) == 0 {
			return
		}
		expanded = append(expanded, pendingImages...)
		pendingImages = nil
	}
	for _, message := range input {
		if message == nil || message.Role != schema.Tool {
			flushImages()
			expanded = append(expanded, message)
			continue
		}
		toolName := message.ToolName
		if strings.TrimSpace(toolName) == "" {
			toolName = message.Name
		}
		var placeholder struct {
			TransientReference      string `json:"transientReference"`
			TransientImageReference string `json:"transientImageReference"`
		}
		if err := json.Unmarshal([]byte(message.Content), &placeholder); err != nil {
			expanded = append(expanded, message)
			continue
		}
		switch projectToolBaseName(toolName) {
		case projectToolGetPreviewConsoleLogs:
			result, ok := s.transientToolResults[strings.TrimSpace(placeholder.TransientReference)]
			if !ok {
				expanded = append(expanded, message)
				continue
			}
			cloned := *message
			cloned.Content = result
			expanded = append(expanded, &cloned)
			changed = true
		case projectToolInspectDevelopmentPreview, projectToolInteractDevelopmentPreview:
			preview, ok := s.transientPreviewImages[strings.TrimSpace(placeholder.TransientImageReference)]
			if !ok {
				expanded = append(expanded, message)
				continue
			}
			cloned := *message
			cloned.Content = projectEinoAssistantPreviewResultWithoutImageReference(message.Content)
			expanded = append(expanded, &cloned)
			pendingImages = append(pendingImages, projectEinoAssistantPreviewImageMessage(preview))
			changed = true
		default:
			expanded = append(expanded, message)
		}
	}
	flushImages()
	if !changed {
		return input
	}
	return expanded
}

// projectEinoAssistantPreviewImageMessage carries an inspected preview
// screenshot on its own user message. Image parts are not accepted on tool
// messages, so the tool result stays textual and the screenshot follows the
// tool group it belongs to.
func projectEinoAssistantPreviewImageMessage(preview projectEinoAssistantTransientPreviewImage) *schema.Message {
	data := preview.Base64Data
	message := schema.UserMessage("")
	message.UserInputMultiContent = []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: projectEinoAssistantPreviewImageIntro},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: preview.MIMEType}}},
	}
	return message
}

// projectEinoAssistantPreviewResultWithoutImageReference drops the transient
// bookkeeping key from the durable tool result; the screenshot it pointed at is
// attached to the model input directly.
func projectEinoAssistantPreviewResultWithoutImageReference(content string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return content
	}
	if _, ok := payload["transientImageReference"]; !ok {
		return content
	}
	delete(payload, "transientImageReference")
	encoded, err := json.Marshal(payload)
	if err != nil {
		return content
	}
	return string(encoded)
}

func (s *projectEinoAssistantRunState) SetTurnPolicy(policy projectAssistantTurnPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnPolicy = normalizeProjectAssistantTurnPolicy(policy, projectAssistantTurnProfileDebugging)
}

func (s *projectEinoAssistantRunState) TurnProfile() projectAssistantTurnProfile {
	return s.TurnPolicy().profile
}

func (s *projectEinoAssistantRunState) TurnPolicy() projectAssistantTurnPolicy {
	if s == nil {
		return projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return normalizeProjectAssistantTurnPolicy(s.turnPolicy, projectAssistantTurnProfileDebugging)
}

func (s *projectEinoAssistantRunState) SetToolPrompt(prompt string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolPrompt = strings.TrimSpace(prompt)
}

func (s *projectEinoAssistantRunState) SetToolDiscovery(discovery projectEinoAssistantToolDiscovery) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	discovery.Prompt = strings.TrimSpace(discovery.Prompt)
	digest := projectEinoAssistantDynamicToolCatalogDigest(discovery)
	if s.dynamicToolCatalogDigest != digest {
		s.selectedDynamicToolNames = map[string]struct{}{}
	}
	s.dynamicToolCatalogDigest = digest
	s.toolDiscovery = &discovery
	s.toolPrompt = discovery.Prompt
}

func (s *projectEinoAssistantRunState) SetAgentOptimizationMode(mode string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentOptimizationMode = projectEinoAssistantNormalizeOptimizationMode(mode)
}

func (s *projectEinoAssistantRunState) CodexPOCEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentOptimizationMode == projectEinoAssistantOptimizationCodexPOC
}

func (s *projectEinoAssistantRunState) DynamicToolSelected(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.selectedDynamicToolNames[projectAssistantToolKey(name)]
	return ok
}

func (s *projectEinoAssistantRunState) ApplyDynamicToolSearchResult(result string) error {
	if s == nil {
		return errors.New("assistant run state is unavailable")
	}
	var decoded projectEinoAssistantToolSearchResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return fmt.Errorf("decode tool search result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.EqualFold(strings.TrimSpace(decoded.CatalogDigest), strings.TrimSpace(s.dynamicToolCatalogDigest)) {
		return errors.New("tool search catalog changed before selection could be applied")
	}
	if s.selectedDynamicToolNames == nil {
		s.selectedDynamicToolNames = map[string]struct{}{}
	}
	available := make(map[string]struct{}, len(decoded.Matches))
	if s.toolDiscovery != nil {
		if s.toolDiscovery.IncludeCommitBridge {
			available[projectToolCommitProjectFiles] = struct{}{}
		}
		for _, tool := range s.toolDiscovery.MCPTools {
			if tool != nil {
				available[projectAssistantToolKey(tool.Spec().Name)] = struct{}{}
			}
		}
	}
	for _, match := range decoded.Matches {
		name := projectAssistantToolKey(match.Name)
		if name == "" || len(s.selectedDynamicToolNames) >= projectEinoAssistantMaxSelectedDynamicTools {
			continue
		}
		if _, ok := available[name]; !ok {
			return fmt.Errorf("tool search selected unavailable capability %q", name)
		}
		s.selectedDynamicToolNames[name] = struct{}{}
	}
	return nil
}

func (s *projectEinoAssistantRunState) ToolDiscovery() (projectEinoAssistantToolDiscovery, bool) {
	if s == nil {
		return projectEinoAssistantToolDiscovery{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolDiscovery == nil {
		return projectEinoAssistantToolDiscovery{}, false
	}
	return *s.toolDiscovery, true
}

func (s *projectEinoAssistantRunState) ToolPrompt() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolPrompt
}

func (s *projectEinoAssistantRunState) SetSessionSnapshot(snapshot projectEinoAssistantSessionSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionSnapshot = cloneProjectEinoAssistantSessionSnapshot(&snapshot)
}

func (s *projectEinoAssistantRunState) SessionSnapshot() *projectEinoAssistantSessionSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectEinoAssistantSessionSnapshot(s.sessionSnapshot)
}

func (s *projectEinoAssistantRunState) SetProjectRepositoryRef(ref string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectRepositoryRef = strings.TrimSpace(ref)
}

func (s *projectEinoAssistantRunState) SetContextResources(receipts []projectAssistantContextResourceReceipt) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectedContextResourceReceipts = cloneProjectAssistantContextResourceReceipts(receipts)
}

func (s *projectEinoAssistantRunState) ContextResources() []projectAssistantContextResourceReceipt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantContextResourceReceipts(s.selectedContextResourceReceipts)
}

func (s *projectEinoAssistantRunState) SetContentParts(parts []projectAssistantContentPart) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentParts = cloneProjectAssistantContentParts(parts)
}

func (s *projectEinoAssistantRunState) ContentParts() []projectAssistantContentPart {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantContentParts(s.contentParts)
}

func (s *projectEinoAssistantRunState) RestoreCheckpointState(state projectAssistantCheckpointState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandbox = nil
	s.sandboxMetadata = nil
	if state.Sandbox != nil {
		metadata := state.Sandbox.Metadata
		s.sandboxMetadata = &metadata
	}
	s.messages = cloneChatMessages(state.Messages)
	s.lastToolMessages = cloneChatMessages(state.LastToolMessages)
	s.catalogDigest = strings.TrimSpace(state.CatalogDigest)
	s.selectedSkillReceipts = make(map[string]projectAssistantSkillReceipt, len(state.SelectedSkillReceipts))
	s.loadedSkillReceipts = make(map[string]projectAssistantSkillReceipt, len(state.LoadedSkillReceipts)+len(state.SelectedSkillReceipts))
	s.selectedContextResourceReceipts = cloneProjectAssistantContextResourceReceipts(state.SelectedContextResourceReceipts)
	s.contentParts = cloneProjectAssistantContentParts(state.ContentParts)
	for _, receipt := range state.SelectedSkillReceipts {
		s.selectedSkillReceipts[receipt.ID] = receipt
		s.loadedSkillReceipts[receipt.ID] = receipt
	}
	for _, receipt := range state.LoadedSkillReceipts {
		s.loadedSkillReceipts[receipt.ID] = receipt
	}
	s.toolEvidence = projectEinoAssistantCollectToolEvidence(s.messages)
	s.toolCalls = cloneProjectAssistantToolCalls(state.ToolCalls)
	s.seenToolCalls = projectEinoAssistantSanitizeSeenToolCalls(state.SeenToolCalls)
	s.turn = state.Turn
	s.turnPolicy = projectAssistantTurnPolicyForCheckpoint(state)
	s.projectRepositoryRef = strings.TrimSpace(state.ProjectRepositoryRef)
	s.agentOptimizationMode = projectEinoAssistantNormalizeOptimizationMode(state.AgentOptimizationMode)
	s.dynamicToolCatalogDigest = strings.TrimSpace(state.DynamicToolCatalogDigest)
	s.selectedDynamicToolNames = projectEinoAssistantDynamicToolNameSet(state.SelectedDynamicToolNames)
	s.approvedPlan = cloneProjectAssistantApprovedPlan(state.ApprovedPlan)
	s.executionPlan = cloneProjectAssistantApprovedPlan(state.ExecutionPlan)
	s.planProgress = cloneProjectAssistantPlanSnapshot(state.PlanProgress)
	s.sourceMutationRevision = state.SourceMutationRevision
	s.checkedMutationRevision = state.CheckedMutationRevision
	s.verifiedMutationRevision = state.VerifiedMutationRevision
	s.developmentSyncRevision = state.DevelopmentSyncRevision
	s.developmentSyncStatus = strings.TrimSpace(state.DevelopmentSyncStatus)
	s.developmentSyncFailure = strings.TrimSpace(state.DevelopmentSyncFailure)
	s.developmentSyncRetry = state.DevelopmentSyncRetry
	s.commitRequired = state.CommitRequired
	s.committedMutationRevision = state.CommittedMutationRevision
	s.commitAttemptedRevision = state.CommitAttemptedRevision
	s.verifiedWorkspaceDigest = strings.TrimSpace(state.VerifiedWorkspaceDigest)
	s.committedWorkspaceDigest = strings.TrimSpace(state.CommittedWorkspaceDigest)
	s.verificationAttempted = state.VerificationAttempted
	s.verificationOutcome = strings.TrimSpace(state.VerificationOutcome)
	s.verificationSummary = strings.TrimSpace(state.VerificationSummary)
	s.verificationBlockers = append([]string(nil), state.VerificationBlockers...)
	s.previewEvidence = state.PreviewEvidence
	if s.previewEvidence.Revision != s.sourceMutationRevision {
		s.previewEvidence = projectAssistantPreviewEvidence{}
	}
	s.repeatedActionSignature = projectEinoAssistantSanitizeActionSignature(state.RepeatedActionSignature)
	s.repeatedActionToolName = projectToolBaseName(state.RepeatedActionToolName)
	s.repeatedActionCount = min(max(state.RepeatedActionCount, 0), projectEinoAssistantRepeatedActionLimit)
	s.runtimeWarmupAttempts = min(max(state.RuntimeWarmupAttempts, 0), projectEinoAssistantRepeatedActionLimit)
	s.modelCallOrdinal = max(state.ModelCallOrdinal, 0)
	s.acceptedProgressCount = min(max(state.AcceptedProgressCount, 0), projectEinoAssistantProgressReminderMaxAcceptedCount)
	s.lastAcceptedProgressModelCall = max(state.LastAcceptedProgressModelCall, 0)
	s.progressReminderSilenceTriggered = state.ProgressReminderSilenceTriggered
	if kind := strings.TrimSpace(state.ProgressReminderKind); projectEinoAssistantProgressReminderKindValid(kind) {
		attempts := min(max(state.ProgressReminderAttempts, 0), projectEinoAssistantProgressReminderMaxAttempts)
		if attempts >= projectEinoAssistantProgressReminderMaxAttempts {
			s.progressReminder = nil
			s.progressReminderAttempts = 0
		} else {
			s.progressReminder = &projectEinoAssistantProgressReminder{Kind: kind}
			s.progressReminderAttempts = attempts
		}
	} else {
		s.progressReminder = nil
		s.progressReminderAttempts = 0
		s.progressReminderSilenceTriggered = false
	}
	if s.lastAcceptedProgressModelCall > s.modelCallOrdinal {
		s.lastAcceptedProgressModelCall = 0
	}
	if s.repeatedActionSignature == "" || s.repeatedActionToolName == "" || s.repeatedActionCount == 0 {
		s.repeatedActionSignature = ""
		s.repeatedActionToolName = ""
		s.repeatedActionCount = 0
	}
	if s.checkedMutationRevision == 0 ||
		s.checkedMutationRevision != s.sourceMutationRevision ||
		s.verifiedMutationRevision != s.sourceMutationRevision ||
		strings.TrimSpace(s.verificationOutcome) != "ready" {
		s.verifiedMutationRevision = 0
	}
	s.completedReadCalls = projectEinoAssistantSanitizeCompletedReads(state.CompletedReadCalls)
	s.readFileCoverage = projectEinoAssistantRestoreReadCoverage(state.ReadFileCoverage)
	s.observedReadFilePaths = projectEinoAssistantReadPathSet(state.ObservedReadFilePaths)
	s.readFileVersions = projectEinoAssistantReadVersionMap(state.ReadFileVersions)
	s.successfulMutationPaths = projectEinoAssistantReadPathSet(state.SuccessfulMutationPaths)
	s.mutationRecoveryAttempts = projectEinoAssistantRestoreMutationRecoveryAttempts(state.MutationRecoveryAttempts)
	s.mutationRecoveryRefs = projectEinoAssistantRecoveryReferenceSet(state.MutationRecoveryRefs)
	s.mutationRecoveryIdentities = projectEinoAssistantRecoveryIdentitySnapshot(state.MutationRecoveryIdentities)
	for ref := range s.mutationRecoveryIdentities {
		if s.mutationRecoveryRefs == nil {
			s.mutationRecoveryRefs = map[string]struct{}{}
		}
		s.mutationRecoveryRefs[ref] = struct{}{}
	}
	s.sessionSnapshot = cloneProjectEinoAssistantSessionSnapshot(state.SessionSnapshot)
	s.restoredRolloutBudget = cloneProjectAssistantRolloutBudgetStatePtr(state.RolloutBudget)
}

func (s *projectEinoAssistantRunState) SetRolloutBudget(budget *projectEinoAssistantRolloutBudget) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloutBudget = budget
	s.restoredRolloutBudget = nil
}

func (s *projectEinoAssistantRunState) RolloutBudget() *projectEinoAssistantRolloutBudget {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rolloutBudget
}

func (s *projectEinoAssistantRunState) RestoredRolloutBudget() *projectAssistantRolloutBudgetState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantRolloutBudgetStatePtr(s.restoredRolloutBudget)
}

func (s *projectEinoAssistantRunState) ProjectRepositoryRef() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectRepositoryRef
}

func (s *projectEinoAssistantRunState) ApprovePlan(plan projectAssistantApprovedPlan) {
	if s == nil {
		return
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvedPlan = &normalized
	// Approval closes the inspection phase. Let the mutation phase perform one
	// fresh, bounded read of approved existing targets before ordinary edits.
	s.completedReadCalls = map[string]uint64{}
	s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
	s.readFileVersions = map[string]string{}
	s.successfulMutationPaths = map[string]struct{}{}
	s.runtimeWarmupAttempts = 0
}

func (s *projectEinoAssistantRunState) ApprovedPlan() *projectAssistantApprovedPlan {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantApprovedPlan(s.approvedPlan)
}

func (s *projectEinoAssistantRunState) SetExecutionPlan(plan projectAssistantApprovedPlan) {
	if s == nil {
		return
	}
	normalized := normalizeProjectAssistantApprovedPlan(plan)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionPlan = &normalized
}

func (s *projectEinoAssistantRunState) ClearExecutionPlan() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionPlan = nil
	s.planProgress = projectAssistantPlanSnapshot{}
}

func (s *projectEinoAssistantRunState) ExecutionPlan() *projectAssistantApprovedPlan {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantApprovedPlan(s.executionPlan)
}

func (s *projectEinoAssistantRunState) SetPlanProgress(plan projectAssistantPlanSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.planProgress = cloneProjectAssistantPlanSnapshot(plan)
}

func (s *projectEinoAssistantRunState) PlanProgress() projectAssistantPlanSnapshot {
	if s == nil {
		return projectAssistantPlanSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectAssistantPlanSnapshot(s.planProgress)
}

func (s *projectEinoAssistantRunState) ExecutionPlanComplete() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.executionPlan == nil ||
		len(s.executionPlan.Steps) == 0 ||
		len(s.planProgress.Steps) != len(s.executionPlan.Steps) {
		return false
	}
	for index, step := range s.planProgress.Steps {
		if step.Status != "completed" ||
			strings.TrimSpace(step.Content) != projectEinoAssistantTodoProgressLabel(s.executionPlan.Steps[index]) {
			return false
		}
	}
	return true
}

func (s *projectEinoAssistantRunState) ClearApprovedPlan() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvedPlan = nil
}

func (s *projectEinoAssistantRunState) RecordSourceMutation() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceMutationRevision++
	// A successful source mutation establishes progress for every prior
	// recovery target. Keep no stale same-revision failure budget after the
	// revision advances.
	s.mutationRecoveryAttempts = map[string]projectAssistantMutationRecoveryAttempt{}
	if s.developmentSyncRevision != s.sourceMutationRevision {
		s.developmentSyncRevision = s.sourceMutationRevision
		s.developmentSyncStatus = "unknown"
		s.developmentSyncFailure = "positive workspace synchronization evidence is unavailable for this mutation"
		s.signalDevelopmentSyncChangedLocked()
	}
	s.verifiedMutationRevision = 0
	s.checkedMutationRevision = 0
	s.verificationAttempted = false
	s.verificationOutcome = ""
	s.verificationSummary = ""
	s.verificationBlockers = nil
	s.previewEvidence = projectAssistantPreviewEvidence{}
	s.runtimeWarmupAttempts = 0
	s.verifiedWorkspaceDigest = ""
	s.completedReadCalls = map[string]uint64{}
	s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
}

func (s *projectEinoAssistantRunState) BeginDevelopmentSyncForNextMutation() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := s.sourceMutationRevision + 1
	s.developmentSyncRevision = revision
	s.developmentSyncStatus = "pending"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
	return revision
}

func (s *projectEinoAssistantRunState) BeginDevelopmentSyncForCurrentMutation() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceMutationRevision == 0 {
		return 0
	}
	s.developmentSyncRevision = s.sourceMutationRevision
	s.developmentSyncStatus = "pending"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
	return s.sourceMutationRevision
}

func (s *projectEinoAssistantRunState) ClaimDevelopmentSyncRetry(revision uint64) (uint64, bool) {
	if s == nil || revision == 0 {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceMutationRevision != revision ||
		s.developmentSyncRevision != revision ||
		s.developmentSyncStatus != "failed" ||
		s.developmentSyncRetry == revision {
		return 0, false
	}
	s.developmentSyncRetry = revision
	s.developmentSyncStatus = "pending"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
	return revision, true
}

func (s *projectEinoAssistantRunState) CompleteDevelopmentSync(revision uint64, syncErr error) {
	if s == nil || revision == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncRevision != revision {
		return
	}
	if syncErr != nil {
		s.developmentSyncStatus = "failed"
		s.developmentSyncFailure = strings.TrimSpace(syncErr.Error())
		if s.developmentSyncFailure == "" {
			s.developmentSyncFailure = "workspace synchronization failed"
		}
		s.signalDevelopmentSyncChangedLocked()
		return
	}
	s.developmentSyncStatus = "succeeded"
	s.developmentSyncFailure = ""
	s.signalDevelopmentSyncChangedLocked()
}

func (s *projectEinoAssistantRunState) signalDevelopmentSyncChangedLocked() {
	if s.developmentSyncChanged != nil {
		close(s.developmentSyncChanged)
	}
	s.developmentSyncChanged = make(chan struct{})
}

// WaitForDevelopmentSync boundedly observes the current revision rather than
// making verification race the background synchronization goroutine.
func (s *projectEinoAssistantRunState) WaitForDevelopmentSync(
	ctx context.Context,
	revision uint64,
	timeout time.Duration,
) (string, string) {
	if s == nil || revision == 0 || timeout <= 0 {
		return s.DevelopmentSyncEvidence(revision)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		status := strings.TrimSpace(s.developmentSyncStatus)
		failure := strings.TrimSpace(s.developmentSyncFailure)
		if s.developmentSyncRevision != revision {
			status = "unknown"
			failure = "positive workspace synchronization evidence is unavailable for this mutation"
		}
		if status == "" {
			status = "unknown"
		}
		if status != "pending" {
			s.mu.Unlock()
			return status, failure
		}
		if s.developmentSyncChanged == nil {
			s.developmentSyncChanged = make(chan struct{})
		}
		changed := s.developmentSyncChanged
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return s.DevelopmentSyncEvidence(revision)
		case <-timer.C:
			return s.DevelopmentSyncEvidence(revision)
		case <-changed:
		}
	}
}

func (s *projectEinoAssistantRunState) DevelopmentSyncEvidence(revision uint64) (string, string) {
	if s == nil || revision == 0 {
		return "unknown", "positive workspace synchronization evidence is unavailable"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncRevision != revision {
		return "unknown", "positive workspace synchronization evidence is unavailable for this mutation"
	}
	status := strings.TrimSpace(s.developmentSyncStatus)
	if status == "" {
		status = "unknown"
	}
	return status, strings.TrimSpace(s.developmentSyncFailure)
}

func (s *projectEinoAssistantRunState) RecordDevelopmentVerification(ready bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verificationAttempted = true
	if ready {
		s.verificationOutcome = "ready"
		s.checkedMutationRevision = s.sourceMutationRevision
	} else {
		s.verificationOutcome = "not_ready"
		s.checkedMutationRevision = 0
	}
	s.verificationSummary = ""
	s.verificationBlockers = nil
	if ready && s.sourceMutationRevision > 0 {
		s.verifiedMutationRevision = s.sourceMutationRevision
		return
	}
	s.verifiedMutationRevision = 0
	if !ready {
		s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
			Kind:   projectEinoAssistantProgressReminderVerification,
			Detail: "runtime verification did not pass",
		})
	}
}

func (s *projectEinoAssistantRunState) RecordDevelopmentVerificationResult(content string) {
	if s == nil {
		return
	}
	var payload projectAssistantRuntimeVerificationResult
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &payload) != nil {
		s.RecordDevelopmentVerification(false)
		return
	}
	rawStatus := strings.TrimSpace(payload.Status)
	status := strings.ToLower(rawStatus)
	if rawStatus == "" {
		status = "not_ready"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verificationAttempted = true
	s.checkedMutationRevision = payload.CheckedMutationRevision
	s.verificationSummary = strings.TrimSpace(payload.Summary)
	s.verificationBlockers = append([]string(nil), payload.Blockers...)
	if projectEinoAssistantRuntimeVerificationDisposition(payload) != projectEinoAssistantVerificationOperational {
		s.runtimeWarmupAttempts = 0
	}
	if rawStatus == "ready" &&
		payload.CheckedMutationRevision > 0 &&
		payload.CheckedMutationRevision == s.sourceMutationRevision {
		s.verifiedMutationRevision = payload.CheckedMutationRevision
		s.verificationOutcome = "ready"
		return
	}
	s.verifiedMutationRevision = 0
	if rawStatus == "ready" {
		s.verificationOutcome = "stale"
		s.verificationBlockers = append(
			s.verificationBlockers,
			fmt.Sprintf(
				"verification checked workspace revision %d, but the current revision is %d",
				payload.CheckedMutationRevision,
				s.sourceMutationRevision,
			),
		)
		s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
			Kind:   projectEinoAssistantProgressReminderVerification,
			Detail: "verification is stale for the current workspace revision",
		})
		return
	}
	if status == "ready" {
		s.verificationOutcome = "unavailable"
		s.verificationBlockers = append(
			s.verificationBlockers,
			"verification returned a non-canonical ready status",
		)
		s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
			Kind:   projectEinoAssistantProgressReminderVerification,
			Detail: "verification returned an unavailable status",
		})
		return
	}
	s.verificationOutcome = status
	if status != "ready" {
		s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
			Kind:   projectEinoAssistantProgressReminderVerification,
			Detail: strings.TrimSpace(payload.Summary),
		})
	}
}

func (s *projectEinoAssistantRunState) CompletionEvidence() projectAssistantCompletionEvidence {
	if s == nil {
		return projectAssistantCompletionEvidence{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	planDefined := s.executionPlan != nil
	planComplete := planDefined &&
		len(s.executionPlan.Steps) > 0 &&
		len(s.planProgress.Steps) == len(s.executionPlan.Steps)
	for index, step := range s.planProgress.Steps {
		if !planComplete ||
			step.Status != "completed" ||
			strings.TrimSpace(step.Content) != projectEinoAssistantTodoProgressLabel(s.executionPlan.Steps[index]) {
			planComplete = false
			break
		}
	}
	latestVerified := s.sourceMutationRevision > 0 &&
		s.verifiedMutationRevision == s.sourceMutationRevision
	outcome := strings.TrimSpace(s.verificationOutcome)
	if outcome == "" && s.verificationAttempted {
		outcome = "not_ready"
	}
	if outcome == "" {
		outcome = "not_run"
	}
	evidence := projectAssistantCompletionEvidence{
		PlanDefined:                  planDefined,
		PlanComplete:                 planComplete,
		SourceMutationRevision:       s.sourceMutationRevision,
		VerifiedMutationRevision:     s.verifiedMutationRevision,
		LatestMutationVerified:       latestVerified,
		CommitRequired:               s.commitRequired,
		CommittedMutationRevision:    s.committedMutationRevision,
		LatestMutationCommitted:      !s.commitRequired || (s.sourceMutationRevision > 0 && s.committedMutationRevision == s.sourceMutationRevision),
		VerificationOutcome:          outcome,
		VerificationSummary:          strings.TrimSpace(s.verificationSummary),
		Blockers:                     append([]string(nil), s.verificationBlockers...),
		PreviewEvidenceRevision:      s.previewEvidence.Revision,
		PreviewEvidenceScope:         s.previewEvidence.Scope,
		PreviewEvidenceOutcome:       s.previewEvidence.Outcome,
		PreviewRenderedStateObserved: s.previewEvidence.RenderedStateObserved,
		PreviewInteractionVerified:   s.previewEvidence.InteractionVerified,
		PreviewAssertionsObserved:    s.previewEvidence.AssertionsObserved,
		PreviewAssertionsPassed:      s.previewEvidence.AssertionsPassed,
		PreviewAssertionCount:        s.previewEvidence.AssertionCount,
		PreviewFailedAssertionCount:  s.previewEvidence.FailedAssertionCount,
	}
	if outcome == "provisioning" {
		evidence.Blockers = append(evidence.Blockers, "runtime provisioning")
	}
	return evidence
}

func (s *projectEinoAssistantRunState) SourceMutationVerified() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision > 0 &&
		s.verifiedMutationRevision == s.sourceMutationRevision
}

func (s *projectEinoAssistantRunState) RecordSourceCommit(workspaceDigest string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaceDigest = strings.TrimSpace(workspaceDigest)
	if s.sourceMutationRevision > 0 && workspaceDigest != "" {
		s.commitAttemptedRevision = s.sourceMutationRevision
		s.committedMutationRevision = s.sourceMutationRevision
		s.committedWorkspaceDigest = workspaceDigest
		// Preserve a verification claim only when commit persisted the exact
		// same workspace bundle. Otherwise fail closed instead of allowing two
		// unrelated digests to appear as one completed state.
		if s.verifiedWorkspaceDigest != workspaceDigest {
			s.verifiedMutationRevision = 0
			s.checkedMutationRevision = 0
			s.verificationAttempted = false
			s.verificationOutcome = ""
			s.verificationSummary = ""
			s.verificationBlockers = nil
			s.verifiedWorkspaceDigest = ""
		}
	}
}

func (s *projectEinoAssistantRunState) RecordSourceCommitAttempt(revision uint64) {
	if s == nil || revision == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision > s.commitAttemptedRevision {
		s.commitAttemptedRevision = revision
	}
}

func (s *projectEinoAssistantRunState) RecordVerifiedWorkspaceDigest(digest string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sourceMutationRevision > 0 && s.verifiedMutationRevision == s.sourceMutationRevision {
		s.verifiedWorkspaceDigest = strings.TrimSpace(digest)
	}
}

func (s *projectEinoAssistantRunState) VerifiedWorkspaceDigestMatches(digest string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision > 0 &&
		s.verifiedMutationRevision == s.sourceMutationRevision &&
		s.verifiedWorkspaceDigest != "" &&
		s.verifiedWorkspaceDigest == strings.TrimSpace(digest)
}

func (s *projectEinoAssistantRunState) VerifiedWorkspaceDigest() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifiedWorkspaceDigest
}

func (s *projectEinoAssistantRunState) RecordVerificationBindingFailure(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifiedMutationRevision = 0
	s.verificationOutcome = "not_ready"
	s.verificationBlockers = append(s.verificationBlockers, strings.TrimSpace(reason))
	s.verifiedWorkspaceDigest = ""
	s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
		Kind:   projectEinoAssistantProgressReminderVerification,
		Detail: strings.TrimSpace(reason),
	})
}

func (s *projectEinoAssistantRunState) SourceMutationRevisions() (uint64, uint64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceMutationRevision, s.verifiedMutationRevision
}

// RecordCompletedRead records a successful read dispatch and reports whether
// it is fresh for the current source revision. Replaying the same read is
// intentionally still durable and dispatched, but must not count as new
// implementation progress.
func (s *projectEinoAssistantRunState) RecordCompletedRead(name, arguments string) bool {
	return s.recordCompletedRead(name, arguments, "")
}

// RecordCompletedReadResult is the result-aware variant used by the
// filesystem telemetry boundary. A complete read's opaque version is part of
// freshness, so an externally changed file can count as new evidence even
// when the model repeats the same path/range arguments.
func (s *projectEinoAssistantRunState) RecordCompletedReadResult(name, arguments, result string) bool {
	return s.recordCompletedRead(name, arguments, result)
}

func (s *projectEinoAssistantRunState) recordCompletedRead(name, arguments, result string) bool {
	if s == nil {
		return false
	}
	signature := projectEinoAssistantReadCompletionSignature(name, arguments, result)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completedReadCalls == nil {
		s.completedReadCalls = map[string]uint64{}
	}
	revision := s.sourceMutationRevision + 1
	if previous, exists := s.completedReadCalls[signature]; exists && previous == revision {
		return false
	}
	if len(s.completedReadCalls) >= projectEinoAssistantMaxTrackedReads {
		projectEinoAssistantEvictCompletedRead(s.completedReadCalls)
	}
	s.completedReadCalls[signature] = revision
	return true
}

func projectEinoAssistantEvictCompletedRead(completed map[string]uint64) {
	if len(completed) == 0 {
		return
	}
	// The checkpoint format historically stored only a map. Deterministic
	// eviction keeps that contract migration-free while ensuring the bounded
	// guard continues accepting later distinct reads instead of becoming a
	// permanent no-progress latch after the first 128 signatures.
	var evict string
	for signature := range completed {
		if evict == "" || signature < evict {
			evict = signature
		}
	}
	delete(completed, evict)
}

func projectEinoAssistantReadCompletionSignature(name, arguments, result string) string {
	version := ""
	if projectToolBaseName(name) == projectToolReadFile {
		var evidence struct {
			Version  string `json:"version"`
			Complete bool   `json:"complete"`
		}
		if json.Unmarshal([]byte(result), &evidence) == nil && evidence.Complete {
			version = strings.TrimSpace(evidence.Version)
		}
	}
	if version == "" {
		return projectEinoAssistantToolCallSignature(name, arguments)
	}
	return projectEinoAssistantToolCallSignature(name, arguments+"\x00version="+version)
}

func (s *projectEinoAssistantRunState) RecordReadFileRange(path string, start, end int) {
	if s == nil || path == "" || start <= 0 || end < start {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFileCoverage == nil {
		s.readFileCoverage = map[string][]projectEinoAssistantLineRange{}
	}
	ranges := append(s.readFileCoverage[path], projectEinoAssistantLineRange{start: start, end: end})
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})
	merged := make([]projectEinoAssistantLineRange, 0, len(ranges))
	for _, lineRange := range ranges {
		if len(merged) == 0 ||
			(merged[len(merged)-1].end < projectEinoAssistantReadThroughEOF &&
				lineRange.start > merged[len(merged)-1].end+1) {
			merged = append(merged, lineRange)
			continue
		}
		merged[len(merged)-1].end = max(merged[len(merged)-1].end, lineRange.end)
	}
	otherRanges := 0
	for trackedPath, trackedRanges := range s.readFileCoverage {
		if trackedPath != path {
			otherRanges += len(trackedRanges)
		}
	}
	available := max(projectEinoAssistantMaxTrackedReads-otherRanges, 0)
	if len(merged) > available {
		merged = merged[:available]
	}
	s.readFileCoverage[path] = merged
}

func (s *projectEinoAssistantRunState) InvalidateObservedReadFile(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateObservedReadFileLocked(path)
}

func (s *projectEinoAssistantRunState) invalidateObservedReadFileLocked(path string) {
	s.completedReadCalls = map[string]uint64{}
	delete(s.readFileCoverage, path)
	delete(s.observedReadFilePaths, path)
	delete(s.readFileVersions, path)
}

func (s *projectEinoAssistantRunState) RecordObservedReadFile(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observedReadFilePaths == nil {
		s.observedReadFilePaths = map[string]struct{}{}
	}
	s.observedReadFilePaths[path] = struct{}{}
}

// RecordObservedReadFileVersion records complete read evidence for one path.
// A version is intentionally stored separately from path presence so a
// partial/range read can never authorize a stale-sensitive mutation.
func (s *projectEinoAssistantRunState) RecordObservedReadFileVersion(path, version string) {
	if s == nil || strings.TrimSpace(version) == "" {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFileVersions == nil {
		s.readFileVersions = map[string]string{}
	}
	if len(s.readFileVersions) >= projectEinoAssistantMaxTrackedReads {
		if _, exists := s.readFileVersions[path]; !exists {
			return
		}
	}
	s.readFileVersions[path] = strings.TrimSpace(version)
	if attempt, ok := s.mutationRecoveryAttempts[path]; ok &&
		attempt.SourceRevision == s.sourceMutationRevision && attempt.Failures > 0 && !attempt.Blocked {
		// A complete read is the only evidence that authorizes a retry after a
		// mutation failure. Partial reads never record a version here.
		attempt.Reread = true
		s.mutationRecoveryAttempts[path] = attempt
	}
	if s.observedReadFilePaths == nil {
		s.observedReadFilePaths = map[string]struct{}{}
	}
	s.observedReadFilePaths[path] = struct{}{}
}

func (s *projectEinoAssistantRunState) ReadFileVersion(path string) string {
	if s == nil {
		return ""
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.readFileVersions[path])
}

func (s *projectEinoAssistantRunState) ObservedReadFilePaths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.observedReadFilePaths))
	for path := range s.observedReadFilePaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (s *projectEinoAssistantRunState) RecordSuccessfulMutationPath(path string) {
	if s == nil {
		return
	}
	path, err := workspace.CleanProjectPath(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.successfulMutationPaths == nil {
		s.successfulMutationPaths = map[string]struct{}{}
	}
	s.successfulMutationPaths[path] = struct{}{}
	// The path has made real mutation progress. Remove only the matching
	// target's recovery budget; RecordSourceMutation clears all targets when
	// the durable source revision advances.
	delete(s.mutationRecoveryAttempts, path)
}

func (s *projectEinoAssistantRunState) SuccessfulMutationPaths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return projectEinoAssistantObservedReadPaths(s.successfulMutationPaths)
}

func (s *projectEinoAssistantRunState) ClearSuccessfulMutationPaths() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successfulMutationPaths = map[string]struct{}{}
}

// RecordMutationFailure records one failed workspace mutation at the current
// source revision. A later complete read marks the target as eligible for one
// deterministic repair attempt. The second failed attempt for the same
// canonical target and revision is marked blocked; lifecycle checks turn that
// marker into a typed terminal error before another model sample.
func (s *projectEinoAssistantRunState) RecordMutationFailure(name string, args map[string]any) (projectAssistantMutationRecoveryAttempt, bool) {
	if s == nil {
		return projectAssistantMutationRecoveryAttempt{}, false
	}
	identity, ok := projectAssistantMutationRecoveryIdentityFromTool(name, args)
	if !ok {
		return projectAssistantMutationRecoveryAttempt{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutationRecoveryAttempts == nil {
		s.mutationRecoveryAttempts = map[string]projectAssistantMutationRecoveryAttempt{}
	}
	attempt := s.mutationRecoveryAttempts[identity.Target]
	if attempt.Target == "" || attempt.SourceRevision != s.sourceMutationRevision ||
		!projectAssistantMutationRecoveryIdentityCompatible(
			projectAssistantMutationRecoveryIdentity{Operation: attempt.Operation, Target: attempt.Target},
			identity,
		) {
		attempt = projectAssistantMutationRecoveryAttempt{
			Operation:      identity.Operation,
			Target:         identity.Target,
			SourceRevision: s.sourceMutationRevision,
		}
	}
	attempt.Operation = identity.Operation
	attempt.Target = identity.Target
	attempt.SourceRevision = s.sourceMutationRevision
	attempt.Failures = min(max(attempt.Failures, 0)+1, projectEinoAssistantMutationRecoveryFailureLimit)
	attempt.Reread = false
	attempt.Blocked = attempt.Failures >= projectEinoAssistantMutationRecoveryFailureLimit
	if _, exists := s.mutationRecoveryAttempts[identity.Target]; !exists &&
		len(s.mutationRecoveryAttempts) >= projectEinoAssistantMaxTrackedMutationRecoveryAttempts {
		// Keep blocked entries at the current revision forever. They are the
		// terminal guard that prevents another model sample, so evicting one
		// merely to admit a new target would reopen the recovery loop. Prefer
		// stale entries, then unblocked entries, using a stable target tie-break.
		if !s.evictMutationRecoveryAttemptLocked() {
			// The existing current-revision blocked entries already provide the
			// terminal guard. Do not grow the map or discard that evidence when
			// this new target cannot be tracked within the bound.
			s.invalidateObservedReadFileLocked(identity.Target)
			return projectAssistantMutationRecoveryAttempt{}, false
		}
	}
	s.mutationRecoveryAttempts[identity.Target] = attempt
	// Never let a failed mutation reuse the expectedVersion from the prior
	// attempt. The next write must carry evidence from a fresh complete read.
	s.invalidateObservedReadFileLocked(identity.Target)
	return attempt, attempt.Blocked
}

// evictMutationRecoveryAttemptLocked makes room for one new target while
// preserving every blocked attempt at the current source revision. It must be
// called with s.mu held and returns false when no safe entry can be evicted.
func (s *projectEinoAssistantRunState) evictMutationRecoveryAttemptLocked() bool {
	if len(s.mutationRecoveryAttempts) < projectEinoAssistantMaxTrackedMutationRecoveryAttempts {
		return true
	}
	currentRevision := s.sourceMutationRevision
	candidate := ""
	candidateStale := false
	for target, attempt := range s.mutationRecoveryAttempts {
		stale := attempt.SourceRevision != currentRevision
		if !stale && attempt.Blocked && attempt.Failures >= projectEinoAssistantMutationRecoveryFailureLimit {
			continue
		}
		if candidate == "" ||
			(stale && !candidateStale) ||
			(stale == candidateStale && target < candidate) {
			candidate = target
			candidateStale = stale
		}
	}
	if candidate == "" {
		return false
	}
	delete(s.mutationRecoveryAttempts, candidate)
	return true
}

func (s *projectEinoAssistantRunState) MutationRecoveryBlockedError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var selected projectAssistantMutationRecoveryAttempt
	for _, attempt := range s.mutationRecoveryAttempts {
		if !attempt.Blocked || attempt.Failures < projectEinoAssistantMutationRecoveryFailureLimit ||
			attempt.SourceRevision != s.sourceMutationRevision {
			continue
		}
		if selected.Target == "" || attempt.Target < selected.Target {
			selected = attempt
		}
	}
	if selected.Target == "" {
		return nil
	}
	return newProjectEinoAssistantRecoveryBlockedError(selected, s.verifiedMutationRevision)
}

// RecordMutationRecoveryReference records a server-generated public action ID
// that belongs to this run. A model-supplied recoveryOf value is accepted only
// when it is present in this set; paths are deliberately never used as a
// fallback correlation key.
func (s *projectEinoAssistantRunState) RecordMutationRecoveryReference(callID string) string {
	return s.recordMutationRecoveryReference(callID, projectAssistantMutationRecoveryIdentity{}, false)
}

// RecordMutationRecoveryReferenceForMutation records a failed mutation action
// reference together with its server-derived operation family and canonical
// logical target. The identity is presentation-only and does not alter
// authorization or the mutation itself.
func (s *projectEinoAssistantRunState) RecordMutationRecoveryReferenceForMutation(callID, name string, args map[string]any) string {
	identity, ok := projectAssistantMutationRecoveryIdentityFromTool(name, args)
	return s.recordMutationRecoveryReference(callID, identity, ok)
}

func (s *projectEinoAssistantRunState) recordMutationRecoveryReference(callID string, identity projectAssistantMutationRecoveryIdentity, hasIdentity bool) string {
	if s == nil {
		return ""
	}
	ref := projectAssistantActionPublicID(callID)
	if ref == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutationRecoveryRefs == nil {
		s.mutationRecoveryRefs = map[string]struct{}{}
	}
	if len(s.mutationRecoveryRefs) >= 128 {
		s.mutationRecoveryRefs = map[string]struct{}{}
		s.mutationRecoveryIdentities = map[string]projectAssistantMutationRecoveryIdentity{}
	}
	s.mutationRecoveryRefs[ref] = struct{}{}
	if hasIdentity {
		if s.mutationRecoveryIdentities == nil {
			s.mutationRecoveryIdentities = map[string]projectAssistantMutationRecoveryIdentity{}
		}
		s.mutationRecoveryIdentities[ref] = identity
	}
	return ref
}

func (s *projectEinoAssistantRunState) IsMutationRecoveryReference(ref string) bool {
	if s == nil {
		return false
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.mutationRecoveryRefs[ref]
	return ok
}

// IsMutationRecoveryReferenceCompatible validates both the server-issued
// reference and the retry's operation family/canonical target. No path-only
// fallback is permitted when the reference is absent or lacks identity.
func (s *projectEinoAssistantRunState) IsMutationRecoveryReferenceCompatible(ref, name string, args map[string]any) bool {
	if s == nil {
		return false
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	current, ok := projectAssistantMutationRecoveryIdentityFromTool(name, args)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prior, ok := s.mutationRecoveryIdentities[ref]
	if !ok {
		return false
	}
	return projectAssistantMutationRecoveryIdentityCompatible(prior, current)
}

func (s *projectEinoAssistantRunState) MutationRecoveryReferences() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return projectEinoAssistantRecoveryReferences(s.mutationRecoveryRefs)
}

func (s *projectEinoAssistantRunState) RuntimeWarmupAttempts() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeWarmupAttempts
}

func (s *projectEinoAssistantRunState) RecordCompletedAction(name, arguments string) {
	if s == nil {
		return
	}
	name = projectToolBaseName(name)
	signature := projectEinoAssistantToolCallSignature(name, arguments)
	s.mu.Lock()
	defer s.mu.Unlock()
	if signature == s.repeatedActionSignature {
		s.repeatedActionCount++
	} else {
		s.repeatedActionSignature = signature
		s.repeatedActionToolName = name
		s.repeatedActionCount = 1
	}
	// Keep the repeated signature/count as informational replay and audit
	// context; it never terminates a model turn.
}

func (s *projectEinoAssistantRunState) RepeatedCompletedAction() (string, int) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repeatedActionToolName, s.repeatedActionCount
}

func (s *projectEinoAssistantRunState) NextModelCallOrdinal() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelCallOrdinal++
	if !s.progressReminderSilenceTriggered {
		if s.acceptedProgressCount == 0 {
			if s.modelCallOrdinal >= projectEinoAssistantProgressReminderSilenceModelCalls {
				s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
					Kind:   projectEinoAssistantProgressReminderSilence,
					Detail: "several model calls have passed without an accepted progress update",
				})
				s.progressReminderSilenceTriggered = true
			}
		} else if s.modelCallOrdinal-s.lastAcceptedProgressModelCall >= projectEinoAssistantProgressReminderSilenceModelCalls {
			s.queueProgressReminderLocked(projectEinoAssistantProgressReminder{
				Kind:   projectEinoAssistantProgressReminderSilence,
				Detail: "several model calls have passed without an accepted progress update",
			})
			s.progressReminderSilenceTriggered = true
		}
	}
	return s.modelCallOrdinal
}

func (s *projectEinoAssistantRunState) CurrentModelCallOrdinal() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelCallOrdinal
}

// InvalidateModelContext marks the current model-visible history as replaced.
// This mirrors Codex clearing its reference context baseline after compaction.
// The generation is run-local state; durable conversation/checkpoint state is
// intentionally unchanged.
func (s *projectEinoAssistantRunState) InvalidateModelContext() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextGeneration++
}

func (s *projectEinoAssistantRunState) ModelContextGeneration() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextGeneration
}

func (s *projectEinoAssistantRunState) RecordModelInput(messages []chatMessage) {
	if s == nil {
		return
	}
	messages = cloneChatMessages(messages)
	for index := range messages {
		if messages[index].Role == "tool" &&
			projectToolBaseName(messages[index].Name) == projectToolGetPreviewConsoleLogs {
			messages[index].Content = projectEinoAssistantPersistentToolResult(
				messages[index].Name,
				messages[index].Content,
			)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = messages
}

func (s *projectEinoAssistantRunState) RecordAssistantReply(reply projectAssistantReply) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(reply.ToolCalls) > 0 {
		ensureProjectToolCallIDs(reply.ToolCalls, s.modelCallOrdinal)
		s.toolCalls = cloneProjectAssistantToolCalls(reply.ToolCalls)
		for _, tc := range reply.ToolCalls {
			sig := projectEinoAssistantToolCallSignature(tc.Function.Name, tc.Function.Arguments)
			if _, exists := s.seenToolCalls[sig]; !exists && len(s.seenToolCalls) >= projectEinoAssistantMaxTrackedReads {
				projectEinoAssistantEvictSeenToolCall(s.seenToolCalls)
			}
			s.seenToolCalls[sig]++
		}
		s.messages = append(s.messages, chatMessage{
			Role:      "assistant",
			Content:   reply.Content,
			ToolCalls: cloneProjectAssistantToolCalls(reply.ToolCalls),
		})
		s.turn++
		return
	}
	if strings.TrimSpace(reply.Content) != "" {
		s.messages = append(s.messages, chatMessage{
			Role:    "assistant",
			Content: reply.Content,
		})
	}
}

func (s *projectEinoAssistantRunState) RecordSteeringInput(content string) {
	if s == nil || strings.TrimSpace(content) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, chatMessage{Role: "user", Content: strings.TrimSpace(content)})
}

// DeferSteeringOnceAfterCompaction preserves Codex's continuation ordering:
// when a completed tool result forced the next model step, that continuation
// gets the first request in the new context window. Persisted steering remains
// queued for the following model-safe boundary.
func (s *projectEinoAssistantRunState) DeferSteeringOnceAfterCompaction() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deferSteeringOnce = true
}

func (s *projectEinoAssistantRunState) TakeSteeringDeferral() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deferred := s.deferSteeringOnce
	s.deferSteeringOnce = false
	return deferred
}

func (s *projectEinoAssistantRunState) ModelMessages() []chatMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneChatMessages(s.messages)
}

func (s *projectEinoAssistantRunState) RecordToolMessage(msg chatMessage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := cloneChatMessages([]chatMessage{msg})[0]
	s.messages = append(s.messages, cloned)
	s.lastToolMessages = []chatMessage{cloned}
	s.toolEvidence = append(s.toolEvidence, cloned)
	s.recordPreviewEvidenceLocked(cloned)
	if len(s.toolEvidence) > projectEinoAssistantClosingEvidenceMaxItems {
		s.toolEvidence = cloneChatMessages(s.toolEvidence[len(s.toolEvidence)-projectEinoAssistantClosingEvidenceMaxItems:])
	}
}

func (s *projectEinoAssistantRunState) recordPreviewEvidenceLocked(message chatMessage) {
	toolName := projectToolBaseName(message.Name)
	if toolName != projectToolInspectDevelopmentPreview && toolName != projectToolInteractDevelopmentPreview {
		return
	}
	var result struct {
		Status              string                                             `json:"status"`
		FailureKind         string                                             `json:"failureKind,omitempty"`
		EvidenceScope       string                                             `json:"evidenceScope,omitempty"`
		InteractionEvidence bool                                               `json:"interactionEvidence"`
		Assertions          []projectAssistantPreviewInspectionAssertionResult `json:"assertions,omitempty"`
		Steps               []projectAssistantPreviewInteractionStepResult     `json:"steps,omitempty"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &result) != nil {
		return
	}
	evidence := projectAssistantPreviewEvidence{
		Revision:       s.sourceMutationRevision,
		Scope:          strings.TrimSpace(result.EvidenceScope),
		AssertionCount: len(result.Assertions),
	}
	for _, assertion := range result.Assertions {
		if !assertion.Passed {
			evidence.FailedAssertionCount++
		}
	}
	evidence.AssertionsObserved = evidence.AssertionCount > 0
	evidence.AssertionsPassed = evidence.AssertionsObserved && evidence.FailedAssertionCount == 0

	status := strings.TrimSpace(result.Status)
	if status != "succeeded" {
		evidence.Outcome = "failed"
		if strings.TrimSpace(result.FailureKind) == "not_current" {
			evidence.Outcome = "stale"
		}
		// Interaction/assertion failures happen after navigation and preserve
		// evidence that the application document rendered. Navigation,
		// availability, and freshness failures do not.
		evidence.RenderedStateObserved = result.FailureKind == "interaction" || result.FailureKind == "assertion"
		s.previewEvidence = evidence
		return
	}

	evidence.RenderedStateObserved = true
	evidence.Outcome = "rendered_verified"
	if toolName == projectToolInteractDevelopmentPreview {
		allStepsApplied := len(result.Steps) > 0
		for _, step := range result.Steps {
			allStepsApplied = allStepsApplied && step.Applied
		}
		evidence.InteractionVerified = result.InteractionEvidence && allStepsApplied
		if evidence.InteractionVerified {
			evidence.Outcome = "interactions_verified"
		}
	} else if s.previewEvidence.Revision == s.sourceMutationRevision && s.previewEvidence.InteractionVerified {
		// A later read-only observation may strengthen rendered/assertion
		// evidence, but it cannot erase a successful interaction receipt for
		// the same source revision.
		evidence.InteractionVerified = true
		evidence.Outcome = "interactions_verified"
	}
	s.previewEvidence = evidence
}

func (s *projectEinoAssistantRunState) PermissionBarrierActive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissionBarrier
}

func (s *projectEinoAssistantRunState) TryStartPermissionBarrier() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.permissionBarrier {
		return false
	}
	s.permissionBarrier = true
	return true
}

func (s *projectEinoAssistantRunState) ToolCallByID(callID, name, arguments string) (chatToolCall, int, []chatToolCall) {
	if s == nil {
		return projectEinoAssistantFallbackToolCall(callID, name, arguments), 0, []chatToolCall{projectEinoAssistantFallbackToolCall(callID, name, arguments)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	toolCalls := cloneProjectAssistantToolCalls(s.toolCalls)
	for i, tc := range toolCalls {
		if strings.TrimSpace(callID) != "" && tc.ID == callID {
			return tc, i, toolCalls
		}
	}
	tc := projectEinoAssistantFallbackToolCall(callID, name, arguments)
	if len(toolCalls) == 0 {
		toolCalls = []chatToolCall{tc}
	}
	return tc, 0, toolCalls
}

func (s *projectEinoAssistantRunState) CheckpointState() projectAssistantCheckpointState {
	if s == nil {
		return projectAssistantCheckpointState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rolloutBudget := cloneProjectAssistantRolloutBudgetStatePtr(s.restoredRolloutBudget)
	if s.rolloutBudget != nil {
		rolloutBudget = s.rolloutBudget.Snapshot()
	}
	progressReminderKind := ""
	if s.progressReminder != nil {
		progressReminderKind = s.progressReminder.Kind
	}
	progressReminderAttempts := 0
	if s.progressReminder != nil {
		progressReminderAttempts = min(max(s.progressReminderAttempts, 0), projectEinoAssistantProgressReminderMaxAttempts-1)
	}
	selectedSkillReceipts := make([]projectAssistantSkillReceipt, 0, len(s.selectedSkillReceipts))
	for _, receipt := range s.selectedSkillReceipts {
		selectedSkillReceipts = append(selectedSkillReceipts, receipt)
	}
	loadedSkillReceipts := make([]projectAssistantSkillReceipt, 0, len(s.loadedSkillReceipts))
	for _, receipt := range s.loadedSkillReceipts {
		loadedSkillReceipts = append(loadedSkillReceipts, receipt)
	}
	return projectAssistantCheckpointState{
		Messages:                         projectAssistantBoundCheckpointMessages(s.messages),
		LastToolMessages:                 projectAssistantBoundCheckpointMessages(s.lastToolMessages),
		CatalogDigest:                    s.catalogDigest,
		SelectedSkillReceipts:            cloneProjectAssistantSkillReceipts(selectedSkillReceipts),
		LoadedSkillReceipts:              cloneProjectAssistantSkillReceipts(loadedSkillReceipts),
		SelectedContextResourceReceipts:  cloneProjectAssistantContextResourceReceipts(s.selectedContextResourceReceipts),
		ContentParts:                     cloneProjectAssistantContentParts(s.contentParts),
		ToolCalls:                        cloneProjectAssistantToolCalls(s.toolCalls),
		SeenToolCalls:                    projectEinoAssistantSanitizeSeenToolCalls(s.seenToolCalls),
		Turn:                             s.turn,
		ProjectRepositoryRef:             strings.TrimSpace(s.projectRepositoryRef),
		AgentOptimizationMode:            s.agentOptimizationMode,
		DynamicToolCatalogDigest:         s.dynamicToolCatalogDigest,
		SelectedDynamicToolNames:         projectEinoAssistantSortedDynamicToolNames(s.selectedDynamicToolNames),
		TurnPolicy:                       projectAssistantCheckpointTurnPolicyForPolicy(s.turnPolicy),
		ApprovedPlan:                     cloneProjectAssistantApprovedPlan(s.approvedPlan),
		ExecutionPlan:                    cloneProjectAssistantApprovedPlan(s.executionPlan),
		PlanProgress:                     cloneProjectAssistantPlanSnapshot(s.planProgress),
		SourceMutationRevision:           s.sourceMutationRevision,
		VerifiedMutationRevision:         s.verifiedMutationRevision,
		DevelopmentSyncRevision:          s.developmentSyncRevision,
		DevelopmentSyncStatus:            strings.TrimSpace(s.developmentSyncStatus),
		DevelopmentSyncFailure:           strings.TrimSpace(s.developmentSyncFailure),
		DevelopmentSyncRetry:             s.developmentSyncRetry,
		CommitRequired:                   s.commitRequired,
		CommittedMutationRevision:        s.committedMutationRevision,
		CommitAttemptedRevision:          s.commitAttemptedRevision,
		VerifiedWorkspaceDigest:          s.verifiedWorkspaceDigest,
		CommittedWorkspaceDigest:         s.committedWorkspaceDigest,
		CheckedMutationRevision:          s.checkedMutationRevision,
		VerificationAttempted:            s.verificationAttempted,
		VerificationOutcome:              strings.TrimSpace(s.verificationOutcome),
		VerificationSummary:              strings.TrimSpace(s.verificationSummary),
		VerificationBlockers:             append([]string(nil), s.verificationBlockers...),
		PreviewEvidence:                  s.previewEvidence,
		RepeatedActionSignature:          s.repeatedActionSignature,
		RepeatedActionToolName:           s.repeatedActionToolName,
		RepeatedActionCount:              s.repeatedActionCount,
		RuntimeWarmupAttempts:            s.runtimeWarmupAttempts,
		ModelCallOrdinal:                 s.modelCallOrdinal,
		AcceptedProgressCount:            s.acceptedProgressCount,
		LastAcceptedProgressModelCall:    s.lastAcceptedProgressModelCall,
		ProgressReminderKind:             progressReminderKind,
		ProgressReminderAttempts:         progressReminderAttempts,
		ProgressReminderSilenceTriggered: s.progressReminderSilenceTriggered,
		CompletedReadCalls:               projectEinoAssistantCloneCompletedReads(s.completedReadCalls),
		ReadFileCoverage:                 projectEinoAssistantCheckpointReadCoverage(s.readFileCoverage),
		ObservedReadFilePaths:            projectEinoAssistantObservedReadPaths(s.observedReadFilePaths),
		ReadFileVersions:                 projectEinoAssistantCloneReadVersions(s.readFileVersions),
		SuccessfulMutationPaths:          projectEinoAssistantObservedReadPaths(s.successfulMutationPaths),
		MutationRecoveryAttempts:         projectEinoAssistantMutationRecoveryAttemptsSnapshot(s.mutationRecoveryAttempts),
		MutationRecoveryRefs:             projectEinoAssistantRecoveryReferences(s.mutationRecoveryRefs),
		MutationRecoveryIdentities:       projectEinoAssistantRecoveryIdentitySnapshot(s.mutationRecoveryIdentities),
		SessionSnapshot:                  cloneProjectEinoAssistantSessionSnapshot(s.sessionSnapshot),
		RolloutBudget:                    rolloutBudget,
		Sandbox:                          s.sandboxCheckpointLocked(),
	}
}

func (s *projectEinoAssistantRunState) sandboxCheckpointLocked() *projectAssistantSandboxCheckpoint {
	if s.sandbox != nil {
		metadata := s.sandbox.metadataSnapshot()
		return &projectAssistantSandboxCheckpoint{Metadata: metadata}
	}
	if s.sandboxMetadata == nil {
		return nil
	}
	return &projectAssistantSandboxCheckpoint{Metadata: *s.sandboxMetadata}
}

func cloneProjectAssistantRolloutBudgetStatePtr(state *projectAssistantRolloutBudgetState) *projectAssistantRolloutBudgetState {
	if state == nil {
		return nil
	}
	copy := cloneProjectAssistantRolloutBudgetState(*state)
	return &copy
}

func projectEinoAssistantReadPathSet(paths []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, raw := range paths {
		path, err := workspace.CleanProjectPath(raw)
		if err == nil {
			out[path] = struct{}{}
		}
	}
	return out
}

func projectEinoAssistantReadVersionMap(versions map[string]string) map[string]string {
	out := make(map[string]string, min(len(versions), projectEinoAssistantMaxTrackedReads))
	for rawPath, rawVersion := range versions {
		if len(out) >= projectEinoAssistantMaxTrackedReads {
			break
		}
		path, err := workspace.CleanProjectPath(rawPath)
		if err != nil || strings.TrimSpace(rawVersion) == "" || len([]byte(rawVersion)) > workspace.MaxFileVersionBytes {
			continue
		}
		out[path] = strings.TrimSpace(rawVersion)
	}
	return out
}

func projectEinoAssistantCloneReadVersions(versions map[string]string) map[string]string {
	if len(versions) == 0 {
		return nil
	}
	out := make(map[string]string, len(versions))
	for path, version := range versions {
		out[path] = version
	}
	return out
}

func projectEinoAssistantRestoreMutationRecoveryAttempts(
	attempts map[string]projectAssistantMutationRecoveryAttempt,
) map[string]projectAssistantMutationRecoveryAttempt {
	if len(attempts) == 0 {
		return map[string]projectAssistantMutationRecoveryAttempt{}
	}
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]projectAssistantMutationRecoveryAttempt, min(len(keys), projectEinoAssistantMaxTrackedMutationRecoveryAttempts))
	for _, rawKey := range keys {
		if len(out) >= projectEinoAssistantMaxTrackedMutationRecoveryAttempts {
			break
		}
		attempt := attempts[rawKey]
		attempt.Operation = projectAssistantMutationRecoveryOperationFamily(attempt.Operation)
		attempt.Target = strings.TrimSpace(attempt.Target)
		if attempt.Target == "" {
			attempt.Target = strings.TrimSpace(rawKey)
		}
		clean, err := workspace.CleanProjectPath(attempt.Target)
		if err != nil || clean == "" || len([]byte(clean)) > 240 || attempt.Operation == "" {
			continue
		}
		attempt.Target = clean
		attempt.Failures = min(max(attempt.Failures, 0), projectEinoAssistantMutationRecoveryFailureLimit)
		if attempt.Failures == 0 {
			continue
		}
		attempt.Blocked = attempt.Blocked || attempt.Failures >= projectEinoAssistantMutationRecoveryFailureLimit
		out[clean] = attempt
	}
	return out
}

func projectEinoAssistantMutationRecoveryAttemptsSnapshot(
	attempts map[string]projectAssistantMutationRecoveryAttempt,
) map[string]projectAssistantMutationRecoveryAttempt {
	if len(attempts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]projectAssistantMutationRecoveryAttempt, min(len(keys), projectEinoAssistantMaxTrackedMutationRecoveryAttempts))
	for _, key := range keys {
		if len(out) >= projectEinoAssistantMaxTrackedMutationRecoveryAttempts {
			break
		}
		attempt := attempts[key]
		if attempt.Target == "" || attempt.Failures <= 0 {
			continue
		}
		out[key] = attempt
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func projectEinoAssistantObservedReadPaths(paths map[string]struct{}) []string {
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func projectEinoAssistantRecoveryIdentitySnapshot(in map[string]projectAssistantMutationRecoveryIdentity) map[string]projectAssistantMutationRecoveryIdentity {
	if len(in) == 0 {
		return nil
	}
	refs := make([]string, 0, len(in))
	for ref := range in {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	out := make(map[string]projectAssistantMutationRecoveryIdentity, min(len(refs), 128))
	for _, rawRef := range refs {
		if len(out) >= 128 {
			break
		}
		ref := strings.TrimSpace(rawRef)
		if ref == "" || len([]byte(ref)) > 120 {
			continue
		}
		identity := in[rawRef]
		identity.Operation = projectAssistantBoundedMutationField(identity.Operation, 32)
		identity.Target = strings.TrimSpace(identity.Target)
		if len([]byte(identity.Target)) > 240 || projectAssistantMutationRecoveryOperationFamily(identity.Operation) == "" || identity.Target == "" {
			continue
		}
		if clean, err := workspace.CleanProjectPath(identity.Target); err == nil {
			identity.Target = clean
		} else {
			continue
		}
		out[ref] = identity
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func projectEinoAssistantRecoveryReferences(refs map[string]struct{}) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			out = append(out, ref)
		}
	}
	sort.Strings(out)
	return out
}

func projectEinoAssistantRecoveryReferenceSet(refs []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref != "" && len(ref) <= 120 {
			out[ref] = struct{}{}
		}
		if len(out) >= 128 {
			break
		}
	}
	return out
}

func cloneProjectAssistantPlanSnapshot(plan projectAssistantPlanSnapshot) projectAssistantPlanSnapshot {
	return projectAssistantPlanSnapshot{
		Steps: append([]projectAssistantPlanStep(nil), plan.Steps...),
	}
}

func projectEinoAssistantToolCallSignature(name, arguments string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(name) + "\x00" + arguments))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func projectEinoAssistantSanitizeActionSignature(signature string) string {
	signature = strings.TrimSpace(signature)
	if len(signature) == len("sha256:")+sha256.Size*2 && strings.HasPrefix(signature, "sha256:") {
		return signature
	}
	return ""
}

func projectEinoAssistantSanitizeCompletedReads(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, min(len(in), projectEinoAssistantMaxTrackedReads))
	for signature, revision := range in {
		signature = projectEinoAssistantSanitizeActionSignature(signature)
		if signature == "" || revision == 0 || len(out) >= projectEinoAssistantMaxTrackedReads {
			continue
		}
		out[signature] = revision
	}
	return out
}

func projectEinoAssistantCloneCompletedReads(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for signature, revision := range in {
		out[signature] = revision
	}
	return out
}

func projectEinoAssistantRestoreReadCoverage(
	in map[string][]projectAssistantCheckpointLineRange,
) map[string][]projectEinoAssistantLineRange {
	out := make(map[string][]projectEinoAssistantLineRange, min(len(in), projectEinoAssistantMaxTrackedReads))
	total := 0
	for path, ranges := range in {
		path = strings.TrimSpace(path)
		if path == "" || total >= projectEinoAssistantMaxTrackedReads {
			continue
		}
		for _, lineRange := range ranges {
			end := min(lineRange.End, uint64(projectEinoAssistantReadThroughEOF))
			if lineRange.Start > 0 && end >= uint64(lineRange.Start) {
				out[path] = append(out[path], projectEinoAssistantLineRange{start: lineRange.Start, end: int(end)})
				total++
				if total >= projectEinoAssistantMaxTrackedReads {
					break
				}
			}
		}
	}
	return out
}

func projectEinoAssistantCheckpointReadCoverage(
	in map[string][]projectEinoAssistantLineRange,
) map[string][]projectAssistantCheckpointLineRange {
	out := make(map[string][]projectAssistantCheckpointLineRange, len(in))
	total := 0
	for path, ranges := range in {
		for _, lineRange := range ranges {
			if total >= projectEinoAssistantMaxTrackedReads {
				return out
			}
			out[path] = append(out[path], projectAssistantCheckpointLineRange{
				Start: lineRange.start,
				End:   uint64(max(lineRange.end, 0)),
			})
			total++
		}
	}
	return out
}

func projectEinoAssistantCollectToolEvidence(messages []chatMessage) []chatMessage {
	evidence := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "tool" {
			evidence = append(evidence, cloneChatMessages([]chatMessage{msg})[0])
		}
	}
	if len(evidence) > projectEinoAssistantClosingEvidenceMaxItems {
		evidence = evidence[len(evidence)-projectEinoAssistantClosingEvidenceMaxItems:]
	}
	return evidence
}

func projectEinoAssistantSanitizeSeenToolCalls(src map[string]int) map[string]int {
	keys := make([]string, 0, len(src))
	for signature := range src {
		keys = append(keys, signature)
	}
	sort.Strings(keys)
	out := make(map[string]int, min(len(keys), projectEinoAssistantMaxTrackedReads))
	for _, signature := range keys {
		count := src[signature]
		if !strings.HasPrefix(signature, "sha256:") {
			sum := sha256.Sum256([]byte(signature))
			signature = "sha256:" + hex.EncodeToString(sum[:])
		}
		out[signature] += count
		if len(out) >= projectEinoAssistantMaxTrackedReads {
			break
		}
	}
	return out
}

func projectEinoAssistantEvictSeenToolCall(seen map[string]int) {
	if len(seen) == 0 {
		return
	}
	var evict string
	for signature := range seen {
		if evict == "" || signature < evict {
			evict = signature
		}
	}
	delete(seen, evict)
}

func cloneProjectAssistantApprovedPlan(src *projectAssistantApprovedPlan) *projectAssistantApprovedPlan {
	if src == nil {
		return nil
	}
	out := *src
	out.Steps = append([]string(nil), src.Steps...)
	out.TargetPaths = append([]string(nil), src.TargetPaths...)
	if src.Capabilities != nil {
		out.Capabilities = append([]string{}, src.Capabilities...)
	}
	out.AcceptanceCriteria = append([]string(nil), src.AcceptanceCriteria...)
	return &out
}

func normalizeProjectAssistantApprovedPlan(plan projectAssistantApprovedPlan) projectAssistantApprovedPlan {
	plan.Goal = strings.TrimSpace(plan.Goal)
	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.Steps = normalizeProjectAssistantStringList(plan.Steps)
	plan.TargetPaths = normalizeProjectAssistantStringList(plan.TargetPaths)
	plan.Capabilities = normalizeProjectAssistantStringList(plan.Capabilities)
	plan.AcceptanceCriteria = normalizeProjectAssistantStringList(plan.AcceptanceCriteria)
	plan.ApprovalTool = strings.TrimSpace(plan.ApprovalTool)
	if plan.ApprovedAt.IsZero() {
		plan.ApprovedAt = time.Now().UTC()
	} else {
		plan.ApprovedAt = plan.ApprovedAt.UTC()
	}
	return plan
}

func normalizeProjectAssistantStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func projectEinoAssistantFallbackToolCall(callID, name, arguments string) chatToolCall {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		callID = "tool-1"
	}
	return chatToolCall{
		ID:   callID,
		Type: "function",
		Function: chatToolCallFunction{
			Name:      strings.TrimSpace(name),
			Arguments: strings.TrimSpace(arguments),
		},
	}
}
