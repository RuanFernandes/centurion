package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
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

func TestRuntimeEnforcesPromptTokenBudget(t *testing.T) {
	rt := &runtime{maxPromptTokens: 100, agentTurns: make(map[string]int)}
	used, err := rt.reservePromptTokens(60)
	if err != nil || used != 60 {
		t.Fatalf("expected the first prompt reservation to succeed: used=%d err=%v", used, err)
	}
	if _, err := rt.reservePromptTokens(41); err == nil {
		t.Fatal("expected the prompt budget to reject the reservation")
	}
	used, err = rt.reservePromptTokens(40)
	if err != nil || used != 100 {
		t.Fatalf("expected an exact budget reservation to succeed: used=%d err=%v", used, err)
	}
}

func TestPreferredParallelErrorPreservesFailureOverSiblingCancellation(t *testing.T) {
	failure := errors.New("workspace write failed")
	got := preferredParallelError(nil, []branchResult{
		{err: context.Canceled},
		{err: failure},
	})
	if !errors.Is(got, failure) {
		t.Fatalf("expected actual branch failure, got %v", got)
	}
}

func TestAgentPromptUsesOnlyReachableUpstreamOutputs(t *testing.T) {
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

func TestAgentPromptCarriesContextThroughControlNodes(t *testing.T) {
	workflow := model.WorkflowDefinition{
		Nodes: []model.WorkflowNode{
			{ID: "research", Type: "agent"},
			{ID: "quality", Type: "condition"},
			{ID: "join", Type: "join"},
			{ID: "build", Type: "agent"},
			{ID: "review", Type: "agent"},
		},
		Edges: []model.WorkflowEdge{
			{ID: "research-quality", From: "research", To: "quality"},
			{ID: "quality-build", From: "quality", To: "build"},
			{ID: "review-join", From: "review", To: "join"},
			{ID: "build-join", From: "build", To: "join"},
			{ID: "join-next", From: "join", To: "review"},
		},
	}
	scope := map[string]any{
		"research": map[string]any{"plan": "research-output"},
		"quality":  map[string]any{"result": true},
		"build":    map[string]any{"code": "build-output"},
		"review":   map[string]any{"notes": "review-output"},
		"join":     map[string]any{"status": "completed"},
	}

	conditionPrompt := buildAgentPrompt(model.AgentProfile{Name: "Builder"}, workflow, model.WorkflowNode{ID: "build"}, nil, scope)
	if !strings.Contains(conditionPrompt, "research-output") || !strings.Contains(conditionPrompt, "quality") {
		t.Fatalf("agent after condition lost upstream context: %s", conditionPrompt)
	}

	joinPrompt := buildAgentPrompt(model.AgentProfile{Name: "Reviewer"}, workflow, model.WorkflowNode{ID: "review"}, nil, scope)
	if !strings.Contains(joinPrompt, "build-output") || !strings.Contains(joinPrompt, "review-output") {
		t.Fatalf("agent after join lost branch context: %s", joinPrompt)
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

func TestAgentPromptIncludesExecutionBriefAndNodeTask(t *testing.T) {
	prompt := buildAgentPrompt(
		model.AgentProfile{Name: "Implementation Builder", Role: "Backend implementer"},
		model.WorkflowDefinition{ExecutionBrief: "Build the agreed ProspectOS backend."},
		model.WorkflowNode{ID: "build", Prompt: "Implement the API and validate it with tests."},
		nil,
		nil,
	)
	if !strings.Contains(prompt, "Build the agreed ProspectOS backend.") {
		t.Fatalf("execution brief was not included: %q", prompt)
	}
	if !strings.Contains(prompt, "Implement the API and validate it with tests.") {
		t.Fatalf("node task was not included: %q", prompt)
	}
}

func TestAgentContinuationPromptDoesNotRepeatTheFullWorkflowTask(t *testing.T) {
	node := model.WorkflowNode{
		ID:     "implement-api",
		Label:  "Implement API",
		Prompt: "FULL_NODE_TASK_MUST_NOT_BE_REPEATED " + strings.Repeat("detail ", 1_000),
	}

	prompt := buildAgentContinuationPrompt(node)
	if !strings.Contains(prompt, "implement-api") || !strings.Contains(prompt, "Implement API") {
		t.Fatalf("continuation prompt lost the step identity: %q", prompt)
	}
	if strings.Contains(prompt, "FULL_NODE_TASK_MUST_NOT_BE_REPEATED") {
		t.Fatalf("continuation prompt repeated the full original task: %q", prompt)
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
	if !strings.Contains(got, "- filesystem:") || !strings.Contains(got, "- git:") || !strings.Contains(got, "- shell:") {
		t.Fatalf("unexpected tool list: %q", got)
	}
	if !strings.Contains(got, "not literal Codex tool names") {
		t.Fatalf("tool capability guidance was omitted: %q", got)
	}
}

func TestPromptToolsExplainsFileWriteCapability(t *testing.T) {
	got := promptTools([]string{"files.read", "files.write"})
	if !strings.Contains(got, "files.write: create or modify files") {
		t.Fatalf("file-write capability was not rendered: %q", got)
	}
	if !strings.Contains(got, "minimal workspace edit") {
		t.Fatalf("file-write guidance was not rendered: %q", got)
	}
}

func TestAgentPromptEndsWithFileWriteRuntimeGuidance(t *testing.T) {
	prompt := buildAgentPrompt(
		model.AgentProfile{ToolAllowlist: []string{"files.read", "files.write"}},
		model.WorkflowDefinition{},
		model.WorkflowNode{Prompt: "Do not run commands outside the scoped task."},
		nil,
		nil,
	)
	nodeTaskOffset := strings.Index(prompt, "Do not run commands outside the scoped task.")
	runtimeOffset := strings.Index(prompt, "Centurion runtime permission interpretation:")
	if nodeTaskOffset < 0 || runtimeOffset <= nodeTaskOffset {
		t.Fatalf("runtime capability guidance must follow the node task: %q", prompt)
	}
}

func TestParseAgentOutputExtractsStructuredJSONAfterPreamble(t *testing.T) {
	output := "I inspected the workspace.\n{\"status\":\"blocked\",\"result\":\"implementation_not_started\",\"blockers\":[\"No approved brief\"]}"
	parsed := parseAgentOutput(output, "completed")
	if parsed["status"] != "blocked" || parsed["result"] != "implementation_not_started" {
		t.Fatalf("structured result was not extracted: %#v", parsed)
	}
	blocked, reason := agentOutputBlocked(parsed)
	if !blocked || !strings.Contains(reason, "No approved brief") {
		t.Fatalf("expected a blocking result, got blocked=%v reason=%q", blocked, reason)
	}
}

func TestCompletedTurnWithBlockingAgentResultDoesNotLookSuccessful(t *testing.T) {
	parsed := parseAgentOutput(`{"status":"completed","decision":"do_not_route_builder","nextStep":"Define a bounded scope first."}`, "completed")
	blocked, reason := agentOutputBlocked(parsed)
	if !blocked || !strings.Contains(reason, "Define a bounded scope first") {
		t.Fatalf("expected semantic blocker to stop the workflow: blocked=%v reason=%q", blocked, reason)
	}
}

func TestCompletedTurnWithLocalizedFailureResultFailsTheWorkflow(t *testing.T) {
	parsed := parseAgentOutput(`{"status":"falha","blocker":"Arquivo não alterado."}`, "completed")
	failed, reason := agentOutputFailed(parsed)
	if !failed || !strings.Contains(reason, "Arquivo não alterado") {
		t.Fatalf("expected a semantic failure, got failed=%v reason=%q", failed, reason)
	}
	if blocked, _ := agentOutputBlocked(parsed); blocked {
		t.Fatalf("a failure result must not be downgraded to blocked: %#v", parsed)
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
