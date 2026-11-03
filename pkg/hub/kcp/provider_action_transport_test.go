/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/pkg/server/virtualresources"
	apis "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	fake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
	informers "github.com/kcp-dev/sdk/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type nativeActionTestTransport func(*http.Request) (*http.Response, error)

func (f nativeActionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestShardTransportUsesBindingRatherThanSharedBackendHost(t *testing.T) {
	factory := informers.NewSharedInformerFactory(fake.NewClientset(), 0)
	informer := factory.Apis().V1alpha2().APIBindings()
	for _, tc := range []struct{ cluster, path string }{
		{"consumerone", "root:railgrid:tenants:org-one:providers:kuery"},
		{"consumertwo", "root:railgrid:tenants:org-two:providers:kuery"},
		{"platformconsumer", "root:railgrid:providers:kuery"},
	} {
		binding := &apis.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", Annotations: map[string]string{"kcp.io/cluster": tc.cluster}}}
		binding.Spec.Reference.Export = &apis.ExportBindingReference{Path: tc.path, Name: "kuery.providers.railgrid.ai"}
		binding.Status.APIExportClusterName = "provider-" + tc.cluster
		binding.Status.BoundResources = []apis.BoundAPIResource{{Group: "kuery.providers.railgrid.ai", Resource: "savedviews"}}
		if err := informer.Informer().GetIndexer().Add(binding); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &rest.Config{}
	c := &server.Config{VirtualResources: &virtualresources.Config{Extra: virtualresources.ExtraConfig{VWClientConfig: cfg, APIBindingInformer: informer}}}
	embedded := NewEmbeddedKCP(EmbeddedKCPOptions{})
	embedded.wrapProviderActions(c)
	forwarded := ""
	embedded.SetProviderActionTransport(func(r *http.Request, path, name, exportCluster string) (*http.Response, error) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if exportCluster != "provider-"+parts[1] {
			t.Fatalf("resolved native export identity = %q", exportCluster)
		}
		forwarded = path
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(name))}, nil
	})
	native := false
	tr := cfg.WrapTransport(nativeActionTestTransport(func(*http.Request) (*http.Response, error) {
		native = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("native"))}, nil
	}))
	for _, tc := range []struct{ cluster, want string }{
		{"consumerone", "root:railgrid:tenants:org-one:providers:kuery"},
		{"consumertwo", "root:railgrid:tenants:org-two:providers:kuery"},
		{"platformconsumer", ""},
	} {
		t.Run(tc.cluster, func(t *testing.T) {
			forwarded = ""
			native = false
			req, _ := http.NewRequest("POST", "http://same-provider.private.svc:8080/clusters/"+tc.cluster+"/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/view/run", nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if forwarded != tc.want || native != (tc.want == "") {
				t.Fatalf("forwarded=%q native=%v", forwarded, native)
			}
		})
	}
}

func TestShardTransportRejectsMissingResolvedExportIdentity(t *testing.T) {
	factory := informers.NewSharedInformerFactory(fake.NewClientset(), 0)
	informer := factory.Apis().V1alpha2().APIBindings()
	binding := &apis.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", Annotations: map[string]string{"kcp.io/cluster": "consumer"}}}
	binding.Spec.Reference.Export = &apis.ExportBindingReference{Path: "root:railgrid:tenants:org:providers:kuery", Name: "kuery.providers.railgrid.ai"}
	binding.Status.BoundResources = []apis.BoundAPIResource{{Group: "kuery.providers.railgrid.ai", Resource: "savedviews"}}
	if err := informer.Informer().GetIndexer().Add(binding); err != nil {
		t.Fatal(err)
	}
	cfg := &rest.Config{}
	c := &server.Config{VirtualResources: &virtualresources.Config{Extra: virtualresources.ExtraConfig{VWClientConfig: cfg, APIBindingInformer: informer}}}
	embedded := NewEmbeddedKCP(EmbeddedKCPOptions{})
	embedded.wrapProviderActions(c)
	called := false
	embedded.SetProviderActionTransport(func(*http.Request, string, string, string) (*http.Response, error) { called = true; return nil, nil })
	tr := cfg.WrapTransport(nativeActionTestTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, nil }))
	req, _ := http.NewRequest("POST", "http://same-provider.private.svc:8080/clusters/consumer/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/view/run", nil)
	if _, err := tr.RoundTrip(req); err == nil || called {
		t.Fatalf("missing identity was forwarded: err=%v called=%v", err, called)
	}
}
