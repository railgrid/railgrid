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

package providers

import (
	authnv1 "k8s.io/api/authentication/v1"
	apiserverserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
)

// TokenReviewMatchesCluster verifies the issuer reported by native kcp after
// authenticating the credential. Its JWT validators look up the account in the
// token's issuing cluster, not necessarily the TokenReview endpoint's cluster.
// Identical ServiceAccount usernames in different clusters are different
// principals. Never infer this issuer from the request URL or unverified JWT.
func TokenReviewMatchesCluster(status authnv1.TokenReviewStatus, cluster string) bool {
	issuers := status.User.Extra[apiserverserviceaccount.ClusterNameKey]
	return status.Authenticated && cluster != "" && len(issuers) == 1 && issuers[0] == cluster
}
