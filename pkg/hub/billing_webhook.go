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

package hub

import (
	"net/http"

	"github.com/gorilla/mux"

	"github.com/railgrid/railgrid/pkg/hub/metering"
)

// registerBillingWebhook serves the billing provider's webhook on every hub,
// with or without kcp and metering. The route is public: the deployment
// verifies the provider's signature, which is the only credential a webhook
// carries. billing is nil when metering is not configured, and the relay
// then answers 503, so the provider retries later instead of taking a 404
// for a receipt.
func registerBillingWebhook(router *mux.Router, billing *metering.Billing) {
	router.HandleFunc("/api/billing/webhook", billing.ForwardWebhook).Methods(http.MethodPost)
}
