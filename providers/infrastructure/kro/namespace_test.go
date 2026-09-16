/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import "testing"

// TestRuntimeNamespace pins the source→runtime namespace mapping. An empty
// source namespace must default to "default" — instances created without an
// explicit namespace (App Studio's promoted Application/SimpleWebApp) carry ""
// on the instance object, yet kro materializes their children into
// "<cluster>-default". Bare concatenation yielded "<cluster>-", an invalid
// RFC 1123 label that both crashed the bridge reconcile and missed the pods'
// namespace, leaving instances stuck on "no such key: fqdn (data pending)".
func TestRuntimeNamespace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster string
		ns      string
		want    string
	}{
		{name: "empty namespace defaults to default", cluster: "jcb49sm6dkg85xwg", ns: "", want: "jcb49sm6dkg85xwg-default"},
		{name: "explicit namespace preserved", cluster: "jcb49sm6dkg85xwg", ns: "team-a", want: "jcb49sm6dkg85xwg-team-a"},
		{name: "explicit default preserved", cluster: "abc", ns: "default", want: "abc-default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuntimeNamespace(tc.cluster, tc.ns); got != tc.want {
				t.Fatalf("RuntimeNamespace(%q, %q) = %q, want %q", tc.cluster, tc.ns, got, tc.want)
			}
		})
	}
}
