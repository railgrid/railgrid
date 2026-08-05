// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/faroshq/provider-vibe-studio/client"
	"github.com/faroshq/provider-vibe-studio/engine"
	"github.com/faroshq/provider-vibe-studio/provision"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/store"
	"github.com/faroshq/provider-vibe-studio/webtools"
)

// EinoEngine is the real model harness behind session.Engine: the intake turn
// drives a forced propose_blueprint tool against the live catalog; the studio
// turn edits the session workspace with file tools that sync straight into
// the sandbox. Tenants without an LLM Secret fall back to the scripted engine
// so the product keeps working (and tests stay deterministic).
type EinoEngine struct {
	server   *Server
	fallback session.Engine
	eng      *engine.Engine
}

// NewEinoEngine wires the engine onto an existing Server (store, dataplane,
// gateway access).
func NewEinoEngine(server *Server, fallback session.Engine) *EinoEngine {
	return &EinoEngine{server: server, fallback: fallback, eng: engine.New()}
}

// prepare resolves the caller-scoped client + chat model, or reports the
// scripted fallback should run.
func (e *EinoEngine) prepare(ctx context.Context, tc session.TurnContext) (*client.Client, llmProfile, error) {
	if e.server.gql == nil || tc.ClusterID == "" || tc.Token == "" {
		return nil, llmProfile{}, errLLMNotConfigured
	}
	scope, err := e.server.gql.For(tc.ClusterID, tc.Token)
	if err != nil {
		return nil, llmProfile{}, errLLMNotConfigured
	}
	cl := client.New(scope)
	prof, modelName, err := resolveLLMProfile(ctx, cl, tc.SessionID)
	if err != nil {
		return nil, llmProfile{}, err
	}
	if modelName != "" {
		go touchModelUsed(context.WithoutCancel(ctx), cl, modelName, metav1.Now())
	}
	return cl, prof, nil
}

// IntakeTurn implements session.Engine with a real blueprint proposal.
func (e *EinoEngine) IntakeTurn(ctx context.Context, tc session.TurnContext, state session.SessionState, input string, answers map[string]string) (session.Blueprint, error) {
	cl, prof, err := e.prepare(ctx, tc)
	if err != nil {
		return e.fallback.IntakeTurn(ctx, tc, state, input, answers)
	}
	model, err := buildChatModel(ctx, prof)
	if err != nil {
		return session.Blueprint{}, err
	}

	catalog, err := intakeCatalog(ctx, cl)
	if err != nil {
		return session.Blueprint{}, fmt.Errorf("reading template catalog: %w", err)
	}

	var captured *session.Blueprint
	tools := []engine.Tool{{
		Name: "propose_blueprint",
		Desc: "Propose the app blueprint. Call EXACTLY ONCE per turn with the complete draft. " +
			"template.name MUST be one of the catalog names. Include questions ONLY when an answer " +
			"is decision-blocking (template choice, data model, integrations); 2-5 options each, " +
			"exactly one recommended:true; never compound 'X or Y' labels. With questions present, " +
			"keep the rest of the blueprint provisional. No questions -> the blueprint goes to review.",
		JSONSchema: blueprintToolSchema,
		Exec: func(_ context.Context, argsJSON string) (string, error) {
			var bp session.Blueprint
			if err := json.Unmarshal([]byte(argsJSON), &bp); err != nil {
				return "", fmt.Errorf("invalid blueprint payload: %w", err)
			}
			if !catalogHas(catalog, bp.Template.Name) {
				return "", fmt.Errorf("template %q is not in the catalog; choose one of: %s", bp.Template.Name, catalogNames(catalog))
			}
			captured = &bp
			return "blueprint recorded — do not restate it in chat", nil
		},
	}}

	sys := "You are the vibe-studio intake wizard. From the user's intent, draft a blueprint " +
		"grounded in the infrastructure template catalog below. `values` MUST use ONLY that " +
		"template's listed inputs, using the declared TYPE (an object-typed input needs an object, never true/false) — app requirements (features, pages, entities) belong in the " +
		"summary and success criteria, never in values. Ask at most 3 clarifying questions " +
		"and only when decision-blocking; prefer good assumptions over questions (a simple request " +
		"gets zero questions and sensible defaults). Success criteria must be user-visible and testable. " +
		"The host renders all UI: never restate the blueprint or list questions in chat.\n\nCatalog:\n" + catalogText(catalog)
	if state.ProposeIterations > 0 {
		sys += fmt.Sprintf("\nThis is proposal round %d of %d — converge.", state.ProposeIterations+1, session.MaxProposeIterations)
	}
	msgs := []engine.Message{{Role: engine.RoleSystem, Content: sys}}
	if state.Blueprint != nil {
		prev, _ := json.Marshal(state.Blueprint)
		msgs = append(msgs, engine.Message{Role: engine.RoleAssistant, Content: "Previous draft: " + string(prev)})
	}
	switch {
	case len(answers) > 0:
		aj, _ := json.Marshal(answers)
		msgs = append(msgs, engine.Message{Role: engine.RoleUser, Content: "Wizard answers (these OVERRIDE your recommendations; re-emit once with questions: []): " + string(aj)})
	default:
		msgs = append(msgs, engine.Message{Role: engine.RoleUser, Content: input})
	}

	if _, err := e.eng.StreamTurnWithTools(ctx, model, msgs, tools, 4, turnCallbacks(tc)); err != nil {
		return session.Blueprint{}, err
	}
	if captured == nil {
		return session.Blueprint{}, fmt.Errorf("the model did not propose a blueprint")
	}
	return *captured, nil
}

