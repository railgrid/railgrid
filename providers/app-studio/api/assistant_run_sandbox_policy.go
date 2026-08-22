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

// This file owns run-sandbox policy: configuration, eligibility, identity, and
// metadata contracts. The infrastructure provider owns the worker
// implementation; App Studio only addresses an ordinary infrastructure
// Instance through the protocol and lifecycle helpers in the companion files.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantRunSandboxModeEnv         = "APP_STUDIO_RUN_SANDBOX_MODE"
	projectAssistantRunSandboxFlagEnv         = "APP_STUDIO_RUN_SANDBOX"
	projectAssistantDevelopmentModeEnv        = "APP_STUDIO_DEVELOPMENT_MODE"
	projectAssistantReplicaCountEnv           = "APP_STUDIO_REPLICA_COUNT"
	projectAssistantRunSandboxTemplateEnv     = "APP_STUDIO_RUN_SANDBOX_TEMPLATE"
	projectAssistantRunSandboxDefaultTemplate = "universal-coding-sandbox"
	projectAssistantRunSandboxHardTTL         = 12 * time.Hour
	// Cached project sandboxes are retained until their hard lifetime. The
	// infrastructure Template and this coordinator use the same cap; an idle
	// cache may be evicted for quota pressure, but never merely after 30m.
	projectAssistantRunSandboxIdleTTL            = 12 * time.Hour
	projectAssistantRunSandboxMaxActive          = 2
	projectAssistantRunSandboxWorkspaceVerb      = "workspace"
	projectAssistantRunSandboxResource           = "instances"
	projectAssistantRunSandboxAPIVersion         = "infrastructure.faros.sh/v1alpha1"
	projectAssistantRunSandboxKind               = "Instance"
	projectAssistantRunSandboxEnvironment        = "assistant-run"
	projectAssistantRunSandboxBinding            = "assistant-run"
	projectAssistantRunSandboxNamePrefix         = "as-run-"
	projectAssistantRunSandboxMaxChanges         = 128
	projectAssistantRunSandboxMaxChangeBytes     = 8 << 20
	projectAssistantRunSandboxLabel              = "faros.sh/app-studio-run-sandbox"
	projectAssistantRunSandboxIdleExpiry         = "faros.sh/app-studio-run-sandbox-idle-expires-at"
	projectAssistantRunSandboxHardExpiry         = "faros.sh/app-studio-run-sandbox-hard-expires-at"
	projectAssistantRunSandboxClaimOwner         = "faros.sh/app-studio-run-sandbox-claim-owner"
	projectAssistantRunSandboxClaimExpiry        = "faros.sh/app-studio-run-sandbox-claim-expires-at"
	projectAssistantRunSandboxCacheGeneration    = "faros.sh/app-studio-run-sandbox-cache-generation"
	projectAssistantRunSandboxCacheState         = "faros.sh/app-studio-run-sandbox-cache-state"
	projectAssistantRunSandboxLastActivity       = "faros.sh/app-studio-run-sandbox-last-activity-at"
	projectAssistantRunSandboxCacheStateNew      = "provisioning"
	projectAssistantRunSandboxCacheStateActive   = "active"
	projectAssistantRunSandboxCacheStateCached   = "cached"
	projectAssistantRunSandboxCacheStateEvicting = "evicting"
	// The universal-coding-sandbox development overlay derives all of its
	// component-scoped child names from the Instance name (see
	// providers/infrastructure/backend/kro/devoverlay.go):
	//
	//   <instance>-dev-workspace
	//   <instance>-dev-workspace-platform-state
	//   <instance>-dev-workspace-actions-ca
	//   <instance>-dev-workspace-control
	//
	// The control Service is a DNS label and therefore limits the Instance
	// name to 63 characters. PVCs and ConfigMaps use Kubernetes DNS subdomain
	// names (up to 253 characters), so their longer suffixes do not reduce the
	// Instance budget. The instance-wide token Secret/ServiceAccount/Role/
	// RoleBinding/Job use shorter -dev-control or -dev-token suffixes.
	projectAssistantRunSandboxChildWorkspaceSuffix      = "-dev-workspace"
	projectAssistantRunSandboxChildPlatformStateSuffix  = "-dev-workspace-platform-state"
	projectAssistantRunSandboxChildActionsCASuffix      = "-dev-workspace-actions-ca"
	projectAssistantRunSandboxChildControlServiceSuffix = "-dev-workspace-control"
	projectAssistantRunSandboxChildControlSecretSuffix  = "-dev-control"
	projectAssistantRunSandboxChildTokenSuffix          = "-dev-token"
	projectAssistantRunSandboxChildServiceSuffix        = projectAssistantRunSandboxChildControlServiceSuffix
	projectAssistantRunSandboxDNSLabelMaxLength         = 63
	projectAssistantRunSandboxDNSSubdomainMaxLength     = 253
	projectAssistantRunSandboxNameMaxLength             = projectAssistantRunSandboxDNSLabelMaxLength - len(projectAssistantRunSandboxChildServiceSuffix)
	projectAssistantRunSandboxHashBytes                 = 6
	projectAssistantRunSandboxHashLength                = projectAssistantRunSandboxHashBytes * 2
	projectAssistantRunSandboxNameMaxBase               = projectAssistantRunSandboxNameMaxLength - len(projectAssistantRunSandboxNamePrefix) - 1 - projectAssistantRunSandboxHashLength
	// Instance creation is asynchronous: the ordinary Instance first becomes
	// visible, then its development overlay publishes the routing references
	// consumed by the data-plane resolver. Keep setup bounded while polling
	// the API rather than racing the first /sync request with a fixed sleep.
	projectAssistantRunSandboxReadyTimeout = 2 * time.Minute
	projectAssistantRunSandboxReadyPoll    = 250 * time.Millisecond
)

