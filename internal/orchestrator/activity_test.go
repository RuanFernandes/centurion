package orchestrator

import "testing"

func TestAgentActivityForMethodUsesSafeOperationalSummaries(t *testing.T) {
	tests := []struct {
		method   string
		activity string
	}{
		{method: "item/reasoning/summaryTextDelta", activity: "Reviewing the request"},
		{method: "item/commandExecution/started", activity: "Running a tool step"},
		{method: "item/agentMessage/delta", activity: "Drafting the result"},
		{method: "turn/completed", activity: "Finishing the turn"},
	}
	for _, test := range tests {
		phase := agentActivityForMethod(test.method)
		if phase.Activity != test.activity {
			t.Fatalf("method %q mapped to %q, want %q", test.method, phase.Activity, test.activity)
		}
		if phase.Detail == "" {
			t.Fatalf("method %q has no detail", test.method)
		}
	}
}

func TestAgentActivityForMethodDoesNotEchoPrivatePayloads(t *testing.T) {
	phase := agentActivityForMethod("item/reasoning/private-secret")
	if phase.Activity == "item/reasoning/private-secret" || phase.Detail == "item/reasoning/private-secret" {
		t.Fatal("activity summary echoed the protocol method")
	}
}