// StudioTurn implements session.Engine with workspace + runtime tools.
func (e *EinoEngine) StudioTurn(ctx context.Context, tc session.TurnContext, state session.SessionState, input string) (string, error) {
	cl, prof, err := e.prepare(ctx, tc)
	if err != nil {
		return e.fallback.StudioTurn(ctx, tc, state, input)
	}
	model, err := buildChatModel(ctx, prof)
	if err != nil {
		return "", err
	}

	// Resolve the sandbox target from the Project + Template (spec is truth).
	var (
		info provision.DevInfo
		ref  provision.Ref
	)
	// Web search is a WORKSPACE service: one backend, shared by every
	// project, resolved from the Studio singleton.
	var search webtools.SearchRef
	if resource, name := e.server.searchBackend(ctx, cl); name != "" {
		search = webtools.SearchRef{Resource: resource, Name: name}
	}
	if state.ProjectName != "" {
		if p, err := cl.GetProject(ctx, state.ProjectName); err == nil {
			if p.Spec.Template != nil {
				if tmpl, err := cl.GetTemplate(ctx, p.Spec.Template.Name); err == nil {
					if i, err := provision.ParseDevInfo(tmpl); err == nil {
						info = i
						ref = provision.Ref{Resource: i.Resource, Name: state.ProjectName}
					}
				}
			}
		}
	}
	scope := store.Scope{Tenant: tc.Tenant}

	pc := provision.NewClient(hubBaseURL(), tc.ClusterID, tc.Token, hubInsecure())
	// The web family is always on: looking things up is part of building,
	// and the search backend is provisioned with the project.
	tools := append(e.studioTools(scope, pc, tc.SessionID, info, ref), webtools.Tools(pc, search)...)

	sys := "You are the vibe-studio build assistant working on the user's app"
	if state.Blueprint != nil {
		sys += fmt.Sprintf(" (%s — %s, template %s)", state.Blueprint.Title, state.Blueprint.Summary, state.Blueprint.Template.Name)
	}
	sys += ". The workspace files are the source of truth; write_file/delete_file sync into the " +
		"live dev sandbox automatically (hot reload). Always read a file before editing it. Keep " +
		"changes minimal and verify with get_logs when behavior matters. Components and their " +
		"workspace paths: " + provision.ComponentsText(info.Components) + ". " +
		"You have web access: web_search finds pages, web_fetch reads one — use them to check " +
		"documentation, an API's shape, or a repository page rather than asking the user to paste it. " +
		"Answer concisely."
	msgs := []engine.Message{{Role: engine.RoleSystem, Content: sys}}
	msgs = append(msgs, e.recentTranscript(scope, tc.SessionID, 20)...)
	msgs = append(msgs, engine.Message{Role: engine.RoleUser, Content: input})

	res, err := e.eng.StreamTurnWithTools(ctx, model, msgs, tools, 16, turnCallbacks(tc))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Content) == "" {
		return "(done)", nil
	}
	return res.Content, nil
}

