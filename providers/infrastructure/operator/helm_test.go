/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import "testing"

func TestPlanRecovery(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		lastDeployed int
		hasDeployed  bool
		wantAction   recoveryAction
		wantRev      int
	}{
		{
			name:       "deployed needs no recovery",
			status:     "deployed",
			wantAction: recoverNone,
		},
		{
			name:       "failed needs no recovery (upgrade --install retries it)",
			status:     "failed",
			wantAction: recoverNone,
		},
		{
			name:         "pending-upgrade rolls back to last deployed",
			status:       "pending-upgrade",
			lastDeployed: 446,
			hasDeployed:  true,
			wantAction:   recoverRollback,
			wantRev:      446,
		},
		{
			name:         "pending-rollback rolls back to last deployed",
			status:       "pending-rollback",
			lastDeployed: 12,
			hasDeployed:  true,
			wantAction:   recoverRollback,
			wantRev:      12,
		},
		{
			name:        "pending-upgrade with no good revision uninstalls",
			status:      "pending-upgrade",
			hasDeployed: false,
			wantAction:  recoverUninstall,
		},
		{
			name:       "pending-install uninstalls (nothing to roll back to)",
			status:     "pending-install",
			wantAction: recoverUninstall,
		},
		{
			name:       "unknown status is left alone",
			status:     "uninstalling",
			wantAction: recoverNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAction, gotRev := planRecovery(tt.status, tt.lastDeployed, tt.hasDeployed)
			if gotAction != tt.wantAction {
				t.Errorf("planRecovery(%q) action = %v, want %v", tt.status, gotAction, tt.wantAction)
			}
			if gotAction == recoverRollback && gotRev != tt.wantRev {
				t.Errorf("planRecovery(%q) rev = %d, want %d", tt.status, gotRev, tt.wantRev)
			}
		})
	}
}