type CodingSandboxMode string

const (
	CodingSandboxModeOff     CodingSandboxMode = "off"
	CodingSandboxModeBYOOnly CodingSandboxMode = "byo-only"
	CodingSandboxModeForce   CodingSandboxMode = "force"
)

// CodingSandboxConfig is process-owned policy. DevelopmentMode is an explicit
// deployment assertion; it is never inferred from TLS, localhost, model
// optimization, or missing credentials.
type CodingSandboxConfig struct {
	Mode            CodingSandboxMode
	DevelopmentMode bool
	ReplicaCount    int
}

// CodingSandboxEligibility is reevaluated at every start and resume before any
// Infrastructure lookup. BYO resolution is intentionally fail-closed until a
// provider export/transport resolver exists.
type CodingSandboxEligibility struct {
	Eligible            bool   `json:"eligible"`
	Reason              string `json:"reason"`
	ProviderExportPath  string `json:"providerExportPath,omitempty"`
	TransportGeneration string `json:"transportGeneration,omitempty"`
}

type CodingSandboxEligibilityResolver func(context.Context, identity, workspace.Scope) (CodingSandboxEligibility, error)

const (
	projectAssistantPlatformInfrastructureExportPath = "root:faros:providers:infrastructure"
	projectAssistantSandboxTransportGeneration       = "hub-virtual-workspace-v1"
)

func parseSandboxBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on", "enabled", "codex_poc", "codex-poc":
		return true
	default:
		return false
	}
}

