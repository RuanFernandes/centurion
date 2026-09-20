package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/store"
)

func TestSelectBuilderModelDelegatesAccountDefaultToCodex(t *testing.T) {
	models := []model.ModelInfo{
		{
			ID:          "gpt-5.6-sol",
			DisplayName: "GPT-5.6-Sol",
			IsDefault:   true,
			SupportedReasoningEfforts: []model.ReasoningEffort{
				{ReasoningEffort: "low"},
				{ReasoningEffort: "high"},
			},
		},
		{
			ID:          "gpt-5.6-luna",
			DisplayName: "GPT-5.6-Luna",
			SupportedReasoningEfforts: []model.ReasoningEffort{
				{ReasoningEffort: "medium"},
				{ReasoningEffort: "max"},
			},
		},
	}

	modelID, effort, err := selectBuilderModel(model.BuilderRequest{}, models)
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "" || effort != "" {
		t.Fatalf("account default was pinned to catalog defaults: model=%q effort=%q", modelID, effort)
	}
}

func TestSelectBuilderModelPreservesExplicitEffortForAccountDefault(t *testing.T) {
	modelID, effort, err := selectBuilderModel(model.BuilderRequest{ReasoningEffort: "MAX"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "" || effort != "max" {
		t.Fatalf("account model selection was not delegated: model=%q effort=%q", modelID, effort)
	}
}

func TestSelectBuilderModelPreservesExplicitSelection(t *testing.T) {
	models := []model.ModelInfo{
		{
			ID:          "gpt-5.6-sol",
			DisplayName: "GPT-5.6-Sol",
			IsDefault:   true,
			SupportedReasoningEfforts: []model.ReasoningEffort{
				{ReasoningEffort: "low"},
				{ReasoningEffort: "high"},
			},
		},
		{
			ID:          "gpt-5.6-luna",
			DisplayName: "GPT-5.6-Luna",
			SupportedReasoningEfforts: []model.ReasoningEffort{
				{ReasoningEffort: "medium"},
				{ReasoningEffort: "max"},
			},
		},
	}

	modelID, effort, err := selectBuilderModel(model.BuilderRequest{
		ModelID:         "GPT-5.6-Luna",
		ReasoningEffort: "MAX",
	}, models)
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "gpt-5.6-luna" || effort != "max" {
		t.Fatalf("explicit selection was not canonicalized: model=%q effort=%q", modelID, effort)
	}
}

func TestSelectBuilderModelRejectsSilentFallbacks(t *testing.T) {
	models := []model.ModelInfo{{ID: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol"}}

	if _, _, err := selectBuilderModel(model.BuilderRequest{ModelID: "gpt-5.6-luna"}, models); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unavailable model was not rejected: %v", err)
	}
	if _, _, err := selectBuilderModel(model.BuilderRequest{ModelID: "gpt-5.6-sol", ReasoningEffort: "max"}, models); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported effort was not rejected: %v", err)
	}
}

func TestBuilderFollowUpPromptReusesThreadContextInsteadOfRepeatingCatalog(t *testing.T) {
	project := model.Project{Name: "Token test", Folders: []string{"C:/PROJECT_ROOT_MARKER"}}
	agents := []model.AgentProfile{{ID: "agent-marker", Name: "AGENT_CATALOG_MARKER", Role: "Builder"}}
	servers := []model.MCPServer{{Name: "MCP_CATALOG_MARKER", Tools: []model.MCPTool{{Name: "tool-marker"}}}}
	templates := map[string]string{
		"orchestrator.builder": "Project={{project_context}}\nCatalog={{catalog_context}}\nRequest={{user_request}}",
	}

	bootstrap, err := buildBuilderPrompt("Create the first draft.", project, agents, servers, templates, "autonomous")
	if err != nil {
		t.Fatal(err)
	}
	followUp := buildBuilderFollowUpPrompt("Add a reviewer to the proposal.", "autonomous")

	for _, marker := range []string{"PROJECT_ROOT_MARKER", "AGENT_CATALOG_MARKER", "MCP_CATALOG_MARKER"} {
		if !strings.Contains(bootstrap, marker) {
			t.Fatalf("bootstrap prompt lost required context marker %q: %q", marker, bootstrap)
		}
		if strings.Contains(followUp, marker) {
			t.Fatalf("continuation prompt repeated bootstrap context marker %q: %q", marker, followUp)
		}
	}
	if strings.Contains(bootstrap, "gpt-5.6-sol") {
		t.Fatalf("builder bootstrap should not carry an unused model catalog: %q", bootstrap)
	}
	if !strings.Contains(bootstrap, "not literal Codex tool name") {
		t.Fatalf("builder bootstrap did not explain capability labels: %q", bootstrap)
	}
	if !strings.Contains(followUp, "Add a reviewer to the proposal.") {
		t.Fatalf("continuation prompt lost the latest request: %q", followUp)
	}
	if !strings.Contains(followUp, "not literal Codex tools") {
		t.Fatalf("continuation prompt did not retain the capability reminder: %q", followUp)
	}
	if len(followUp) >= len(bootstrap) {
		t.Fatalf("continuation prompt should be smaller than bootstrap: continuation=%d bootstrap=%d", len(followUp), len(bootstrap))
	}
}

func TestPlanningFollowUpPromptDoesNotRepeatProjectCatalog(t *testing.T) {
	bootstrap, err := buildPlanningPrompt(
		"Plan the release.",
		model.Project{Name: "Planning token test", Folders: []string{"C:/PLANNING_ROOT_MARKER"}},
		model.AgentProfile{Name: "Planner", Role: "Lead", Instructions: "Keep scope clear."},
		[]model.AgentProfile{{ID: "team-marker", Name: "PLANNING_TEAM_MARKER", Role: "Reviewer"}},
		map[string]string{"orchestrator.planner": "Project={{project_context}} Catalog={{catalog_context}} Request={{user_message}}"},
	)
	if err != nil {
		t.Fatal(err)
	}
	followUp := buildPlanningFollowUpPrompt("Add acceptance criteria.")

	if !strings.Contains(bootstrap, "PLANNING_ROOT_MARKER") || !strings.Contains(bootstrap, "PLANNING_TEAM_MARKER") {
		t.Fatalf("planning bootstrap lost required context: %q", bootstrap)
	}
	if strings.Contains(followUp, "PLANNING_ROOT_MARKER") || strings.Contains(followUp, "PLANNING_TEAM_MARKER") {
		t.Fatalf("planning continuation repeated bootstrap catalog: %q", followUp)
	}
	if !strings.Contains(followUp, "Add acceptance criteria.") {
		t.Fatalf("planning continuation lost latest request: %q", followUp)
	}
}

func TestBuilderThreadRecordedRestoresPersistedConversation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	dataStore, err := store.Open(filepath.Join(workspace, "centurion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()

	project := model.Project{ID: "project-token", Name: "Token project", Folders: []string{workspace}}
	if err := dataStore.SaveProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := dataStore.AppendHistory(ctx, model.HistoryEntry{
		ID:        "builder-history",
		ProjectID: project.ID,
		Kind:      "builder_prompt",
		Title:     "Builder request",
		Content:   "Create a workflow.",
		Metadata:  map[string]any{"threadID": "builder-thread-1"},
	}); err != nil {
		t.Fatal(err)
	}

	recorded, err := builderThreadRecorded(ctx, dataStore, project.ID, "builder-thread-1")
	if err != nil || !recorded {
		t.Fatalf("recorded Builder thread was not restored: recorded=%v err=%v", recorded, err)
	}
	missing, err := builderThreadRecorded(ctx, dataStore, project.ID, "other-thread")
	if err != nil || missing {
		t.Fatalf("unknown Builder thread was accepted: recorded=%v err=%v", missing, err)
	}
}
