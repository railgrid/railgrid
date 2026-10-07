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
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// mergeProjectIntegrationActions applies the desired action declaration to an
// existing binding. Revocation is intentionally handled before catalog
// verification: closing an existing grant must continue to work while the
// provider is down or its catalog has changed.
func (s *Server) mergeProjectIntegrationActions(
	ctx context.Context,
	id identity,
	project *aiv1alpha1.Project,
	provider string,
	ref *aiv1alpha1.ProjectProviderResourceReference,
	existing []aiv1alpha1.ProjectProviderActionSpec,
	desired []aiv1alpha1.ProjectProviderActionSpec,
	consentAccepted bool,
) ([]aiv1alpha1.ProjectProviderActionSpec, error) {
	normalized, err := normalizeProjectIntegrationActions(desired)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]aiv1alpha1.ProjectProviderActionSpec, len(existing))
	for _, grant := range existing {
		key := projectProviderActionKey(grant.Name, grant.Version)
		if key == "" {
			return nil, newValidationError("stored allowed action has an invalid name or version")
		}
		if _, duplicate := byKey[key]; duplicate {
			return nil, newValidationError(fmt.Sprintf("stored allowed actions contain duplicate %s/%s", grant.Name, grant.Version))
		}
		byKey[key] = grant
	}
	restrictionOnly := projectIntegrationActionPatchIsRestrictionOnly(byKey, normalized)

	merged := make([]aiv1alpha1.ProjectProviderActionSpec, len(normalized))
	toVerify := make([]aiv1alpha1.ProjectProviderActionSpec, 0, len(normalized))
	verifyIndexes := make([]int, 0, len(normalized))
	preserveAudit := make([]bool, 0, len(normalized))
	var caller string
	var revokedAt *metav1.Time
	for index, next := range normalized {
		key := projectProviderActionKey(next.Name, next.Version)
		prior, exists := byKey[key]
		if !exists {
			if next.Revoked {
				return nil, newValidationError(fmt.Sprintf("cannot revoke unknown action %s/%s", next.Name, next.Version))
			}
			toVerify = append(toVerify, next)
			verifyIndexes = append(verifyIndexes, index)
			preserveAudit = append(preserveAudit, false)
			continue
		}

		if next.Revoked {
			if prior.Revoked {
				// Idempotent revocations retain both grant and revoke audit.
				merged[index] = copyProjectProviderActionSpec(prior)
				continue
			}
			if caller == "" {
				caller = strings.TrimSpace(id.user)
				if caller == "" {
					return nil, newValidationError("authenticated caller is required to revoke provider actions")
				}
			}
			if revokedAt == nil {
				now := metav1.Now()
				revokedAt = &now
			}
			grant := copyProjectProviderActionSpec(prior)
			grant.Revoked = true
			grant.RevokedBy = caller
			grant.RevokedAt = revokedAt.DeepCopy()
			merged[index] = grant
			continue
		}

		if restrictionOnly {
			// A patch that only preserves, removes, or revokes existing grants
			// cannot expand Project authority. Keep unchanged grant records intact
			// and allow the restriction to proceed without depending on the
			// provider's current catalog or the caller's current access.
			merged[index] = copyProjectProviderActionSpec(prior)
			continue
		}

		// When a patch expands or changes authority, validate the complete active
		// declaration against the current catalog and consent state. If
		// verification succeeds, unchanged grants retain their original audit; a
		// digest change or reactivation receives fresh grant audit.
		toVerify = append(toVerify, next)
		verifyIndexes = append(verifyIndexes, index)
		preserveAudit = append(preserveAudit, !prior.Revoked && strings.TrimSpace(prior.SchemaDigest) == next.SchemaDigest)
	}

	if len(toVerify) == 0 {
		return merged, nil
	}
	verified, err := s.verifyProjectActionGrants(ctx, id, provider, ref, toVerify, consentAccepted, project)
	if err != nil {
		return nil, err
	}
	if len(verified) != len(verifyIndexes) {
		return nil, fmt.Errorf("verified action grant count %d does not match requested count %d", len(verified), len(verifyIndexes))
	}
	for index, grant := range verified {
		if preserveAudit[index] {
			prior := byKey[projectProviderActionKey(normalized[verifyIndexes[index]].Name, normalized[verifyIndexes[index]].Version)]
			merged[verifyIndexes[index]] = copyProjectProviderActionSpec(prior)
			continue
		}
		grant.Revoked = false
		grant.RevokedBy = ""
		grant.RevokedAt = nil
		merged[verifyIndexes[index]] = grant
	}
	return merged, nil
}

