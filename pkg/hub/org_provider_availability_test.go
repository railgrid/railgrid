// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"fmt"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/hub/kcp/byolimits"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestOrganizationProviderAvailabilityFailsClosedUntilQuotaSupportIsVerified(t *testing.T) {
	complete := byolimits.Config{MaxObjectBytes: 1024, MaxRequestBytes: 2048, RequestsPerSecond: 10, Burst: 5, MaxConcurrentRequests: 4, MaxConcurrentStreams: 2, MaxOrganizations: 10, RequestTimeout: time.Second, StreamTimeout: time.Minute}
	consumer := providers.OrgConsumerPolicyConfig{Quota: providers.ConsumerQuotaLimits{MaxResourceTypes: 16, MaxObjectsPerResource: 4}, MaxConsumers: 2, MaxLiveBytes: 1 << 30}
	for _, scenario := range []string{"disabled", "partial", "external", "external kubeconfig", "consumer disabled", "consumer partial", "consumer bytes insufficient", "consumer without native", "not ready", "ready"} {
		t.Run(scenario, func(t *testing.T) {
			opts := &Options{EmbeddedKCP: true, OrganizationLimits: complete, OrganizationConsumerPolicy: consumer}
			switch scenario {
			case "disabled":
				opts.OrganizationLimits = byolimits.Config{}
				opts.OrganizationConsumerPolicy = providers.OrgConsumerPolicyConfig{}
			case "partial":
				opts.OrganizationLimits.Burst = 0
			case "external":
				opts.EmbeddedKCP = false
			case "external kubeconfig":
				opts.ExternalKCPKubeconfig = "external.kubeconfig"
			case "consumer disabled":
				opts.OrganizationConsumerPolicy = providers.OrgConsumerPolicyConfig{}
			case "consumer partial":
				opts.OrganizationConsumerPolicy.Quota.MaxResourceTypes = 0
			case "consumer bytes insufficient":
				opts.OrganizationConsumerPolicy.MaxLiveBytes = 1
			case "consumer without native":
				opts.OrganizationLimits = byolimits.Config{}
			}
			checks := 0
			server := &Server{opts: opts}
			err := server.orgProviderAvailability(func() error {
				checks++
				if scenario == "not ready" {
					return fmt.Errorf("native ownership state unsynchronized")
				}
				return nil
			})(t.Context())
			if err == nil {
				t.Fatalf("availability error=%v", err)
			}
			if scenario == "ready" {
				if err.Error() != "hosted BYO enrollment is disabled until supported KCP ResourceQuota accounting is verified" {
					t.Fatalf("ready configuration error=%v", err)
				}
				if checks != 0 {
					t.Fatal("unsupported quota implementation consulted native readiness")
				}
			}
			if scenario != "not ready" && scenario != "ready" && checks != 0 {
				t.Fatal("incomplete configuration consulted native readiness")
			}
			_, err = NewServer(opts)
			invalid := scenario != "disabled" && scenario != "ready" && scenario != "not ready"
			if (err != nil) != invalid {
				t.Fatalf("NewServer error=%v want invalid=%t", err, invalid)
			}
			if scenario == "ready" && organizationConsumerPolicyConfig(opts).MaxOrganizations != complete.MaxOrganizations {
				t.Fatal("consumer lock capacity must follow native organization capacity")
			}
		})
	}
}
