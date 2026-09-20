package builder

import (
	"strings"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestOutputSchemaRequiresEveryDeclaredProperty(t *testing.T) {
	assertStrictSchemaProperties(t, OutputSchema(), "$")
}

func assertStrictSchemaProperties(t *testing.T, value any, path string) {
	t.Helper()
	schema, ok := value.(map[string]any)
	if !ok {
		return
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		required := make(map[string]struct{})
		if entries, ok := schema["required"].([]string); ok {
			for _, entry := range entries {
				required[entry] = struct{}{}
			}
		}
		for name := range properties {
			if _, ok := required[name]; !ok {
				t.Errorf("%s.properties.%s is not listed in required", path, name)
			}
			assertStrictSchemaProperties(t, properties[name], path+".properties."+name)
		}
	}
	if items, ok := schema["items"]; ok {
		assertStrictSchemaProperties(t, items, path+".items")
	}
}

func TestParseProposalAcceptsCodeFence(t *testing.T) {
	proposal, err := ParseProposal("```json\n{\"schemaVersion\":1,\"summary\":\"Create a team\",\"agents\":[],\"workflow\":null}\n```")
	if err != nil {
		t.Fatalf("ParseProposal() error = %v", err)
	}
	if proposal.Summary != "Create a team" {
		t.Fatalf("unexpected summary: %q", proposal.Summary)
	}
}

func TestParseProposalAcceptsStringSchemaVersion(t *testing.T) {
	proposal, err := ParseProposal(`{"schemaVersion":"1","summary":"Create a team","agents":[],"workflow":null}`)
	if err != nil {
		t.Fatalf("ParseProposal() error = %v", err)
	}
	if proposal.SchemaVersion != 1 || proposal.Summary != "Create a team" {
		t.Fatalf("string schemaVersion was not normalized: %#v", proposal)
	}
}

func TestParseProposalMapsConnectionAliases(t *testing.T) {
	proposal, err := ParseProposal(`{"schemaVersion":1,"summary":"Connected team","agents":[],"workflow":{"id":"flow","name":"Flow","entryNodeID":"start","nodes":[{"id":"start","type":"agent","agentID":"lead"},{"id":"review","type":"agent","agentID":"reviewer"}],"connections":[{"source":"start","target":"review"}]}}`)
	if err != nil {
		t.Fatalf("ParseProposal() error = %v", err)
	}
	if len(proposal.Workflow.Edges) != 1 || proposal.Workflow.Edges[0].From != "start" || proposal.Workflow.Edges[0].To != "review" {
		t.Fatalf("connection alias was not mapped to edges: %#v", proposal.Workflow.Edges)
	}
}

func TestParseProposalMapsNodeNextAliases(t *testing.T) {
	proposal, err := ParseProposal(`{"schemaVersion":1,"summary":"Connected team","agents":[],"workflow":{"id":"flow","name":"Flow","entryNodeID":"start","nodes":[{"id":"start","type":"agent","agentID":"lead","next":"review"},{"id":"review","type":"agent","agentID":"reviewer","next":[{"node":"done","condition":"truthy:review.approved"}]},{"id":"done","type":"artifact"}]}}`)
	if err != nil {
		t.Fatalf("ParseProposal() error = %v", err)
	}
	if len(proposal.Workflow.Edges) != 2 {
		t.Fatalf("next aliases were not mapped to edges: %#v", proposal.Workflow.Edges)
	}
	if proposal.Workflow.Edges[0].From != "start" || proposal.Workflow.Edges[0].To != "review" {
		t.Fatalf("unexpected first next edge: %#v", proposal.Workflow.Edges[0])
	}
	if proposal.Workflow.Edges[1].Condition != "truthy:review.approved" {
		t.Fatalf("next edge condition was not preserved: %#v", proposal.Workflow.Edges[1])
	}
}

func TestParseProposalMapsLegacyAgentFieldAliases(t *testing.T) {
	proposal, err := ParseProposal(`{"schemaVersion":1,"summary":"Legacy proposal","agents":[{"temporaryID":"builder","name":"Builder","avatarID":"builder","model":"builder-model","prompt":"Implement the approved changes and run tests.","permissions":["files.read","files.write"],"mcpTools":["github.create_issue"],"approvalProfile":"autonomous"}],"workflow":null}`)
	if err != nil {
		t.Fatalf("ParseProposal() error = %v", err)
	}
	agent := proposal.Agents[0]
	if agent.ModelID != "builder-model" || agent.Instructions != "Implement the approved changes and run tests." {
		t.Fatalf("legacy model or prompt aliases were not mapped: %#v", agent)
	}
	if agent.Role != "Software implementer" {
		t.Fatalf("legacy avatar did not infer a role: %q", agent.Role)
	}
	if len(agent.ToolAllowlist) != 3 {
		t.Fatalf("legacy permissions and MCP tools were not merged: %#v", agent.ToolAllowlist)
	}
}

func TestNormalizeProposalConnectsNodesWhenEdgesAreMissing(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary: "Connected team",
		Agents:  []model.BuilderAgentDraft{{TemporaryID: "lead", Name: "Lead", Role: "Lead", Instructions: "Lead."}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Flow",
			EntryNodeID: "start",
			Nodes: []model.WorkflowNode{
				{ID: "start", Type: "agent", AgentID: "lead"},
				{ID: "review", Type: "artifact"},
				{ID: "done", Type: "artifact"},
			},
		},
	}
	normalized, err := NormalizeProposal(proposal, model.Project{}, Catalog{})
	if err != nil {
		t.Fatalf("NormalizeProposal() error = %v", err)
	}
	if len(normalized.Workflow.Edges) != 2 {
		t.Fatalf("expected two inferred edges, got %#v", normalized.Workflow.Edges)
	}
	if normalized.Workflow.Edges[0].From != "start" || normalized.Workflow.Edges[0].To != "review" || normalized.Workflow.Edges[1].To != "done" {
		t.Fatalf("unexpected inferred route: %#v", normalized.Workflow.Edges)
	}
	if !strings.Contains(strings.Join(normalized.Notes, "\n"), "linked them in declared order") {
		t.Fatalf("expected inferred-route note, got %#v", normalized.Notes)
	}
}

