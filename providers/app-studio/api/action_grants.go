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
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// mergeProjectIntegrationActions applies the desired action declaration to an
// existing binding. Revocation is intentionally handled before catalog
// verification: closing an existing grant must continue to work while the
// provider is down or its catalog has changed.
func (s *Server) mergeProjectIntegrationActions(
	ctx context.Context,
	id identity,
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

		// Active declarations are checked against the current catalog even when
		// their requested key and digest are unchanged. If verification succeeds,
		// unchanged grants retain their original audit; a digest change or
		// reactivation receives fresh grant audit.
		toVerify = append(toVerify, next)
		verifyIndexes = append(verifyIndexes, index)
		preserveAudit = append(preserveAudit, !prior.Revoked && strings.TrimSpace(prior.SchemaDigest) == next.SchemaDigest)
	}

	if len(toVerify) == 0 {
		return merged, nil
	}
	verified, err := s.verifyProjectActionGrants(ctx, id, provider, ref, toVerify, consentAccepted)
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
