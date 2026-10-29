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

package metering

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

// The admin area's writes. Reads go through the metering API (proxy in
// metering.go); writes go straight to the objects in the metering workspace
// as kcp-admin, which is the same path the organization reconciler uses. The
// metering controller picks them up like any other change: the entitlement
// reconciler recomputes effectiveLimits on its sample interval, and the
// enforcement webhook refreshes from the entitlements it reads.
//
//	PUT    /api/admin/metering/entitlements/{name}   {plan, overrides[]}
//	PUT    /api/admin/metering/plans/{name}          {displayName, limits[]}
//	DELETE /api/admin/metering/plans/{name}
//
// A plan is a quota template; an entitlement is one organization's
// subscription to it plus negotiated overrides. The hub only ever creates an
// entitlement (on the default plan); this is where it changes.

var gvrPlans = schema.GroupVersionResource{Group: "metering.contrib.kcp.io", Version: "v1alpha1", Resource: "plans"}

// Limit is one per-meter row, the wire shape of contrib-metering's PlanLimit.
type Limit struct {
	Meter   string `json:"meter"`
	Shape   string `json:"shape,omitempty"`
	Enforce string `json:"enforce"`
	Max     string `json:"max,omitempty"`
}

// EntitlementUpdate is the body of PUT .../entitlements/{name}.
type EntitlementUpdate struct {
	Plan      string  `json:"plan"`
	Overrides []Limit `json:"overrides"`
}

// PlanUpdate is the body of PUT .../plans/{name}; the plan is created when it
// does not exist yet.
type PlanUpdate struct {
	DisplayName string  `json:"displayName"`
	Limits      []Limit `json:"limits"`
}

// Writer performs the admin writes against the metering workspace.
type Writer struct {
	client      dynamic.Interface
	defaultPlan string
	caller      func(context.Context) string
	logger      logr.Logger
}

// NewWriter builds a Writer. caller returns the admin identity of a request
// for the audit line; defaultPlan is the plan the hub puts new organizations
// on, which therefore cannot be deleted.
func NewWriter(client dynamic.Interface, defaultPlan string, caller func(context.Context) string, logger logr.Logger) *Writer {
	if caller == nil {
		caller = func(context.Context) string { return "" }
	}
	return &Writer{client: client, defaultPlan: defaultPlan, caller: caller, logger: logger}
}

// WithWriter enables the write routes on h.
func (h *Handler) WithWriter(w *Writer) *Handler {
	h.writer = w
	return h
}

func (h *Handler) registerWrites(r *mux.Router) {
	r.HandleFunc("/metering/entitlements/{name}", h.requireInstalled(h.writer.updateEntitlement)).Methods(http.MethodPut)
	r.HandleFunc("/metering/plans/{name}", h.requireInstalled(h.writer.putPlan)).Methods(http.MethodPut)
	r.HandleFunc("/metering/plans/{name}", h.requireInstalled(h.writer.deletePlan)).Methods(http.MethodDelete)
}

// requireInstalled refuses a write while metering is not discovered: the
// objects' schemas only exist in the workspace once metering init ran.
func (h *Handler) requireInstalled(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.discovery.Current(); !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "metering is not installed"})
			return
		}
		next(w, r)
	}
}

// httpError carries a status with a message the admin UI shows verbatim.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, format string, args ...any) error {
	return &httpError{status: status, msg: fmt.Sprintf(format, args...)}
}

func respond(w http.ResponseWriter, obj *unstructured.Unstructured, err error) {
	if err != nil {
		var he *httpError
		switch {
		case errors.As(err, &he):
			writeJSON(w, he.status, map[string]string{"error": he.msg})
		case apierrors.IsNotFound(err):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case apierrors.IsConflict(err):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the object changed underneath this edit; reload and retry"})
		case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
		return
	}
	if obj == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, obj.Object)
}

func decode(r *http.Request, into any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fail(http.StatusBadRequest, "invalid body: %v", err)
	}
	return nil
}

func (wr *Writer) updateEntitlement(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	var body EntitlementUpdate
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	obj, err := wr.UpdateEntitlement(r.Context(), name, body)
	respond(w, obj, err)
}

// UpdateEntitlement moves entitlement name to body.Plan with body.Overrides.
func (wr *Writer) UpdateEntitlement(ctx context.Context, name string, body EntitlementUpdate) (*unstructured.Unstructured, error) {
	if body.Plan == "" {
		return nil, fail(http.StatusBadRequest, "plan is required")
	}
	if err := validateLimits(body.Overrides, "overrides"); err != nil {
		return nil, err
	}
	ent, err := wr.client.Resource(gvrEntitlements).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := wr.client.Resource(gvrPlans).Get(ctx, body.Plan, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fail(http.StatusBadRequest, "plan %q does not exist", body.Plan)
		}
		return nil, err
	}
	previous, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
	if err := unstructured.SetNestedField(ent.Object, body.Plan, "spec", "plan"); err != nil {
		return nil, err
	}
	setLimits(ent, body.Overrides, "spec", "overrides")
	out, err := wr.client.Resource(gvrEntitlements).Update(ctx, ent, metav1.UpdateOptions{FieldManager: FieldManager})
	if err != nil {
		return nil, err
	}
	wr.logger.Info("metering entitlement updated", "by", wr.caller(ctx), "entitlement", name, "from", previous, "plan", body.Plan, "overrides", len(body.Overrides))
	return out, nil
}

