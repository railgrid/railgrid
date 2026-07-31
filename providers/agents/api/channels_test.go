// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"strings"
	"testing"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

func TestNormalizeChannels(t *testing.T) {
	t.Run("wholly blank rows are ignored", func(t *testing.T) {
		out, err := normalizeChannels([]channelInput{{}, {Name: "  ", ConnectionRef: " "}})
		if err != nil {
			t.Fatalf("a row the user never filled in should not be an error: %v", err)
		}
		if len(out) != 0 {
			t.Fatalf("got %+v, want none", out)
		}
	})

	// Regression: a row with a connection but no name used to be dropped
	// silently, so the save reported success and bound nothing — the agent
	// looked configured in the portal but Discord/Telegram messages found no
	// agent for the connection.
	t.Run("a connection with no name is rejected, not dropped", func(t *testing.T) {
		_, err := normalizeChannels([]channelInput{{ConnectionRef: "dicordia"}})
		if err == nil {
			t.Fatal("want an error; silently dropping the row reports a save that did nothing")
		}
		if !strings.Contains(err.Error(), "dicordia") {
			t.Fatalf("the error should name the connection so the user can find it: %v", err)
		}
	})

	t.Run("a name with no connection is rejected", func(t *testing.T) {
		if _, err := normalizeChannels([]channelInput{{Name: "primary"}}); err == nil {
			t.Fatal("want an error for a named row with no connection")
		}
	})

	t.Run("duplicate names are rejected", func(t *testing.T) {
		_, err := normalizeChannels([]channelInput{
			{Name: "primary", ConnectionRef: "a"},
			{Name: "primary", ConnectionRef: "b"},
		})
		if err == nil {
			t.Fatal("want an error for duplicate channel names")
		}
	})

	t.Run("exactly one primary, defaulting to the first row", func(t *testing.T) {
		out, err := normalizeChannels([]channelInput{
			{Name: "primary", ConnectionRef: "tg"},
			{Name: "incidents", ConnectionRef: "slack"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || !out[0].Primary || out[1].Primary {
			t.Fatalf("got %+v, want the first row primary", out)
		}
	})

	t.Run("an explicit primary wins and later ones are cleared", func(t *testing.T) {
		out, err := normalizeChannels([]channelInput{
			{Name: "a", ConnectionRef: "x"},
			{Name: "b", ConnectionRef: "y", Primary: true},
			{Name: "c", ConnectionRef: "z", Primary: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out[0].Primary || !out[1].Primary || out[2].Primary {
			t.Fatalf("got %+v, want only 'b' primary", out)
		}
	})
}

// Inbound routing finds the agent by the connection its channels reference —
// the lookup behind "No agent is bound to this Discord connection yet".
func TestAgentClaimsConnection(t *testing.T) {
	spec := agentsv1alpha1.AgentSpec{
		Channels: []agentsv1alpha1.AgentChannel{
			{Name: "primary", ConnectionRef: "dicordia", Primary: true},
			{Name: "news", ConnectionRef: "tg"},
		},
	}
	if !spec.AgentClaimsConnection("dicordia") {
		t.Fatal("the agent must claim the connection its primary channel points at")
	}
	if !spec.AgentClaimsConnection("tg") {
		t.Fatal("secondary channels bind for inbound too")
	}
	if spec.AgentClaimsConnection("other") {
		t.Fatal("an unbound connection must not match")
	}
	var empty agentsv1alpha1.AgentSpec
	if empty.AgentClaimsConnection("dicordia") {
		t.Fatal("an agent with no channels claims nothing")
	}
}
