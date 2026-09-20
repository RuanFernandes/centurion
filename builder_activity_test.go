package main

import "testing"

func TestBuilderActivityForCodexMethodUsesSafeSummaries(t *testing.T) {
	tests := []struct {
		method   string
		activity string
	}{
		{method: "item/reasoning/summaryTextDelta", activity: "Reviewing the request"},
		{method: "item/agentMessage/delta", activity: "Drafting the response"},
		{method: "item/commandExecution/started", activity: "Checking available actions"},
		{method: "turn/completed", activity: "Finishing the response"},
	}

	for _, test := range tests {
		phase := builderActivityForCodexMethod(test.method)
		if phase.Activity != test.activity {
			t.Fatalf("method %q mapped to %q, want %q", test.method, phase.Activity, test.activity)
		}
		if phase.Detail == "" {
			t.Fatalf("method %q returned an empty safe detail", test.method)
		}
	}
}

func TestBuilderActivityForCodexMethodDoesNotEchoProtocolMethod(t *testing.T) {
	method := "item/reasoning/private-secret-token"
	phase := builderActivityForCodexMethod(method)
	if phase.Activity == method || phase.Detail == method {
		t.Fatalf("activity summary echoed the raw protocol method")
	}
}
