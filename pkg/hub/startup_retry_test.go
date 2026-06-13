/*
Copyright 2026 The Faros Authors.

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

package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRunStartupStepWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	transientErr := errors.New("connection reset by peer")
	attempts := 0

	err := runStartupStepWithRetry(context.Background(), startupRetryPolicy{
		Name:     "test step",
		Interval: time.Millisecond,
		Timeout:  time.Second,
		Retryable: func(err error) bool {
			return errors.Is(err, transientErr)
		},
	}, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return transientErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runStartupStepWithRetry() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestRunStartupStepWithRetryFailsFastOnPermanentError(t *testing.T) {
	permanentErr := errors.New("invalid CRD schema")
	attempts := 0

	err := runStartupStepWithRetry(context.Background(), startupRetryPolicy{
		Name:     "test step",
		Interval: time.Hour,
		Timeout:  time.Hour,
		Retryable: func(error) bool {
			return false
		},
	}, func(context.Context) error {
		attempts++
		return permanentErr
	})
	if !errors.Is(err, permanentErr) {
		t.Fatalf("runStartupStepWithRetry() error = %v, want %v", err, permanentErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRunStartupStepWithRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	err := runStartupStepWithRetry(ctx, startupRetryPolicy{
		Name:     "test step",
		Interval: time.Hour,
		Timeout:  time.Hour,
		Retryable: func(error) bool {
			return true
		},
	}, func(context.Context) error {
		attempts++
		cancel()
		return errors.New("temporary failure")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runStartupStepWithRetry() error = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestIsRetriableKCPBootstrapError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "connection reset",
			err:  fmt.Errorf("getting CRD edges.kedge.faros.sh: read tcp: connection reset by peer"),
			want: true,
		},
		{
			name: "net timeout",
			err:  timeoutError{},
			want: true,
		},
		{
			name: "not ready forbidden",
			err: apierrors.NewForbidden(
				schema.GroupResource{Resource: "namespaces"},
				"kube-system",
				errors.New("not yet ready to handle request"),
			),
			want: true,
		},
		{
			name: "permanent validation",
			err:  errors.New("creating CRD edges.kedge.faros.sh: spec.validation.openAPIV3Schema is invalid"),
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriableKCPBootstrapError(tt.err); got != tt.want {
				t.Fatalf("isRetriableKCPBootstrapError() = %v, want %v", got, tt.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}