func (wr *Writer) putPlan(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	var body PlanUpdate
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	obj, created, err := wr.PutPlan(r.Context(), name, body)
	if err == nil && created {
		writeJSON(w, http.StatusCreated, obj.Object)
		return
	}
	respond(w, obj, err)
}

// PutPlan creates or updates plan name. On update everything but
// displayName and limits (the backend meter mapping) is preserved.
func (wr *Writer) PutPlan(ctx context.Context, name string, body PlanUpdate) (*unstructured.Unstructured, bool, error) {
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return nil, false, fail(http.StatusBadRequest, "plan name %q: %s", name, strings.Join(errs, "; "))
	}
	if err := validateLimits(body.Limits, "limits"); err != nil {
		return nil, false, err
	}
	plans := wr.client.Resource(gvrPlans)
	plan, err := plans.Get(ctx, name, metav1.GetOptions{})
	created := false
	switch {
	case apierrors.IsNotFound(err):
		created = true
		plan = &unstructured.Unstructured{}
		plan.SetAPIVersion(gvrPlans.GroupVersion().String())
		plan.SetKind("Plan")
		plan.SetName(name)
	case err != nil:
		return nil, false, err
	}
	if body.DisplayName != "" {
		if err := unstructured.SetNestedField(plan.Object, body.DisplayName, "spec", "displayName"); err != nil {
			return nil, false, err
		}
	} else {
		unstructured.RemoveNestedField(plan.Object, "spec", "displayName")
	}
	setLimits(plan, body.Limits, "spec", "limits")
	var out *unstructured.Unstructured
	if created {
		out, err = plans.Create(ctx, plan, metav1.CreateOptions{FieldManager: FieldManager})
	} else {
		out, err = plans.Update(ctx, plan, metav1.UpdateOptions{FieldManager: FieldManager})
	}
	if err != nil {
		return nil, false, err
	}
	wr.logger.Info("metering plan written", "by", wr.caller(ctx), "plan", name, "created", created, "limits", len(body.Limits))
	return out, created, nil
}

func (wr *Writer) deletePlan(w http.ResponseWriter, r *http.Request) {
	respond(w, nil, wr.DeletePlan(r.Context(), mux.Vars(r)["name"]))
}

// DeletePlan removes plan name unless it is the default plan or an
// entitlement still subscribes to it.
func (wr *Writer) DeletePlan(ctx context.Context, name string) error {
	if name == wr.defaultPlan {
		return fail(http.StatusConflict, "%q is the default plan new organizations are placed on (--metering-default-plan); it cannot be deleted", name)
	}
	ents, err := wr.client.Resource(gvrEntitlements).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var using []string
	for _, e := range ents.Items {
		if p, _, _ := unstructured.NestedString(e.Object, "spec", "plan"); p == name {
			using = append(using, e.GetName())
		}
	}
	if len(using) > 0 {
		return fail(http.StatusConflict, "plan %q is in use by %d entitlement(s): %s", name, len(using), strings.Join(using, ", "))
	}
	if err := wr.client.Resource(gvrPlans).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	wr.logger.Info("metering plan deleted", "by", wr.caller(ctx), "plan", name)
	return nil
}

var meterSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// validateLimits mirrors the PlanLimit schema plus the one rule the CRD
// cannot express: a Soft or Hard limit needs a max, and a max is a
// non-negative decimal.
func validateLimits(limits []Limit, field string) error {
	seen := map[string]bool{}
	for i, l := range limits {
		at := fmt.Sprintf("%s[%d]", field, i)
		if !meterSlug.MatchString(l.Meter) {
			return fail(http.StatusBadRequest, "%s: meter %q must be a slug (lowercase letters, digits, '_', '.', '-')", at, l.Meter)
		}
		if seen[l.Meter] {
			return fail(http.StatusBadRequest, "%s: meter %q is listed twice", at, l.Meter)
		}
		seen[l.Meter] = true
		switch l.Shape {
		case "", "Consumption", "Level":
		default:
			return fail(http.StatusBadRequest, "%s: shape must be Consumption or Level, got %q", at, l.Shape)
		}
		switch l.Enforce {
		case "Meter", "Soft", "Hard":
		default:
			return fail(http.StatusBadRequest, "%s: enforce must be Meter, Soft or Hard, got %q", at, l.Enforce)
		}
		if l.Max == "" {
			if l.Enforce != "Meter" {
				return fail(http.StatusBadRequest, "%s: a %s limit needs a max", at, l.Enforce)
			}
			continue
		}
		v, ok := new(big.Rat).SetString(l.Max)
		if !ok || v.Sign() < 0 || strings.ContainsAny(l.Max, "/eE") {
			return fail(http.StatusBadRequest, "%s: max %q must be a non-negative decimal number", at, l.Max)
		}
	}
	return nil
}

// setLimits writes limits at path, dropping the field when empty so the
// object reads as "no overrides" rather than an empty list.
func setLimits(obj *unstructured.Unstructured, limits []Limit, path ...string) {
	if len(limits) == 0 {
		unstructured.RemoveNestedField(obj.Object, path...)
		return
	}
	rows := make([]any, 0, len(limits))
	for _, l := range limits {
		row := map[string]any{"meter": l.Meter, "enforce": l.Enforce}
		if l.Shape != "" {
			row["shape"] = l.Shape
		}
		if l.Max != "" {
			row["max"] = l.Max
		}
		rows = append(rows, row)
	}
	_ = unstructured.SetNestedSlice(obj.Object, rows, path...)
}