// projectIntegrationActionPatchIsRestrictionOnly reports whether every active
// desired grant already exists as active with the same schema digest. A false
// result keeps catalog and consent verification on the complete active set
// before any authority expansion is persisted.
func projectIntegrationActionPatchIsRestrictionOnly(
	existing map[string]aiv1alpha1.ProjectProviderActionSpec,
	desired []aiv1alpha1.ProjectProviderActionSpec,
) bool {
	desiredByKey := make(map[string]aiv1alpha1.ProjectProviderActionSpec, len(desired))
	for _, next := range desired {
		desiredByKey[projectProviderActionKey(next.Name, next.Version)] = next
	}

	// An unchanged declaration is not a reduction: it must keep verifying the
	// live catalog so a stale schema digest cannot be silently preserved by a
	// no-op PATCH.
	hasReduction := false
	for key := range existing {
		if _, retained := desiredByKey[key]; !retained {
			hasReduction = true
		}
	}
	for _, next := range desired {
		key := projectProviderActionKey(next.Name, next.Version)
		prior, found := existing[key]
		if !found {
			return false
		}
		if next.Revoked {
			if !prior.Revoked {
				hasReduction = true
			}
			continue
		}
		if prior.Revoked || strings.TrimSpace(prior.SchemaDigest) != strings.TrimSpace(next.SchemaDigest) {
			return false
		}
	}
	return hasReduction
}

// projectIntegrationActionsRequiringAuthorization returns active grants that
// add or change the authority persisted on a Project. Existing active grants
// with the same action coordinate and schema digest do not expand the grant
// set; removals and revocations must remain possible after caller access is
// withdrawn.
func projectIntegrationActionsRequiringAuthorization(
	existing []aiv1alpha1.ProjectProviderActionSpec,
	merged []aiv1alpha1.ProjectProviderActionSpec,
) []aiv1alpha1.ProjectProviderActionSpec {
	priorByKey := make(map[string]aiv1alpha1.ProjectProviderActionSpec, len(existing))
	for _, prior := range existing {
		if key := projectProviderActionKey(prior.Name, prior.Version); key != "" {
			priorByKey[key] = prior
		}
	}

	var requiring []aiv1alpha1.ProjectProviderActionSpec
	for _, next := range merged {
		if next.Revoked {
			continue
		}
		prior, found := priorByKey[projectProviderActionKey(next.Name, next.Version)]
		if !found || prior.Revoked || strings.TrimSpace(prior.SchemaDigest) != strings.TrimSpace(next.SchemaDigest) {
			requiring = append(requiring, next)
		}
	}
	return requiring
}

func projectProviderActionKey(name, version string) string {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" || version == "" {
		return ""
	}
	return strings.ToLower(name + "\x00" + version)
}

func copyProjectProviderActionSpec(in aiv1alpha1.ProjectProviderActionSpec) aiv1alpha1.ProjectProviderActionSpec {
	out := in
	if in.GrantedAt != nil {
		out.GrantedAt = in.GrantedAt.DeepCopy()
	}
	if in.RevokedAt != nil {
		out.RevokedAt = in.RevokedAt.DeepCopy()
	}
	return out
}
