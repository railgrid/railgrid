// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package orgboundary

import (
	"testing"

	kcpserver "github.com/kcp-dev/kcp/pkg/server"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/compatibility"
	"k8s.io/kubernetes/pkg/controlplane/apiserver/miniaggregator"
)

func TestInstallAuthenticatorFlowsIntoOptionalVirtualServer(t *testing.T) {
	delegate := fixedAuthenticator{}
	codecs := serializer.NewCodecFactory(runtime.NewScheme())
	mainRecommended := genericapiserver.NewRecommendedConfig(codecs)
	mainRecommended.ExternalAddress = "127.0.0.1:8443"
	mainRecommended.EffectiveVersion = compatibility.DefaultBuildEffectiveVersion()
	mainRecommended.Authentication.Authenticator = delegate
	virtualRecommended := genericapiserver.NewRecommendedConfig(codecs)
	virtualRecommended.ExternalAddress = "127.0.0.1:8443"
	virtualRecommended.EffectiveVersion = compatibility.DefaultBuildEffectiveVersion()

	config := &kcpserver.Config{
		GenericConfig: &mainRecommended.Config,
		MiniAggregator: &miniaggregator.MiniAggregatorConfig{
			GenericConfig: mainRecommended.Config,
		},
		OptionalVirtual: &kcpserver.VirtualConfig{Generic: virtualRecommended},
		ExtraConfig: kcpserver.ExtraConfig{
			KcpSharedInformerFactory: kcpinformers.NewSharedInformerFactory(kcpfake.NewClientset(), 0),
		},
	}
	if err := InstallWithConfig(config, DefaultConfig()); err != nil {
		t.Fatalf("InstallWithConfig: %v", err)
	}

	if _, ok := config.MiniAggregator.GenericConfig.Authentication.Authenticator.(boundaryAuthenticator); !ok {
		t.Fatal("MiniAggregator authenticator was not wrapped by the organization boundary")
	}
	// KCP Config.Complete constructs OptionalVirtual with the authentication info
	// from the completed MiniAggregator config. Exercise that same completion
	// path to prove the root virtual server uses the installed wrapper too.
	mini := config.MiniAggregator.Complete()
	virtual := config.OptionalVirtual.Complete(mini.GenericConfig.Authentication, nil, nil, mainRecommended.ExternalAddress)
	if _, ok := virtual.Generic.Authentication.Authenticator.(boundaryAuthenticator); !ok {
		t.Fatal("OptionalVirtual authenticator did not inherit the MiniAggregator boundary wrapper")
	}
}