func TestNormalizeProposalConstrainsAccessRootsAndLoops(t *testing.T) {
	root := t.TempDir()
	proposal := model.BuilderProposal{
		Summary: "Create implementation team",
		Agents: []model.BuilderAgentDraft{{
			TemporaryID:     "lead",
			Name:            "Tech Lead",
			Role:            "Technical lead",
			Instructions:    "Coordinate the work.",
			ApprovalProfile: "full access",
			WorkspaceRoots:  []string{root},
			ToolAllowlist:   []string{"files.read", "unknown.secret"},
		}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Delivery",
			EntryNodeID: "start",
			Nodes: []model.WorkflowNode{
				{ID: "start", Type: "agent", Label: "Lead", AgentID: "lead"},
				{ID: "loop", Type: "loop", Label: "Review", MaxIterations: 99},
			},
			Edges: []model.WorkflowEdge{{ID: "edge", From: "start", To: "loop"}},
		},
	}
	normalized, err := NormalizeProposal(proposal, model.Project{Folders: []string{root}}, Catalog{})
	if err != nil {
		t.Fatalf("NormalizeProposal() error = %v", err)
	}
	if normalized.Agents[0].ApprovalProfile != "autonomous" {
		t.Fatalf("approval profile was not normalized: %q", normalized.Agents[0].ApprovalProfile)
	}
	if len(normalized.Agents[0].ToolAllowlist) != 1 {
		t.Fatalf("unexpected permissions: %#v", normalized.Agents[0].ToolAllowlist)
	}
	if normalized.Workflow.Nodes[1].MaxIterations != 20 {
		t.Fatalf("loop limit was not capped: %d", normalized.Workflow.Nodes[1].MaxIterations)
	}
	if !strings.Contains(strings.Join(normalized.Notes, "\n"), "not in the current catalog") {
		t.Fatalf("expected catalog note, got %#v", normalized.Notes)
	}
}