// studioTools builds the file + runtime toolset for one turn.
func (e *EinoEngine) studioTools(scope store.Scope, pc *provision.Client, sessionID string, info provision.DevInfo, ref provision.Ref) []engine.Tool {
	s := e.server
	pathParam := map[string]engine.Param{"path": {Type: "string", Desc: "workspace-relative file path", Required: true}}
	return []engine.Tool{
		{
			Name: "list_files", Desc: "List every file path in the workspace.",
			Params: map[string]engine.Param{},
			Exec: func(ctx context.Context, _ string) (string, error) {
				paths, err := s.store.ListWorkspaceFiles(ctx, scope, sessionID)
				if err != nil {
					return "", err
				}
				return strings.Join(paths, "\n"), nil
			},
		},
		{
			Name: "read_file", Desc: "Read one workspace file.",
			Params: pathParam,
			Exec: func(ctx context.Context, args string) (string, error) {
				var a struct{ Path string }
				if err := json.Unmarshal([]byte(args), &a); err != nil {
					return "", err
				}
				f, err := s.store.GetWorkspaceFile(ctx, scope, sessionID, a.Path)
				if err != nil {
					return "", err
				}
				return f.Content, nil
			},
		},
		{
			Name: "write_file", Desc: "Create or overwrite one workspace file with the FULL new content; it syncs into the running sandbox immediately.",
			Params: map[string]engine.Param{
				"path":    {Type: "string", Desc: "workspace-relative file path", Required: true},
				"content": {Type: "string", Desc: "complete file content", Required: true},
			},
			Exec: func(ctx context.Context, args string) (string, error) {
				var a struct{ Path, Content string }
				if err := json.Unmarshal([]byte(args), &a); err != nil {
					return "", err
				}
				if len(a.Content) > store.MaxWorkspaceFileBytes {
					return "", fmt.Errorf("file exceeds %d bytes", store.MaxWorkspaceFileBytes)
				}
				if err := s.store.PutWorkspaceFiles(ctx, scope, sessionID, []store.WorkspaceFile{{Path: a.Path, Content: a.Content}}, time.Now()); err != nil {
					return "", err
				}
				if ref.Name != "" {
					if _, err := pc.SyncFiles(ctx, ref, info.Components, []provision.File{{Path: a.Path, Content: a.Content}}); err != nil {
						return "saved, but sandbox sync failed: " + err.Error(), nil
					}
				}
				return "written and synced", nil
			},
		},
		{
			Name: "delete_file", Desc: "Delete one workspace file (also removed from the sandbox).",
			Params: pathParam,
			Exec: func(ctx context.Context, args string) (string, error) {
				var a struct{ Path string }
				if err := json.Unmarshal([]byte(args), &a); err != nil {
					return "", err
				}
				if err := s.store.DeleteWorkspaceFile(ctx, scope, sessionID, a.Path); err != nil {
					return "", err
				}
				if ref.Name != "" {
					if err := pc.SyncDelete(ctx, ref, info.Components, a.Path); err != nil {
						return "deleted, but sandbox sync failed: " + err.Error(), nil
					}
				}
				return "deleted", nil
			},
		},
		{
			Name: "get_logs", Desc: "Fetch recent runtime logs from a sandbox component.",
			Params: map[string]engine.Param{"component": {
				Type: "string", Required: true,
				Desc: "component NAME (not its directory) — one of: " + provision.ComponentNames(info.Components),
				Enum: provision.ComponentEnum(info.Components),
			}},
			Exec: func(ctx context.Context, args string) (string, error) {
				var a struct{ Component string }
				if err := json.Unmarshal([]byte(args), &a); err != nil {
					return "", err
				}
				comp, err := provision.ResolveComponent(info.Components, a.Component)
				if err != nil {
					return "", err
				}
				r := ref
				r.Component = comp
				body, status, err := pc.Call(ctx, r, provision.VerbLog, "GET", nil)
				if err != nil {
					return "", err
				}
				if status < 200 || status >= 300 {
					return "", fmt.Errorf("logs for component %q returned %d: %s", comp, status, provision.Tail(strings.TrimSpace(string(body)), 300))
				}
				return provision.Tail(string(body), 8000), nil
			},
		},
		{
			Name: "restart", Desc: "Restart a sandbox component's process.",
			Params: map[string]engine.Param{"component": {
				Type: "string", Required: true,
				Desc: "component NAME (not its directory) — one of: " + provision.ComponentNames(info.Components),
				Enum: provision.ComponentEnum(info.Components),
			}},
			Exec: func(ctx context.Context, args string) (string, error) {
				var a struct{ Component string }
				if err := json.Unmarshal([]byte(args), &a); err != nil {
					return "", err
				}
				comp, err := provision.ResolveComponent(info.Components, a.Component)
				if err != nil {
					return "", err
				}
				r := ref
				r.Component = comp
				_, status, err := pc.Call(ctx, r, provision.VerbRestart, "POST", []byte("{}"))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("restart of %s returned %d", comp, status), nil
			},
		},
	}
}

