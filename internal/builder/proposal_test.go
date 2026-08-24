package builder

import (
	"strings"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestParseProposalAcceptsCodeFence(t *testing.T) {
	proposal, err := ParseProposal("```json\n{\"schemaVersion\":1,\"summary\":\"Create a team\",\"agents\":[],\"workflow\":null}\n```")
	if err != nil {
		t.Fatalf("ParseProposal() error = %v", err)
	}
	if proposal.Summary != "Create a team" {
		t.Fatalf("unexpected summary: %q", proposal.Summary)
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

func TestNormalizeProposalRejectsUnknownAgentReference(t *testing.T) {
	proposal := model.BuilderProposal{
		Summary: "Invalid graph",
		Agents:  []model.BuilderAgentDraft{{TemporaryID: "lead", Name: "Lead", Role: "Lead", Instructions: "Work."}},
		Workflow: &model.WorkflowDefinition{
			Name:        "Invalid",
			EntryNodeID: "start",
			Nodes:       []model.WorkflowNode{{ID: "start", Type: "agent", AgentID: "missing"}},
		},
	}
	if _, err := NormalizeProposal(proposal, model.Project{}, Catalog{}); err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("expected unknown agent error, got %v", err)
	}
}

func TestApplyAgentDefaultsPreservesExplicitAgentModel(t *testing.T) {
	proposal := model.BuilderProposal{Agents: []model.BuilderAgentDraft{
		{Name: "Lead"},
		{Name: "Reviewer", ModelID: "review-model", ReasoningEffort: "high"},
	}}
	defaults := ApplyAgentDefaults(proposal, "builder-model", "xhigh")
	if defaults.Agents[0].ModelID != "builder-model" || defaults.Agents[0].ReasoningEffort != "xhigh" {
		t.Fatalf("missing builder defaults: %#v", defaults.Agents[0])
	}
	if defaults.Agents[1].ModelID != "review-model" || defaults.Agents[1].ReasoningEffort != "high" {
		t.Fatalf("explicit agent model was overwritten: %#v", defaults.Agents[1])
	}
}
