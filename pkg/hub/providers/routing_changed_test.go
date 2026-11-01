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
	"net/url"
	"testing"
	"testing/fstest"
)

func TestRoutingChanged(t *testing.T) {
	base := Provider{
		Name:           "code",
		EndpointsValid: true,
		UIURL:          &url.URL{Scheme: "http", Host: "code:8083"},
		BackendURL:     &url.URL{Scheme: "http", Host: "code:8083"},
	}
	same := base
	same.LastHeartbeat = base.LastHeartbeat.Add(1)
	same.ReportedVersion = "v2"
	if routingChanged(base, same) {
		t.Fatal("heartbeat and version changes must not count as a routing change")
	}

	cases := map[string]func(p *Provider){
		"endpointsValid": func(p *Provider) { p.EndpointsValid = false },
		"ui":             func(p *Provider) { p.UIURL = &url.URL{Scheme: "http", Host: "other:1"} },
		"ui cleared":     func(p *Provider) { p.UIURL = nil },
		"backend":        func(p *Provider) { p.BackendURL = nil },
		"localUI":        func(p *Provider) { p.LocalUIAssets = fstest.MapFS{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			next := base
			mutate(&next)
			if !routingChanged(base, next) {
				t.Fatalf("%s change must be reported", name)
			}
		})
	}
}

func TestRegistryLookupIsExactScope(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "code"})
	if _, ok := reg.lookup("org-a", "code"); ok {
		t.Fatal("lookup must not fall back from an Org scope to the platform record")
	}
	if _, ok := reg.lookup("", "code"); !ok {
		t.Fatal("platform record must be found under the empty org")
	}
	reg.Upsert(Provider{Name: "code", OrgUUID: "org-a"})
	if _, ok := reg.lookup("org-a", "code"); !ok {
		t.Fatal("org record must be found under its own org")
	}
}