func TestNormalizeProposalRepairsWorkflowAgentReferences(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary: "Repair workflow references",
		Agents: []model.BuilderAgentDraft{{
			TemporaryID:     "agent-research",
			Name:            "Research",
			Role:            "Researcher",
			Instructions:    "Research the topic.",
			ApprovalProfile: "on_request",
		}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Research flow",
			EntryNodeID: "node-gate0",
			Nodes: []model.WorkflowNode{{
				ID:      "node-gate0",
				Type:    "agent",
				Label:   "Research",
				AgentID: "agent-research-gate0",
			}},
		},
	}
	normalized, err := NormalizeProposal(proposal, model.Project{}, Catalog{})
	if err != nil {
		t.Fatalf("NormalizeProposal() error = %v", err)
	}
	if normalized.Workflow.Nodes[0].AgentID != "agent-research" {
		t.Fatalf("workflow reference was not repaired: %q", normalized.Workflow.Nodes[0].AgentID)
	}
	if len(normalized.Agents) != 1 {
		t.Fatalf("unexpected agent count after repair: %d", len(normalized.Agents))
	}
}

func TestNormalizeProposalAddsPlaceholderForMissingWorkflowAgent(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary: "Complete workflow",
		Agents:  []model.BuilderAgentDraft{{TemporaryID: "lead", Name: "Lead", Role: "Lead", Instructions: "Lead."}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Complete flow",
			EntryNodeID: "start",
			Nodes:       []model.WorkflowNode{{ID: "start", Type: "agent", AgentID: "missing-specialist"}},
		},
	}
	normalized, err := NormalizeProposal(proposal, model.Project{}, Catalog{})
	if err != nil {
		t.Fatalf("NormalizeProposal() error = %v", err)
	}
	if len(normalized.Agents) != 2 || normalized.Workflow.Nodes[0].AgentID != "missing-specialist" {
		t.Fatalf("missing workflow agent was not materialized: agents=%#v workflow=%#v", normalized.Agents, normalized.Workflow.Nodes[0])
	}
	if normalized.Agents[1].ApprovalProfile != "on_request" {
		t.Fatalf("placeholder agent was not approval-gated: %q", normalized.Agents[1].ApprovalProfile)
	}
}

func TestApplyAgentModelSelectionOverridesAgentModels(t *testing.T) {
	proposal := model.BuilderProposal{Agents: []model.BuilderAgentDraft{
		{Name: "Lead"},
		{Name: "Reviewer", ModelID: "review-model", ReasoningEffort: "high"},
	}}
	selected := ApplyAgentModelSelection(proposal, "builder-model", "xhigh")
	for _, agent := range selected.Agents {
		if agent.ModelID != "builder-model" || agent.ReasoningEffort != "xhigh" {
			t.Fatalf("application model selection was not enforced: %#v", agent)
		}
	}

	accountDefault := ApplyAgentModelSelection(proposal, "", "")
	for _, agent := range accountDefault.Agents {
		if agent.ModelID != "" || agent.ReasoningEffort != "" {
			t.Fatalf("account defaults were not delegated to Codex: %#v", agent)
		}
	}
}

func TestApplySubagentPermissionsUsesTheExplicitBuilderChoice(t *testing.T) {
	proposal := model.BuilderProposal{Agents: []model.BuilderAgentDraft{
		{Name: "Lead", ApprovalProfile: "on_request"},
		{Name: "Builder", ApprovalProfile: "autonomous"},
	}}

	fullAccess := ApplySubagentPermissions(proposal, "autonomous")
	for _, agent := range fullAccess.Agents {
		if agent.ApprovalProfile != "autonomous" {
			t.Fatalf("full access choice was not applied: %#v", fullAccess.Agents)
		}
	}

	approval := ApplySubagentPermissions(proposal, "on_request")
	for _, agent := range approval.Agents {
		if agent.ApprovalProfile != "on_request" {
			t.Fatalf("approval choice was not applied: %#v", approval.Agents)
		}
	}
}

