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

package api

// Agent templates over MCP: the catalog is read-only shipped content
// (templates.Builtin), and create_agent takes a template plus parameters and
// wiring and instantiates it before applying the request's other fields as
// overrides. The portal does the same instantiation in TypeScript and writes
// the result through the kube client; the fixtures under
// portal/src/test/fixtures/agent-templates keep the two in step.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/templates"
)

// createReport is what a templated create produced beyond the agent itself.
type createReport struct {
	// Schedules are the names of the Schedules created with the agent.
	Schedules []string
	// Warnings are non-fatal problems: the agent exists, but something it was
	// meant to get did not land (a schedule create failed).
	Warnings []string
}

// createAgentFromRequest creates an agent from a request, by template when
// one is named and from the flat fields otherwise. It is the one create path;
// applyAgentCreate wraps it for callers that only want the agent.
func (s *Server) createAgentFromRequest(ctx context.Context, c *agentsclient.Client, req *createAgentRequest) (*agentsv1alpha1.Agent, createReport, error) {
	if strings.TrimSpace(req.Template) == "" {
		a, err := s.createPlainAgent(ctx, c, req)
		return a, createReport{}, err
	}
	res, err := agentFromTemplateRequest(req)
	if err != nil {
		return nil, createReport{}, err
	}
	if err := verifyTemplateWiring(ctx, c, req); err != nil {
		return nil, createReport{}, err
	}
	if len(res.Agent.Spec.Channels) > 0 {
		if err := s.validateChannelUniqueness(ctx, c, res.Agent.Name, res.Agent.Spec.Channels); err != nil {
			return nil, createReport{}, errConflict(err.Error())
		}
	}
	out, err := c.Agents().Create(ctx, res.Agent, metav1.CreateOptions{})
	if err != nil {
		return nil, createReport{}, err
	}
	s.stampReady(ctx, c, out)
	var report createReport
	for _, sched := range res.Schedules {
		if _, err := c.Schedules().Create(ctx, sched, metav1.CreateOptions{}); err != nil {
			// The agent is useful on its own, so a failed schedule is reported,
			// not rolled back. The caller sees exactly what did and did not land.
			report.Warnings = append(report.Warnings, fmt.Sprintf("schedule %q was not created: %v", sched.Name, err))
			continue
		}
		report.Schedules = append(report.Schedules, sched.Name)
	}
	return out, report, nil
}

