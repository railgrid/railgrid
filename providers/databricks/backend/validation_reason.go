// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"
	"errors"
	"net"
)

// Stable condition reasons exposed by the provider controllers. Keep these
// values independent of Databricks response text so consumers can distinguish
// automatic retries from failures that need user action.
const (
	ValidationReasonDatabricksUnavailable = "DatabricksUnavailable"
	ValidationReasonAccessDenied          = "AccessDenied"
	ValidationReasonResourceNotFound      = "ResourceNotFound"
	ValidationReasonUnsupportedTableType  = "UnsupportedTableType"
	ValidationReasonValidationFailed      = "ValidationFailed"
)

type httpStatusCoder interface {
	HTTPStatusCode() int
}

// ClassifyValidationError maps validator failures to the stable condition
// reason contract. HTTP response bodies are intentionally not inspected here;
// validators retain only bounded, safe status messages for controller status.
func ClassifyValidationError(err error) string {
	if err == nil {
		return ValidationReasonValidationFailed
	}
	if errors.Is(err, context.Canceled) {
		return ValidationReasonValidationFailed
	}
	var unsupportedTableType UnsupportedTableTypeError
	if errors.As(err, &unsupportedTableType) {
		return ValidationReasonUnsupportedTableType
	}

	var statusErr httpStatusCoder
	if errors.As(err, &statusErr) {
		switch status := statusErr.HTTPStatusCode(); {
		case status == 408 || status == 429 || status >= 500 && status <= 599:
			return ValidationReasonDatabricksUnavailable
		case status == 401 || status == 403:
			return ValidationReasonAccessDenied
		case status == 404:
			return ValidationReasonResourceNotFound
		default:
			return ValidationReasonValidationFailed
		}
	}

	// HTTP clients wrap DNS, connection, TLS, and timeout failures in
	// url.Error/net.OpError. Treat all net.Errors and deadlines as transient;
	// context cancellation itself is not a Databricks outage and remains a
	// normal validation failure if it reaches this boundary.
	var networkErr net.Error
	if errors.As(err, &networkErr) || errors.Is(err, context.DeadlineExceeded) {
		return ValidationReasonDatabricksUnavailable
	}
	return ValidationReasonValidationFailed
}
