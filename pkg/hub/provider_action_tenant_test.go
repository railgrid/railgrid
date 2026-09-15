// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"net/http/httptest"
	"testing"
)

func TestProviderActionRouteDoesNotBroadenServiceAccountAccess(t *testing.T) {
	const valid = "/services/providers/linear/actions/clusters/tenant-one/teams/engineering/issues/v1"
	for _, test := range []struct {
		method, path string
		want         bool
	}{
		{"POST", valid, true}, {"GET", valid, true}, {"DELETE", valid, false},
		{"POST", "/services/providers/linear/api/onboarding/teams", false},
		{"POST", valid + "/extra", false}, {"POST", valid + "/", false},
		{"POST", "/ui/providers/linear/actions/clusters/tenant-one/teams/engineering/issues/v1", false},
		{"POST", "/services/providers/linear/actions/clusters/tenant-one/teams/../issues/v1", false},
		{"POST", "/services/providers/linear/actions/clusters/tenant-one/teams/engineering/issues/v0", false},
		{"POST", "/services/providers/linear/actions/clusters/tenant-one/teams/engineering%2Fother/issues/v1", false},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			got := providerActionCluster(req)
			if (got != "") != test.want || (test.want && got != "tenant-one") {
				t.Fatalf("cluster=%q", got)
			}
		})
	}
}
