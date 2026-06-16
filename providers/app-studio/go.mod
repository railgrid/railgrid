module github.com/faroshq/provider-app-studio

go 1.26.3

require (
	github.com/faroshq/provider-sdk v0.0.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/mux v1.8.1
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/lib/pq v1.10.9
	golang.org/x/oauth2 v0.36.0
	k8s.io/apimachinery v0.36.1
	k8s.io/client-go v0.36.1
	k8s.io/klog/v2 v2.140.0
)

require (
	cloud.google.com/go/compute/metadata v0.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/term v0.42.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/kube-openapi v0.0.0-20260414162039-ec9c827d403f // indirect
	k8s.io/utils v0.0.0-20260319190234-28399d86e0b5 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.0 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace (
	github.com/kcp-dev/kcp => github.com/mjudeikis/kcp v0.0.0-20260518141734-ea6103f11755
	github.com/kcp-dev/sdk => github.com/kcp-dev/sdk v0.28.1-0.20260504075209-315ebd35273b
	// TODO: drop this once virtual-workspace-framework is synced from kcp staging.
	github.com/kcp-dev/virtual-workspace-framework => github.com/mjudeikis/kcp/staging/src/github.com/kcp-dev/virtual-workspace-framework v0.0.0-20260518141734-ea6103f11755
)

replace (
	k8s.io/api => github.com/kcp-dev/kubernetes/staging/src/k8s.io/api v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/apiextensions-apiserver => github.com/kcp-dev/kubernetes/staging/src/k8s.io/apiextensions-apiserver v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/apimachinery => github.com/kcp-dev/kubernetes/staging/src/k8s.io/apimachinery v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/apiserver => github.com/kcp-dev/kubernetes/staging/src/k8s.io/apiserver v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/cli-runtime => github.com/kcp-dev/kubernetes/staging/src/k8s.io/cli-runtime v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/client-go => github.com/kcp-dev/kubernetes/staging/src/k8s.io/client-go v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/code-generator => github.com/kcp-dev/kubernetes/staging/src/k8s.io/code-generator v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/component-base => github.com/kcp-dev/kubernetes/staging/src/k8s.io/component-base v0.0.0-20260513103013-2d7bf6b3c556
	k8s.io/kms => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kms v0.0.0-20260513103013-2d7bf6b3c556
)