// createPlainAgent is the pre-template create: flat fields to an Agent.
func (s *Server) createPlainAgent(ctx context.Context, c *agentsclient.Client, req *createAgentRequest) (*agentsv1alpha1.Agent, error) {
	a, err := agentFromCreateRequest(req)
	if err != nil {
		return nil, err
	}
	if len(a.Spec.Channels) > 0 {
		if err := s.validateChannelUniqueness(ctx, c, a.Name, a.Spec.Channels); err != nil {
			return nil, errConflict(err.Error())
		}
	}
	out, err := c.Agents().Create(ctx, a, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	s.stampReady(ctx, c, out)
	return out, nil
}

// stampReady marks a fresh agent usable. Nothing else reconciles Agent status
// at create time, and an empty status read as "not ready" to callers polling
// for phase Ready. Best-effort: the agent was created either way and the
// background sweep re-stamps the phase.
func (s *Server) stampReady(ctx context.Context, c *agentsclient.Client, out *agentsv1alpha1.Agent) {
	if err := c.Agents().PatchStatus(ctx, out.Name, agentsv1alpha1.AgentStatus{Phase: agentsv1alpha1.AgentPhaseReady}); err != nil {
		log.Printf("create agent %s: stamping phase Ready: %v", out.Name, err)
	} else {
		out.Status.Phase = agentsv1alpha1.AgentPhaseReady
	}
}

// agentFromTemplateRequest instantiates the named template with the request's
// parameters and wiring, then applies the request's other fields on top as
// overrides. Pure: no tenant access.
//
// The flat fields double as wiring where they mean the same thing, so a caller
// that already knows create_agent's modelCredential and channels does not
// have to learn a second spelling: modelCredential fills the modelCredential
// slot and the primary channel fills the channel slot when the wiring map
// leaves them empty.
func agentFromTemplateRequest(req *createAgentRequest) (*templates.Result, error) {
	tmpl, ok := templates.Lookup(req.Template)
	if !ok {
		return nil, errBadRequest(fmt.Sprintf("unknown template %q; see list_agent_templates", strings.TrimSpace(req.Template)))
	}
	wiring := map[string]string{}
	for k, v := range req.Wiring {
		wiring[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if cred := strings.TrimSpace(req.ModelCredential); cred != "" {
		if key, found := slotKey(tmpl, templates.SlotModelCredential); found && wiring[key] == "" {
			wiring[key] = cred
		}
	}
	explicitChannels, err := normalizeChannels(req.Channels)
	if err != nil {
		return nil, errBadRequest(err.Error())
	}
	if len(explicitChannels) > 0 {
		if key, found := slotKey(tmpl, templates.SlotChannel); found && wiring[key] == "" {
			primary := explicitChannels[0]
			for _, ch := range explicitChannels {
				if ch.Primary {
					primary = ch
				}
			}
			wiring[key] = primary.ConnectionRef
		}
	}
	res, err := templates.Instantiate(tmpl, templates.Inputs{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Parameters:  req.Parameters,
		Wiring:      wiring,
	})
	if err != nil {
		var ie *templates.InputError
		if errors.As(err, &ie) || strings.Contains(err.Error(), "is required") || strings.Contains(err.Error(), "is not declared") {
			return nil, errBadRequest(err.Error())
		}
		return nil, err
	}
	spec := &res.Agent.Spec

	// Overrides: a set field wins over the template's value.
	if v := strings.TrimSpace(req.Description); v != "" {
		spec.Description = v
	}
	if v := strings.TrimSpace(req.SystemPrompt); v != "" {
		spec.SystemPrompt = v
	}
	if v := strings.TrimSpace(req.Autonomy); v != "" {
		spec.Autonomy = v
	}
	if req.MaxToolTurns < 0 || req.TimeoutSeconds < 0 {
		return nil, errBadRequest("maxToolTurns and timeoutSeconds must be zero or greater")
	}
	if req.MaxToolTurns > 0 {
		spec.Limits.MaxToolTurns = req.MaxToolTurns
	}
	if req.TimeoutSeconds > 0 {
		spec.Limits.TimeoutSeconds = req.TimeoutSeconds
	}
	var tokens *int64
	if req.BudgetTokens != 0 {
		tokens = &req.BudgetTokens
	}
	var usd *string
	if strings.TrimSpace(req.BudgetUSD) != "" {
		usd = &req.BudgetUSD
	}
	if tokens != nil || usd != nil {
		budget, err := normalizeAgentBudget(spec.Budget, tokens, usd)
		if err != nil {
			return nil, errBadRequest(err.Error())
		}
		spec.Budget = budget
	}
	if len(explicitChannels) > 0 {
		spec.Channels = explicitChannels
	}
	if spec.HarnessBacked() {
		if len(req.ModelFallbacks) > 0 || len(req.InteractiveFamilies) > 0 || len(req.BackgroundFamilies) > 0 {
			return nil, errBadRequest(fmt.Sprintf("template %q is harness-backed: modelFallbacks, interactiveFamilies and backgroundFamilies do not apply", tmpl.Metadata.Name))
		}
	} else {
		if fb := trimmedList(req.ModelFallbacks); len(fb) > 0 {
			spec.SetModelFallbacks(fb)
		}
		if len(req.InteractiveFamilies) > 0 {
			spec.Tools.Interactive.Families = normalizeFamilies(req.InteractiveFamilies)
		}
		if len(req.BackgroundFamilies) > 0 {
			spec.Tools.Background.Families = normalizeFamilies(req.BackgroundFamilies)
		}
	}
	return res, nil
}

// slotKey finds the key of the one slot of a kind, for the flat-field
// fallbacks. Connection slots are many per template and are never matched.
func slotKey(t templates.Template, kind string) (string, bool) {
	for _, w := range t.Spec.Wiring {
		if w.Slot == kind {
			return w.Key, true
		}
	}
	return "", false
}

// channelConnectionTypes are the messaging Connection types a channel slot
// may be wired to.
var channelConnectionTypes = map[string]bool{
	agentsv1alpha1.ConnectionTypeTelegram: true,
	agentsv1alpha1.ConnectionTypeSlack:    true,
	agentsv1alpha1.ConnectionTypeDiscord:  true,
	agentsv1alpha1.ConnectionTypeSMTP:     true,
}

// verifyTemplateWiring checks the Connections a template was wired with
// exist and have the type the slot asked for. Instantiate is pure and cannot;
// the portal's pickers only offer the right type, so this is what keeps the
// MCP path as safe: a websearch slot wired to a GitHub connection would
// otherwise create an agent whose web search silently never works.
func verifyTemplateWiring(ctx context.Context, c *agentsclient.Client, req *createAgentRequest) error {
	tmpl, ok := templates.Lookup(req.Template)
	if !ok {
		return nil
	}
	for _, w := range tmpl.Spec.Wiring {
		name := strings.TrimSpace(req.Wiring[w.Key])
		if name == "" && w.Slot == templates.SlotChannel {
			// The flat channels field fills this slot; validateChannelUniqueness
			// and the reconciler already check those.
			continue
		}
		if name == "" {
			continue
		}
		switch w.Slot {
		case templates.SlotConnection, templates.SlotChannel:
			conn, err := c.Connections().Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return errBadRequest(fmt.Sprintf("wiring %q names connection %q, which does not exist in this workspace", w.Key, name))
			}
			if err != nil {
				return err
			}
			if w.Slot == templates.SlotConnection && conn.Spec.Type != w.ConnectionType {
				return errBadRequest(fmt.Sprintf("wiring %q needs a %s connection; %q is %s", w.Key, w.ConnectionType, name, conn.Spec.Type))
			}
			if w.Slot == templates.SlotChannel && !channelConnectionTypes[conn.Spec.Type] {
				return errBadRequest(fmt.Sprintf("wiring %q needs a messaging connection (telegram, slack, discord, smtp); %q is %s", w.Key, name, conn.Spec.Type))
			}
		}
	}
	return nil
}

// --- MCP: the catalog ---

type agentTemplateSummary struct {
	Name        string                 `json:"name"`
	DisplayName string                 `json:"displayName"`
	Description string                 `json:"description,omitempty"`
	Category    string                 `json:"category"`
	Backend     string                 `json:"backend" jsonschema:"model or harness: which backend the produced agent runs on"`
	Parameters  []templates.Parameter  `json:"parameters,omitempty"`
	Wiring      []templates.WiringSlot `json:"wiring,omitempty"`
	Schedules   []string               `json:"schedules,omitempty" jsonschema:"Schedules created with the agent, by suffix"`
}

type listAgentTemplatesOutput struct {
	Templates []agentTemplateSummary `json:"templates"`
	Note      string                 `json:"note"`
}

// getAgentTemplateOutput is the template in full, flattened into the same
// shapes agentSettings uses. It is a view rather than the templates.Template
// itself because ScheduleSpec carries a metav1.Time, which the MCP SDK's schema
// inference refuses as an embedded struct.
type getAgentTemplateOutput struct {
	agentTemplateSummary
	SystemPrompt string                          `json:"systemPrompt,omitempty"`
	Autonomy     string                          `json:"autonomy,omitempty"`
	AgentBackend agentsv1alpha1.AgentBackendSpec `json:"agentBackend,omitzero" jsonschema:"The backend block as the template ships it, wired fields empty"`
	Tools        agentsv1alpha1.AgentToolPolicy  `json:"tools,omitzero"`
	Limits       agentsv1alpha1.AgentLimits      `json:"limits,omitzero"`
	Budget       *agentsv1alpha1.AgentBudget     `json:"budget,omitempty"`
	Schedules    []templateScheduleView          `json:"schedules,omitempty"`
}

type templateScheduleView struct {
	Name       string `json:"name" jsonschema:"Suffix appended to the agent name"`
	Type       string `json:"type"`
	Schedule   string `json:"schedule"`
	TimeZone   string `json:"timeZone,omitempty"`
	Task       string `json:"task"`
	ChannelRef string `json:"channelRef,omitempty"`
}

func templateDetail(t templates.Template) getAgentTemplateOutput {
	out := getAgentTemplateOutput{
		agentTemplateSummary: templateSummary(t),
		SystemPrompt:         t.Spec.Agent.SystemPrompt,
		Autonomy:             t.Spec.Agent.Autonomy,
		AgentBackend:         t.Spec.Agent.Backend,
		Tools:                t.Spec.Agent.Tools,
		Limits:               t.Spec.Agent.Limits,
		Budget:               t.Spec.Agent.Budget,
	}
	// The summary's Schedules are the suffixes; the detail replaces them with
	// the full schedule, under the same key.
	out.agentTemplateSummary.Schedules = nil
	for _, s := range t.Spec.Schedules {
		out.Schedules = append(out.Schedules, templateScheduleView{
			Name: s.Name, Type: s.Spec.Type, Schedule: s.Spec.Schedule, TimeZone: s.Spec.TimeZone, Task: s.Spec.Task, ChannelRef: s.Spec.ChannelRef,
		})
	}
	return out
}

func templateSummary(t templates.Template) agentTemplateSummary {
	out := agentTemplateSummary{
		Name:        t.Metadata.Name,
		DisplayName: t.Spec.DisplayName,
		Description: t.Spec.Description,
		Category:    t.Spec.Category,
		Backend:     t.Spec.Agent.BackendType(),
		Parameters:  t.Spec.Parameters,
		Wiring:      t.Spec.Wiring,
	}
	for _, s := range t.Spec.Schedules {
		out.Schedules = append(out.Schedules, s.Name)
	}
	return out
}

// registerAgentTemplateTools adds the read-only catalog tools. Neither needs a
// tenant client: the catalog is compiled in.
func (s *Server) registerAgentTemplateTools(srv *mcp.Server) {
	yes := true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes}

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "list_agent_templates",
		Title: "List agent templates",
		Description: "List the built-in agent templates: curated starting points (code reviewer, news reporter, researcher, workspace assistant) with the prompt, autonomy, tool grants and schedules already set. " +
			"Each one declares parameters (values substituted into its prompt) and wiring slots (workspace objects it needs: a model credential, a channel, a connection, a machine). " +
			"Create one with create_agent(template, parameters, wiring); explicit create_agent fields override the template.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listAgentTemplatesOutput, error) {
		all := templates.Builtin()
		out := listAgentTemplatesOutput{
			Templates: make([]agentTemplateSummary, 0, len(all)),
			Note: "Wiring values are names from list_model_credentials / list_connections; an edge slot takes \"<Kind>/<name>\" (LinuxServer/dev-box). " +
				"A list parameter is one item per line.",
		}
		for _, t := range all {
			out.Templates = append(out.Templates, templateSummary(t))
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_agent_template",
		Title:       "Get an agent template",
		Description: "Read one agent template in full — its system prompt, tool grants, limits and the schedules it creates — so you can show the user what create_agent(template=...) will produce.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, getAgentTemplateOutput, error) {
		t, ok := templates.Lookup(in.Name)
		if !ok {
			return nil, getAgentTemplateOutput{}, errBadRequest(fmt.Sprintf("unknown template %q; see list_agent_templates", strings.TrimSpace(in.Name)))
		}
		return nil, templateDetail(t), nil
	})
}