// ParseCodingSandboxConfig validates startup policy and reports compatibility
// warnings for legacy boolean flags. A true legacy flag maps only to byo-only;
// it never grants access to the platform Infrastructure provider.
func ParseCodingSandboxConfig(lookup func(string) string) (CodingSandboxConfig, []string, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	config := CodingSandboxConfig{
		Mode:            CodingSandboxModeOff,
		DevelopmentMode: parseSandboxBool(lookup(projectAssistantDevelopmentModeEnv)),
		ReplicaCount:    1,
	}
	if rawReplicas := strings.TrimSpace(lookup(projectAssistantReplicaCountEnv)); rawReplicas != "" {
		replicas, err := strconv.Atoi(rawReplicas)
		if err != nil || replicas < 1 {
			return CodingSandboxConfig{}, nil, fmt.Errorf("%s must be a positive integer", projectAssistantReplicaCountEnv)
		}
		config.ReplicaCount = replicas
	}
	rawMode := strings.ToLower(strings.TrimSpace(lookup(projectAssistantRunSandboxModeEnv)))
	if rawMode != "" {
		config.Mode = CodingSandboxMode(rawMode)
		switch config.Mode {
		case CodingSandboxModeOff, CodingSandboxModeBYOOnly, CodingSandboxModeForce:
		default:
			return CodingSandboxConfig{}, nil, fmt.Errorf("%s must be off, byo-only, or force", projectAssistantRunSandboxModeEnv)
		}
	} else {
		legacy := []string{}
		legacyEnabled := false
		for _, key := range []string{projectAssistantRunSandboxFlagEnv, "APP_STUDIO_CODEX_SANDBOX"} {
			if raw := strings.TrimSpace(lookup(key)); raw != "" {
				legacy = append(legacy, key)
				legacyEnabled = legacyEnabled || parseSandboxBool(raw)
			}
		}
		if len(legacy) > 0 {
			if legacyEnabled {
				config.Mode = CodingSandboxModeBYOOnly
			}
			return config, []string{fmt.Sprintf("legacy sandbox flag(s) %s map to %s; configure %s explicitly", strings.Join(legacy, ", "), config.Mode, projectAssistantRunSandboxModeEnv)}, nil
		}
	}
	if config.Mode == CodingSandboxModeForce && !config.DevelopmentMode {
		return CodingSandboxConfig{}, nil, fmt.Errorf("%s=force requires %s=true", projectAssistantRunSandboxModeEnv, projectAssistantDevelopmentModeEnv)
	}
	if config.Mode == CodingSandboxModeForce && config.ReplicaCount != 1 {
		return CodingSandboxConfig{}, nil, fmt.Errorf("%s=force requires %s=1 until sandbox claims have distributed CAS", projectAssistantRunSandboxModeEnv, projectAssistantReplicaCountEnv)
	}
	return config, nil, nil
}

func codingSandboxEligibility(config CodingSandboxConfig) CodingSandboxEligibility {
	switch config.Mode {
	case CodingSandboxModeForce:
		if !config.DevelopmentMode {
			return CodingSandboxEligibility{Reason: "force mode requires explicit development mode"}
		}
		if config.ReplicaCount != 1 {
			return CodingSandboxEligibility{Reason: "force mode requires a single App Studio replica"}
		}
		return CodingSandboxEligibility{
			Eligible:            true,
			Reason:              "explicit development force mode uses the platform Infrastructure export",
			ProviderExportPath:  projectAssistantPlatformInfrastructureExportPath,
			TransportGeneration: projectAssistantSandboxTransportGeneration,
		}
	case CodingSandboxModeBYOOnly:
		return CodingSandboxEligibility{Reason: "BYO coding sandbox resolver is not available yet"}
	default:
		return CodingSandboxEligibility{Reason: "coding sandbox mode is off"}
	}
}

func (s *Server) codingSandboxConfigSnapshot() (CodingSandboxConfig, error) {
	if s == nil {
		return CodingSandboxConfig{}, errors.New("App Studio server is unavailable")
	}
	s.mu.Lock()
	config, configured := s.runSandboxConfig, s.runSandboxConfigured
	s.mu.Unlock()
	if !configured {
		parsed, _, err := ParseCodingSandboxConfig(getenv)
		if err != nil {
			return CodingSandboxConfig{}, err
		}
		config = parsed
	}
	return config, nil
}

