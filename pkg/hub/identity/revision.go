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

package identity

import (
	"context"
	"strconv"
	"strings"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

const (
	annotationOwnerGeneration      = "railgrid.ai/scoped-identity-owner-generation"
	annotationOwnerResourceVersion = "railgrid.ai/scoped-identity-owner-resource-version"
)

// OwnerRevision is the Project snapshot a provider used to calculate rules.
// ResourceVersion is intentionally treated as an opaque equality token.
type OwnerRevision struct {
	Generation      int64
	ResourceVersion string
}

func requestOwnerRevision(req Request) (*OwnerRevision, error) {
	hasGeneration := req.ExpectedOwnerGeneration != 0
	hasResourceVersion := strings.TrimSpace(req.ExpectedOwnerResourceVersion) != ""
	if !hasGeneration && !hasResourceVersion {
		return nil, nil
	}
	if !hasGeneration || !hasResourceVersion || req.ExpectedOwnerGeneration < 1 {
		return nil, Refusal{Code: CodeInvalidRequest, Reason: "expected owner generation and resourceVersion must be supplied together"}
	}
	return &OwnerRevision{
		Generation:      req.ExpectedOwnerGeneration,
		ResourceVersion: req.ExpectedOwnerResourceVersion,
	}, nil
}

func ownerRevisionMatches(expected *OwnerRevision, observed OwnerObservation) bool {
	return expected != nil && expected.Generation == observed.Generation && expected.ResourceVersion == observed.ResourceVersion
}

func (s *Service) observeOwner(ctx context.Context, clusterID string, owner Owner, requireRevision bool) (OwnerObservation, bool, error) {
	if requireRevision {
		probe, ok := s.owners.(OwnerRevisionProbe)
		if !ok {
			return OwnerObservation{}, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the hub owner probe cannot verify owner revisions"}
		}
		found, observation, err := probe.Observe(ctx, clusterID, owner)
		return observation, found, err
	}
	found, uid, err := s.owners.Exists(ctx, clusterID, owner)
	return OwnerObservation{UID: uid}, found, err
}

func (s *Service) verifyExpectedOwnerRevision(ctx context.Context, clusterID string, owner Owner, expected *OwnerRevision) error {
	if expected == nil {
		return nil
	}
	observed, found, err := s.observeOwner(ctx, clusterID, owner, true)
	if err != nil {
		return err
	}
	if !found || owner.UID != "" && observed.UID != owner.UID || !ownerRevisionMatches(expected, observed) {
		return RevisionConflict{Code: CodeStaleOwner, Reason: "the owner changed after the request rules were derived"}
	}
	return nil
}

func ownerRevisionAnnotations(revision *OwnerRevision) map[string]string {
	if revision == nil {
		return nil
	}
	return map[string]string{
		annotationOwnerGeneration:      strconv.FormatInt(revision.Generation, 10),
		annotationOwnerResourceVersion: revision.ResourceVersion,
	}
}

func mergeOwnerRevisionAnnotations(current map[string]string, revision *OwnerRevision) map[string]string {
	if revision == nil {
		return current
	}
	if current == nil {
		current = map[string]string{}
	}
	current[annotationOwnerGeneration] = strconv.FormatInt(revision.Generation, 10)
	current[annotationOwnerResourceVersion] = revision.ResourceVersion
	return current
}

func ownerRevisionAnnotationsEqual(current map[string]string, revision *OwnerRevision) bool {
	stored, versioned, err := readOwnerRevisionAnnotations(current)
	if err != nil || versioned != (revision != nil) {
		return false
	}
	return revision == nil || stored == *revision
}

func recordOwnerRevision(record *tenancyv1alpha1.ScopedIdentity) (*OwnerRevision, bool, error) {
	if record == nil {
		return nil, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the scoped identity record is missing"}
	}
	revision, versioned, err := readOwnerRevisionAnnotations(record.Annotations)
	if err != nil || !versioned {
		return nil, versioned, err
	}
	return &revision, true, nil
}

func readOwnerRevisionAnnotations(annotations map[string]string) (OwnerRevision, bool, error) {
	generationValue, hasGeneration := annotations[annotationOwnerGeneration]
	resourceVersion, hasResourceVersion := annotations[annotationOwnerResourceVersion]
	if !hasGeneration && !hasResourceVersion {
		return OwnerRevision{}, false, nil
	}
	if !hasGeneration || !hasResourceVersion || strings.TrimSpace(resourceVersion) == "" {
		return OwnerRevision{}, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the scoped identity owner revision is incomplete"}
	}
	generation, err := strconv.ParseInt(generationValue, 10, 64)
	if err != nil || generation < 1 {
		return OwnerRevision{}, false, RevisionConflict{Code: CodeVersionConflict, Reason: "the scoped identity owner generation is invalid"}
	}
	return OwnerRevision{Generation: generation, ResourceVersion: resourceVersion}, true, nil
}
