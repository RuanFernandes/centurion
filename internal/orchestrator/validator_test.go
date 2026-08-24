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