func TestApplyExecutionBriefPersistsBriefAndNodePrompts(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary: "Build the product",
		Agents:  []model.BuilderAgentDraft{{TemporaryID: "builder", Name: "Builder", Role: "Implementer", Instructions: "Implement the approved scope."}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Delivery",
			EntryNodeID: "start",
			Nodes: []model.WorkflowNode{{
				ID:      "start",
				Type:    "agent",
				AgentID: "builder",
				Prompt:  "Create the backend and run the tests.",
			}},
		},
	}

	normalized, err := NormalizeProposal(proposal, model.Project{}, Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	normalized = ApplyExecutionBrief(normalized, "Build the agreed product in the active workspace.")
	if normalized.ExecutionBrief == "" || normalized.Workflow.ExecutionBrief != normalized.ExecutionBrief {
		t.Fatalf("execution brief was not persisted: %#v", normalized)
	}
	if normalized.Workflow.Nodes[0].Prompt != "Create the backend and run the tests." {
		t.Fatalf("node prompt was not preserved: %#v", normalized.Workflow.Nodes[0])
	}
}

func TestApplyExecutionBriefPrefersCompactBuilderBriefOverRawHandoff(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary:        "Create the release workflow.",
		ExecutionBrief: "Implement the API, validate it, and report the changed paths.",
		Workflow:       &model.WorkflowDefinition{},
	}
	rawHandoff := "RAW_HANDOFF_MUST_NOT_BE_REPEATED " + strings.Repeat("planning detail ", 2_000)

	result := ApplyExecutionBrief(proposal, rawHandoff)
	if result.ExecutionBrief != proposal.ExecutionBrief {
		t.Fatalf("compact Builder brief was not preserved: %q", result.ExecutionBrief)
	}
	if result.Workflow.ExecutionBrief != proposal.ExecutionBrief {
		t.Fatalf("workflow did not receive compact Builder brief: %q", result.Workflow.ExecutionBrief)
	}
	if strings.Contains(result.ExecutionBrief, "RAW_HANDOFF_MUST_NOT_BE_REPEATED") {
		t.Fatalf("raw planning handoff leaked into execution brief: %q", result.ExecutionBrief)
	}
}

func TestNormalizeProposalInfersAgentPresetFromResponsibility(t *testing.T) {
	proposal := model.BuilderProposal{Summary: "Team", Agents: []model.BuilderAgentDraft{
		{Name: "Lead", Role: "Technical lead", Instructions: "Coordinate the team."},
		{Name: "Scout", Role: "Researcher", Instructions: "Find evidence."},
		{Name: "QA", Role: "Quality reviewer", Instructions: "Review the implementation."},
		{Name: "API", Role: "Backend implementer", Instructions: "Build the service."},
	}}

	normalized, err := NormalizeProposal(proposal, model.Project{}, Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{normalized.Agents[0].AvatarID, normalized.Agents[1].AvatarID, normalized.Agents[2].AvatarID, normalized.Agents[3].AvatarID}
	want := []string{"supervisor", "researcher", "reviewer", "builder"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("agent %d preset = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestNormalizeProposalPreservesSafeFallbackTarget(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary: "Fallback flow",
		Agents:  []model.BuilderAgentDraft{{TemporaryID: "lead", Name: "Lead", Role: "Lead", Instructions: "Lead."}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Fallback flow",
			EntryNodeID: "start",
			ErrorPolicy: "fallback",
			Nodes: []model.WorkflowNode{
				{ID: "start", Type: "agent", AgentID: "lead", Config: map[string]any{"fallbackNodeID": "recover"}},
				{ID: "recover", Type: "artifact"},
			},
			Edges: []model.WorkflowEdge{{ID: "edge", From: "start", To: "recover"}},
		},
	}
	normalized, err := NormalizeProposal(proposal, model.Project{}, Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Workflow.Nodes[0].Config["fallbackNodeID"] != "recover" {
		t.Fatalf("fallback target was dropped during normalization: %#v", normalized.Workflow.Nodes[0].Config)
	}
}