// recentTranscript replays the last n chat messages for turn continuity.
func (e *EinoEngine) recentTranscript(scope store.Scope, sessionID string, n int) []engine.Message {
	events, err := e.server.store.ListEvents(context.Background(), scope, sessionID, 0, 0)
	if err != nil {
		return nil
	}
	var msgs []engine.Message
	for _, ev := range events {
		var role string
		switch ev.Type {
		case session.EventUserMessage:
			role = engine.RoleUser
		case session.EventAssistantMessage:
			role = engine.RoleAssistant
		default:
			continue
		}
		var d session.MessageData
		if session.DecodeData(ev, &d) == nil && d.Text != "" {
			msgs = append(msgs, engine.Message{Role: role, Content: d.Text})
		}
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	// The current user input is appended by the caller; drop it if it is the
	// trailing user message already recorded in the log.
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == engine.RoleUser {
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

// turnCallbacks bridges engine events to the host's status plumbing: text
// deltas stream into the transient partial; completed tool calls persist as
// durable turn.activity events with a human-readable detail (file path,
// component, template) pulled from the args.
func turnCallbacks(tc session.TurnContext) engine.Callbacks {
	cb := engine.Callbacks{}
	if tc.OnDelta != nil {
		cb.OnDelta = tc.OnDelta
		cb.OnToolStart = func(_, name, args string) { tc.OnDelta("\n⚙ " + name + " " + activityDetail(args) + "…\n") }
	}
	if tc.OnActivity != nil {
		cb.OnTool = func(ev engine.ToolEvent) {
			a := session.ToolActivityData{
				Tool:       ev.Name,
				Detail:     activityDetail(ev.Args),
				OK:         !ev.Err,
				DurationMS: ev.Duration.Milliseconds(),
			}
			if ev.Err {
				a.Error = ev.Result // observation carries the error text
			}
			tc.OnActivity(a)
		}
	}
	return cb
}

// activityDetail extracts the most human-meaningful arg (path, component,
// template name) from a tool-call args JSON.
func activityDetail(argsJSON string) string {
	var args map[string]any
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return ""
	}
	for _, key := range []string{"path", "component", "name", "repositoryRef"} {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	if tmpl, ok := args["template"].(map[string]any); ok {
		if v, ok := tmpl["name"].(string); ok {
			return v
		}
	}
	return ""
}

// Intake helpers.

type catalogEntry struct {
	Name, Description string
	Inputs            []string
}

func intakeCatalog(ctx context.Context, cl *client.Client) ([]catalogEntry, error) {
	items, err := cl.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]catalogEntry, 0, len(items))
	for i := range items {
		desc, _, _ := unstructured.NestedString(items[i].Object, "spec", "description")
		out = append(out, catalogEntry{
			Name: items[i].GetName(), Description: desc,
			Inputs: provision.InputNames(&items[i]),
		})
	}
	return out, nil
}

func catalogHas(c []catalogEntry, name string) bool {
	for _, e := range c {
		if e.Name == name {
			return true
		}
	}
	return false
}

func catalogNames(c []catalogEntry) string {
	names := make([]string, 0, len(c))
	for _, e := range c {
		names = append(names, e.Name)
	}
	return strings.Join(names, ", ")
}

func catalogText(c []catalogEntry) string {
	var b strings.Builder
	for _, e := range c {
		fmt.Fprintf(&b, "- %s: %s\n", e.Name, e.Description)
		if len(e.Inputs) > 0 {
			fmt.Fprintf(&b, "    inputs: %s\n", strings.Join(e.Inputs, ", "))
		}
	}
	return b.String()
}

// blueprintToolSchema mirrors session.Blueprint's JSON shape.
var blueprintToolSchema = map[string]any{
	"type":     "object",
	"required": []string{"title", "summary", "template"},
	"properties": map[string]any{
		"title":   map[string]any{"type": "string", "description": "short app title"},
		"summary": map[string]any{"type": "string", "description": "one sentence"},
		"template": map[string]any{
			"type": "object", "required": []string{"name"},
			"properties": map[string]any{
				"name":   map[string]any{"type": "string", "description": "catalog template name"},
				"reason": map[string]any{"type": "string"},
			},
		},
		"values":          map[string]any{"type": "object", "description": "template input values derived from answers"},
		"assumptions":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"successCriteria": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"questions": map[string]any{
			"type": "array", "maxItems": 3,
			"items": map[string]any{
				"type": "object", "required": []string{"id", "text", "options"},
				"properties": map[string]any{
					"id":   map[string]any{"type": "string"},
					"text": map[string]any{"type": "string", "description": "plain text, no markdown"},
					"options": map[string]any{
						"type": "array", "minItems": 2, "maxItems": 5,
						"items": map[string]any{
							"type": "object", "required": []string{"label"},
							"properties": map[string]any{
								"label":       map[string]any{"type": "string", "description": "single concrete choice"},
								"recommended": map[string]any{"type": "boolean"},
							},
						},
					},
				},
			},
		},
	},
}
