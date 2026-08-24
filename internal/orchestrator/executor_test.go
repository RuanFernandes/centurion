package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/security"
)

func TestRuntimeEnforcesTurnLimits(t *testing.T) {
	rt := &runtime{maxTurns: 2, agentTurns: make(map[string]int)}
	if err := rt.reserveTurn("agent-a", 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.reserveTurn("agent-a", 1); err == nil {
		t.Fatal("expected per-agent turn limit to reject the second turn")
	}
	if err := rt.reserveTurn("agent-b", 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.reserveTurn("agent-b", 1); err == nil {
		t.Fatal("expected global turn limit to reject the third turn")
	}
}

func TestAgentPromptUsesOnlyDirectUpstreamOutputs(t *testing.T) {
	workflow := model.WorkflowDefinition{
		Edges: []model.WorkflowEdge{
			{ID: "edge-input", From: "input", To: "research"},
			{ID: "edge-research", From: "research", To: "writer"},
			{ID: "edge-unrelated", From: "unrelated", To: "review"},
		},
	}
	node := model.WorkflowNode{ID: "writer"}
	input := map[string]any{"goal": "prepare a release note"}
	scope := map[string]any{
		"research":  map[string]any{"summary": "prior-result"},
		"unrelated": map[string]any{"secret": "must-not-be-forwarded"},
		"oldNode":   map[string]any{"transcript": "old-context"},
	}

	prompt := buildAgentPrompt(model.AgentProfile{
		Name:          "Writer",
		Role:          "Technical writer",
		MemorySummary: "Keep release notes concise.",
	}, workflow, node, input, scope)

	if !strings.Contains(prompt, "prepare a release note") {
		t.Fatal("expected the run input in the prompt")
	}
	if !strings.Contains(prompt, "prior-result") {
		t.Fatal("expected the direct upstream output in the prompt")
	}
	if strings.Contains(prompt, "must-not-be-forwarded") || strings.Contains(prompt, "old-context") {
		t.Fatal("prompt included output from a non-upstream node")
	}
}

func TestAgentPromptUsesConfiguredSystemPromptBlocks(t *testing.T) {
	prompt := buildAgentPrompt(model.AgentProfile{
		Name:          "Writer",
		Role:          "Technical writer",
		Instructions:  "Keep the result concise.",
		MemorySummary: "Use the product glossary.",
	}, model.WorkflowDefinition{}, model.WorkflowNode{ID: "writer"}, map[string]any{}, nil, map[string]string{
		"agent.identity":          "Operator: {{agent_name}} / {{agent_role}}",
		"agent.persistent_memory": "Memory block:\n{{memory_summary}}",
		"agent.output_contract":   "Return only a JSON object.",
	})

	if !strings.Contains(prompt, "Operator: Writer / Technical writer") {
		t.Fatalf("configured identity prompt was not rendered: %q", prompt)
	}
	if !strings.Contains(prompt, "Memory block:\nUse the product glossary.") {
		t.Fatalf("configured memory prompt was not rendered: %q", prompt)
	}
	if !strings.Contains(prompt, "Return only a JSON object.") {
		t.Fatalf("configured output contract was not rendered: %q", prompt)
	}
}

func TestCompactJSONStaysWithinBudgetAndValid(t *testing.T) {
	value := map[string]any{
		"title":   "large result",
		"payload": strings.Repeat("x", 50_000),
		"items":   []any{strings.Repeat("y", 10_000), "keep this marker"},
	}
	const budget = 512
	encoded := compactJSON(value, budget)
	if len(encoded) > budget {
		t.Fatalf("compact JSON exceeded budget: got %d, want <= %d", len(encoded), budget)
	}
	var decoded any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("compact JSON is invalid: %v", err)
	}
}

func TestPromptToolsAreDeterministicAndDeduplicated(t *testing.T) {
	got := promptTools([]string{"git", "filesystem", "git", "  shell  "})
	if got != "filesystem, git, shell" {
		t.Fatalf("unexpected tool list: %q", got)
	}
}

func TestEffectiveAgentRootsStayInsideProject(t *testing.T) {
	projectRoot := t.TempDir()
	serviceRoot := filepath.Join(projectRoot, "service")
	if err := os.Mkdir(serviceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideRoot := t.TempDir()
	expectedProjectRoot, err := security.NormalizePath(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	expectedServiceRoot, err := security.NormalizePath(serviceRoot)
	if err != nil {
		t.Fatal(err)
	}

	if got := effectiveAgentRoots([]string{outsideRoot}, []string{projectRoot}); len(got) != 1 || got[0] != expectedProjectRoot {
		t.Fatalf("outside agent root should fall back to project root: %#v", got)
	}
	if got := effectiveAgentRoots([]string{serviceRoot}, []string{projectRoot}); len(got) != 1 || got[0] != expectedServiceRoot {
		t.Fatalf("nested agent root should narrow project scope: %#v", got)
	}
}
