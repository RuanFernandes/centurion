package orchestrator

import "strings"

type agentActivityPhase struct {
	Activity string
	Detail   string
}

// agentActivityForMethod creates a safe operational summary for the run
// timeline. Notification payloads are intentionally not part of this model,
// so private reasoning text and tool arguments are never persisted as events.
func agentActivityForMethod(method string) agentActivityPhase {
	method = strings.ToLower(strings.TrimSpace(method))
	switch {
	case strings.Contains(method, "reasoning"):
		return agentActivityPhase{Activity: "Reviewing the request", Detail: "Checking assumptions and constraints."}
	case strings.Contains(method, "command") || strings.Contains(method, "tool") || strings.Contains(method, "mcp"):
		return agentActivityPhase{Activity: "Running a tool step", Detail: "Processing an approved or allowlisted action."}
	case strings.Contains(method, "file") || strings.Contains(method, "patch"):
		return agentActivityPhase{Activity: "Reviewing project files", Detail: "Keeping changes inside the configured workspace."}
	case strings.Contains(method, "agentmessage") || strings.Contains(method, "message"):
		return agentActivityPhase{Activity: "Drafting the result", Detail: "Preparing the agent response."}
	case strings.Contains(method, "turn/completed"):
		return agentActivityPhase{Activity: "Finishing the turn", Detail: "The agent is returning its result."}
	case strings.Contains(method, "turn"):
		return agentActivityPhase{Activity: "Processing the turn", Detail: "The Codex turn is active."}
	default:
		return agentActivityPhase{Activity: "Working on the step", Detail: "The agent is processing its instructions."}
	}
}