// ResolveCodingSandboxEligibility reevaluates policy for the exact caller and
// Project scope. Off and force are process-owned short circuits. BYO-only must
// resolve an organization binding through the installed server resolver and
// returns fail-closed when that resolver is absent or incomplete.
func (s *Server) ResolveCodingSandboxEligibility(ctx context.Context, id identity, scope workspace.Scope) CodingSandboxEligibility {
	config, err := s.codingSandboxConfigSnapshot()
	if err != nil {
		return CodingSandboxEligibility{Reason: err.Error()}
	}
	if config.Mode != CodingSandboxModeBYOOnly {
		return codingSandboxEligibility(config)
	}
	s.mu.Lock()
	resolver := s.codingSandboxResolver
	s.mu.Unlock()
	if resolver == nil {
		return CodingSandboxEligibility{Reason: "BYO coding sandbox resolver is not available yet"}
	}
	eligibility, err := resolver(ctx, id, scope)
	if err != nil {
		return CodingSandboxEligibility{Reason: "BYO coding sandbox resolution failed: " + err.Error()}
	}
	if !eligibility.Eligible {
		if strings.TrimSpace(eligibility.Reason) == "" {
			eligibility.Reason = "BYO coding sandbox binding is ineligible"
		}
		return eligibility
	}
	if strings.TrimSpace(eligibility.ProviderExportPath) == "" || strings.TrimSpace(eligibility.TransportGeneration) == "" {
		return CodingSandboxEligibility{Reason: "BYO coding sandbox resolver returned incomplete provider transport identity"}
	}
	return eligibility
}

// projectAssistantDevelopmentTemplateBound reports whether the Project has a
// hosted development environment contract. The per-run universal sandbox can
// still author, execute, and checkpoint source when this is false; only the
// legacy Project development-preview synchronization is unavailable.
func projectAssistantDevelopmentTemplateBound(project *aiv1alpha1.Project) bool {
	return project != nil && project.Spec.Template != nil && strings.TrimSpace(project.Spec.Template.Name) != ""
}

// projectAssistantRunSandboxMetadata is deliberately persisted in the
// assistant checkpoint.  It is the recovery contract after a permission
// interrupt or replica restart, not merely an in-memory handle.
type projectAssistantRunSandboxMetadata struct {
	Version             int                             `json:"version"`
	Status              string                          `json:"status"`
	RunID               string                          `json:"runID"`
	OrgUUID             string                          `json:"orgUUID"`
	WorkspaceUUID       string                          `json:"workspaceUUID"`
	ProjectName         string                          `json:"projectName"`
	ProjectUID          string                          `json:"projectUID"`
	Template            string                          `json:"template"`
	ProviderExportPath  string                          `json:"providerExportPath"`
	TransportGeneration string                          `json:"transportGeneration"`
	Instance            projectAssistantSandboxInstance `json:"instance"`
	SourceRevision      uint64                          `json:"sourceRevision"`
	SourceDigest        string                          `json:"sourceDigest"`
	RemoteRevision      uint64                          `json:"remoteRevision,omitempty"`
	RemoteDigest        string                          `json:"remoteDigest,omitempty"`
	RemoteCheckpointID  string                          `json:"remoteCheckpointID,omitempty"`
	CheckpointRevision  uint64                          `json:"checkpointRevision,omitempty"`
	CheckpointDigest    string                          `json:"checkpointDigest,omitempty"`
	CacheGeneration     string                          `json:"cacheGeneration,omitempty"`
	CreatedAt           time.Time                       `json:"createdAt"`
	LastActivityAt      time.Time                       `json:"lastActivityAt"`
	IdleExpiresAt       time.Time                       `json:"idleExpiresAt"`
	HardExpiresAt       time.Time                       `json:"hardExpiresAt"`
	Conflict            string                          `json:"conflict,omitempty"`
}

type projectAssistantSandboxInstance struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
}

type projectAssistantSandboxCheckpoint struct {
	Metadata projectAssistantRunSandboxMetadata `json:"metadata"`
}

func cloneProjectAssistantSandboxCheckpoint(src *projectAssistantSandboxCheckpoint) *projectAssistantSandboxCheckpoint {
	if src == nil {
		return nil
	}
	out := *src
	return &out
}

// projectAssistantRunSandboxEnabled remains a narrow test seam. Runtime
// admission uses the server-owned CodingSandboxEligibility contract.
func projectAssistantRunSandboxEnabled() bool {
	config, _, err := ParseCodingSandboxConfig(getenv)
	return err == nil && config.Mode != CodingSandboxModeOff
}

// getenv is a small test seam.  Tests may replace it without mutating global
// process environment while concurrent assistant runs are active.
var getenv = func(key string) string { return os.Getenv(key) }
