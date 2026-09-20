package orchestrator

import (
	"testing"

	"github.com/RuanFernandes/centurion/internal/model"
)

func TestValidateWorkflowAllowsOnlyBoundedCycles(t *testing.T) {
	base := model.WorkflowDefinition{
		ID: "wf", Name: "Workflow", Version: 1, EntryNodeID: "start", ErrorPolicy: "stop",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: 60, MaxParallel: 2, MaxTurns: 4},
		Nodes: []model.WorkflowNode{
			{ID: "start", Type: "condition", Label: "Start"},
			{ID: "loop", Type: "loop", Label: "Loop", MaxIterations: 2, Config: map[string]any{"onExhausted": "end"}},
			{ID: "end", Type: "artifact", Label: "End"},
		},
		Edges: []model.WorkflowEdge{{ID: "a", From: "start", To: "loop"}, {ID: "b", From: "loop", To: "start"}, {ID: "c", From: "loop", To: "end"}},
	}
	if result := ValidateWorkflow(base); !result.Valid {
		t.Fatalf("bounded cycle should be valid: %#v", result.Errors)
	}

	base.Nodes[1].Type = "tool"
	base.Nodes[1].MaxIterations = 0
	base.Nodes[1].Config = nil
	if result := ValidateWorkflow(base); result.Valid {
		t.Fatal("unbounded cycle should be invalid")
	}
}

func TestEvaluateConditionIsDeclarative(t *testing.T) {
	scope := map[string]any{
		"review": map[string]any{"approved": true, "score": 4.0, "labels": []any{"safe", "ready"}},
	}
	cases := []struct {
		expression string
		want       bool
	}{
		{"truthy:review.approved", true},
		{"equals:review.approved:true", true},
		{"notEquals:review.approved:false", true},
		{"greaterThan:review.score:3", true},
		{"contains:review.labels:ready", true},
		{"exists:review.missing", false},
	}
	for _, testCase := range cases {
		got, err := EvaluateCondition(testCase.expression, scope)
		if err != nil {
			t.Fatalf("%s: %v", testCase.expression, err)
		}
		if got != testCase.want {
			t.Errorf("%s = %v, want %v", testCase.expression, got, testCase.want)
		}
	}
	if _, err := EvaluateCondition("javascript:alert(1)", scope); err == nil {
		t.Fatal("arbitrary condition expression was accepted")
	}
}

func TestValidateWorkflowRejectsUnsupportedLimitsAndPolicies(t *testing.T) {
	workflow := model.WorkflowDefinition{
		ID: "wf-limits", Name: "Limits", Version: 1, EntryNodeID: "start", ErrorPolicy: "retry-forever",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: maxWorkflowDurationSeconds + 1, MaxParallel: maxWorkflowParallel + 1, MaxTurns: maxWorkflowTurns + 1},
		Nodes: []model.WorkflowNode{
			{ID: "start", Type: "agent", AgentID: "agent", Retry: model.RetryPolicy{MaxAttempts: maxRetryAttempts + 1}},
		},
	}
	result := ValidateWorkflow(workflow)
	if result.Valid {
		t.Fatal("expected unsafe limits and unknown error policy to be rejected")
	}
	for _, code := range []string{"workflow.limit.duration.max", "workflow.limit.parallel.max", "workflow.limit.turns.max", "workflow.error_policy.invalid", "node.retry.attempts.max"} {
		found := false
		for _, issue := range result.Errors {
			if issue.Code == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected validation issue %q, got %#v", code, result.Errors)
		}
	}
}

func TestValidateWorkflowRejectsNestedParallel(t *testing.T) {
	workflow := model.WorkflowDefinition{
		ID: "wf-nested", Name: "Nested", Version: 1, EntryNodeID: "outer", ErrorPolicy: "stop",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: 60, MaxParallel: 2, MaxTurns: 10},
		Nodes: []model.WorkflowNode{
			{ID: "outer", Type: "parallel"},
			{ID: "branch", Type: "agent", AgentID: "agent"},
			{ID: "inner", Type: "parallel"},
			{ID: "join", Type: "join"},
		},
		Edges: []model.WorkflowEdge{
			{ID: "outer-branch", From: "outer", To: "branch"},
			{ID: "outer-inner", From: "outer", To: "inner"},
			{ID: "branch-inner", From: "branch", To: "inner"},
			{ID: "inner-join", From: "inner", To: "join"},
		},
	}
	result := ValidateWorkflow(workflow)
	if result.Valid {
		t.Fatal("expected nested parallel workflow to be rejected")
	}
	for _, issue := range result.Errors {
		if issue.Code == "parallel.nested.unsupported" {
			return
		}
	}
	t.Fatalf("expected nested parallel validation error, got %#v", result.Errors)
}

func TestValidateWorkflowRejectsDisconnectedMultiNodeGraph(t *testing.T) {
	workflow := model.WorkflowDefinition{
		ID: "wf-disconnected", Name: "Disconnected", Version: 1, EntryNodeID: "start", ErrorPolicy: "stop",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: 60, MaxParallel: 2, MaxTurns: 4},
		Nodes: []model.WorkflowNode{
			{ID: "start", Type: "agent", AgentID: "agent-start"},
			{ID: "finish", Type: "artifact"},
		},
	}
	result := ValidateWorkflow(workflow)
	if result.Valid {
		t.Fatal("a multi-node workflow without connections must not be runnable")
	}
	for _, issue := range result.Errors {
		if issue.Code == "workflow.graph.disconnected" {
			return
		}
	}
	t.Fatalf("expected disconnected graph validation error, got %#v", result.Errors)
}

func TestValidateWorkflowRejectsUnreachableNode(t *testing.T) {
	workflow := model.WorkflowDefinition{
		ID: "wf-unreachable", Name: "Unreachable", Version: 1, EntryNodeID: "start", ErrorPolicy: "stop",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: 60, MaxParallel: 2, MaxTurns: 4},
		Nodes: []model.WorkflowNode{
			{ID: "start", Type: "artifact"},
			{ID: "finish", Type: "artifact"},
			{ID: "orphan", Type: "artifact"},
		},
		Edges: []model.WorkflowEdge{
			{ID: "start-finish", From: "start", To: "finish"},
		},
	}
	result := ValidateWorkflow(workflow)
	if result.Valid {
		t.Fatal("an unreachable workflow node must not be runnable")
	}
	for _, issue := range result.Errors {
		if issue.Code == "node.unreachable" {
			return
		}
	}
	t.Fatalf("expected unreachable node validation error, got %#v", result.Errors)
}

func TestValidateWorkflowRejectsSingleBranchParallelNode(t *testing.T) {
	workflow := model.WorkflowDefinition{
		ID: "wf-parallel", Name: "Parallel", Version: 1, EntryNodeID: "split", ErrorPolicy: "stop",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: 60, MaxParallel: 2, MaxTurns: 4},
		Nodes: []model.WorkflowNode{
			{ID: "split", Type: "parallel"},
			{ID: "finish", Type: "artifact"},
		},
		Edges: []model.WorkflowEdge{{ID: "split-finish", From: "split", To: "finish"}},
	}
	result := ValidateWorkflow(workflow)
	if result.Valid {
		t.Fatal("a parallel node with one branch must not be runnable")
	}
	for _, issue := range result.Errors {
		if issue.Code == "parallel.single.branch" {
			return
		}
	}
	t.Fatalf("expected parallel branch validation error, got %#v", result.Errors)
}
