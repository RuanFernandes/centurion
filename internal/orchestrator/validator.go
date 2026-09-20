package orchestrator

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/RuanFernandes/centurion/internal/model"
)

var allowedNodeTypes = map[string]struct{}{
	"agent": {}, "condition": {}, "parallel": {}, "join": {}, "loop": {}, "approval": {}, "tool": {}, "artifact": {},
}

const (
	maxWorkflowDurationSeconds = 7 * 24 * 60 * 60
	maxWorkflowParallel        = 16
	maxWorkflowTurns           = 200
	maxNodeTimeoutSeconds      = 24 * 60 * 60
	maxRetryAttempts           = 10
	maxRetryBackoffSeconds     = 300
	maxLoopIterations          = 1000
)

var allowedErrorPolicies = map[string]struct{}{
	"stop":             {},
	"continue":         {},
	"fallback":         {},
	"request_approval": {},
}

func ValidateWorkflow(workflow model.WorkflowDefinition) model.WorkflowValidation {
	validation := model.WorkflowValidation{Valid: true}
	if strings.TrimSpace(workflow.ID) == "" {
		addError(&validation, "workflow.id.required", "id", "workflow id is required")
	}
	if strings.TrimSpace(workflow.Name) == "" {
		addError(&validation, "workflow.name.required", "name", "workflow name is required")
	}
	if workflow.Version <= 0 {
		addError(&validation, "workflow.version.invalid", "version", "version must be greater than zero")
	}
	if workflow.GlobalLimits.MaxDurationSeconds <= 0 {
		addError(&validation, "workflow.limit.duration", "globalLimits.maxDurationSeconds", "maxDurationSeconds must be greater than zero")
	} else if workflow.GlobalLimits.MaxDurationSeconds > maxWorkflowDurationSeconds {
		addError(&validation, "workflow.limit.duration.max", "globalLimits.maxDurationSeconds", fmt.Sprintf("maxDurationSeconds cannot exceed %d", maxWorkflowDurationSeconds))
	}
	if workflow.GlobalLimits.MaxParallel <= 0 {
		addError(&validation, "workflow.limit.parallel", "globalLimits.maxParallel", "maxParallel must be greater than zero")
	} else if workflow.GlobalLimits.MaxParallel > maxWorkflowParallel {
		addError(&validation, "workflow.limit.parallel.max", "globalLimits.maxParallel", fmt.Sprintf("maxParallel cannot exceed %d", maxWorkflowParallel))
	}
	if workflow.GlobalLimits.MaxTurns <= 0 {
		addError(&validation, "workflow.limit.turns", "globalLimits.maxTurns", "maxTurns must be greater than zero")
	} else if workflow.GlobalLimits.MaxTurns > maxWorkflowTurns {
		addError(&validation, "workflow.limit.turns.max", "globalLimits.maxTurns", fmt.Sprintf("maxTurns cannot exceed %d", maxWorkflowTurns))
	}
	if workflow.GlobalLimits.MaxPromptTokens < 0 || workflow.GlobalLimits.MaxPromptTokens > 1_000_000 {
		addError(&validation, "workflow.limit.prompt_tokens", "globalLimits.maxPromptTokens", "maxPromptTokens must be between zero and one million")
	} else if workflow.GlobalLimits.MaxPromptTokens == 0 {
		addWarning(&validation, "workflow.limit.prompt_tokens.defaulted", "globalLimits.maxPromptTokens", "maxPromptTokens is zero and will default to 12000 estimated tokens")
	}
	policy := strings.ToLower(strings.TrimSpace(workflow.ErrorPolicy))
	if policy == "" {
		addWarning(&validation, "workflow.error_policy.defaulted", "errorPolicy", "errorPolicy is empty and will default to stop")
	} else if _, ok := allowedErrorPolicies[policy]; !ok {
		addError(&validation, "workflow.error_policy.invalid", "errorPolicy", "errorPolicy must be stop, continue, fallback, or request_approval")
	}

	nodes := make(map[string]model.WorkflowNode, len(workflow.Nodes))
	for index, node := range workflow.Nodes {
		path := fmt.Sprintf("nodes[%d]", index)
		if strings.TrimSpace(node.ID) == "" {
			addError(&validation, "node.id.required", path+".id", "node id is required")
			continue
		}
		if _, exists := nodes[node.ID]; exists {
			addError(&validation, "node.id.duplicate", path+".id", "node id must be unique")
			continue
		}
		nodes[node.ID] = node
		if _, ok := allowedNodeTypes[node.Type]; !ok {
			addError(&validation, "node.type.invalid", path+".type", "unsupported node type: "+node.Type)
		}
		if node.TimeoutSeconds < 0 {
			addError(&validation, "node.timeout.invalid", path+".timeoutSeconds", "timeoutSeconds cannot be negative")
		} else if node.TimeoutSeconds > maxNodeTimeoutSeconds {
			addError(&validation, "node.timeout.max", path+".timeoutSeconds", fmt.Sprintf("timeoutSeconds cannot exceed %d", maxNodeTimeoutSeconds))
		}
		if node.Retry.MaxAttempts < 0 || node.Retry.BackoffSeconds < 0 {
			addError(&validation, "node.retry.invalid", path+".retry", "retry limits cannot be negative")
		}
		if node.Retry.MaxAttempts > maxRetryAttempts {
			addError(&validation, "node.retry.attempts.max", path+".retry.maxAttempts", fmt.Sprintf("maxAttempts cannot exceed %d", maxRetryAttempts))
		}
		if node.Retry.BackoffSeconds > maxRetryBackoffSeconds {
			addError(&validation, "node.retry.backoff.max", path+".retry.backoffSeconds", fmt.Sprintf("backoffSeconds cannot exceed %d", maxRetryBackoffSeconds))
		}
		if node.Retry.MaxAttempts == 0 {
			addWarning(&validation, "node.retry.defaulted", path+".retry.maxAttempts", "maxAttempts is zero and will default to one attempt")
		}
		if node.Type == "loop" && node.MaxIterations <= 0 {
			addError(&validation, "loop.limit.required", path+".maxIterations", "loop nodes require maxIterations greater than zero")
		} else if node.Type == "loop" && node.MaxIterations > maxLoopIterations {
			addError(&validation, "loop.limit.max", path+".maxIterations", fmt.Sprintf("maxIterations cannot exceed %d", maxLoopIterations))
		}
		if policy == "fallback" {
			fallback, ok := node.Config["fallbackNodeID"].(string)
			if !ok || strings.TrimSpace(fallback) == "" {
				addWarning(&validation, "workflow.fallback.target.missing", path+".config.fallbackNodeID", "this node has no fallbackNodeID and will fail the run if it errors")
			} else if fallback == node.ID {
				addError(&validation, "workflow.fallback.target.self", path+".config.fallbackNodeID", "fallbackNodeID cannot reference the same node")
			}
		}
		if node.Type == "agent" && strings.TrimSpace(node.AgentID) == "" {
			addError(&validation, "agent.id.required", path+".agentID", "agent nodes require an agentID")
		}
		if node.Type == "tool" && strings.TrimSpace(node.ToolName) == "" {
			addError(&validation, "tool.name.required", path+".toolName", "tool nodes require a toolName")
		}
		if node.Condition != "" {
			if err := ValidateCondition(node.Condition); err != nil {
				addError(&validation, "condition.invalid", path+".condition", err.Error())
			}
		}
		if exhausted, ok := node.Config["onExhausted"].(string); ok && node.Type == "loop" && exhausted == node.ID {
			addError(&validation, "loop.exhausted.self", path+".config.onExhausted", "loop exhaustion target cannot be the loop itself")
		}
	}
	if _, ok := nodes[workflow.EntryNodeID]; !ok {
		addError(&validation, "entry.invalid", "entryNodeID", "entryNodeID must reference an existing node")
	}
	if len(nodes) > 1 && len(workflow.Edges) == 0 {
		addError(&validation, "workflow.graph.disconnected", "edges", "workflows with multiple nodes require at least one connection")
	}

	adjacency := make(map[string][]string, len(nodes))
	edgeIDs := make(map[string]struct{}, len(workflow.Edges))
	for index, edge := range workflow.Edges {
		path := fmt.Sprintf("edges[%d]", index)
		if strings.TrimSpace(edge.ID) == "" {
			addError(&validation, "edge.id.required", path+".id", "edge id is required")
		} else if _, exists := edgeIDs[edge.ID]; exists {
			addError(&validation, "edge.id.duplicate", path+".id", "edge id must be unique")
		} else {
			edgeIDs[edge.ID] = struct{}{}
		}
		if _, ok := nodes[edge.From]; !ok {
			addError(&validation, "edge.from.invalid", path+".from", "edge source does not exist")
		}
		if _, ok := nodes[edge.To]; !ok {
			addError(&validation, "edge.to.invalid", path+".to", "edge destination does not exist")
		}
		if edge.From == edge.To && nodeType(nodes, edge.From) != "loop" {
			addError(&validation, "edge.self.invalid", path, "self edges are only valid for bounded loop nodes")
		}
		if edge.Condition != "" {
			if err := ValidateCondition(edge.Condition); err != nil {
				addError(&validation, "edge.condition.invalid", path+".condition", err.Error())
			}
		}
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	for _, node := range workflow.Nodes {
		if node.Type == "parallel" && len(adjacency[node.ID]) < 2 {
			addError(&validation, "parallel.single.branch", "nodes["+node.ID+"]", "parallel nodes require at least two outgoing branches")
		}
		if node.Type == "condition" && len(adjacency[node.ID]) < 2 {
			addWarning(&validation, "condition.single.branch", "nodes["+node.ID+"]", "condition nodes normally need at least two outgoing edges")
		}
		if target, ok := node.Config["onExhausted"].(string); ok && node.Type == "loop" {
			if _, exists := nodes[target]; !exists {
				addError(&validation, "loop.exhausted.invalid", "nodes["+node.ID+"] .config.onExhausted", "loop exhaustion target does not exist")
			}
		}
		if policy == "fallback" {
			if target, ok := node.Config["fallbackNodeID"].(string); ok {
				if _, exists := nodes[target]; !exists {
					addError(&validation, "workflow.fallback.target.invalid", "nodes["+node.ID+"] .config.fallbackNodeID", "fallback target does not exist")
				}
			}
		}
	}
	validateParallelNesting(nodes, adjacency, &validation)

	if workflow.EntryNodeID != "" {
		reachable := make(map[string]bool)
		visitReachable(workflow.EntryNodeID, adjacency, reachable)
		for id := range nodes {
			if !reachable[id] {
				addError(&validation, "node.unreachable", "nodes["+id+"]", "node is not reachable from entryNodeID")
			}
		}
	}
	state := make(map[string]int, len(nodes))
	stack := make([]string, 0, len(nodes))
	for id := range nodes {
		if state[id] == 0 {
			detectCycles(id, adjacency, nodes, state, &stack, &validation)
		}
	}
	validation.Valid = len(validation.Errors) == 0
	return validation
}

func ValidateCondition(expression string) error {
	expression = strings.TrimSpace(expression)
	if expression == "" || expression == "always" {
		return nil
	}
	parts := strings.SplitN(expression, ":", 3)
	if len(parts) < 2 {
		if strings.Contains(expression, "==") || strings.Contains(expression, "!=") {
			return nil
		}
		return fmt.Errorf("condition must use a declarative operator such as truthy:path or equals:path:value")
	}
	switch parts[0] {
	case "truthy", "exists", "empty":
		if strings.TrimSpace(parts[1]) == "" {
			return errorsNew("condition path is required")
		}
	case "equals", "notEquals", "contains", "lessThan", "greaterThan":
		if strings.TrimSpace(parts[1]) == "" || len(parts) < 3 {
			return errorsNew("comparison conditions require a path and a value")
		}
	default:
		return fmt.Errorf("unsupported declarative condition operator: %s", parts[0])
	}
	return nil
}

func EvaluateCondition(expression string, scope map[string]any) (bool, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" || expression == "always" {
		return true, nil
	}
	if strings.Contains(expression, "==") || strings.Contains(expression, "!=") {
		operator := "=="
		if strings.Contains(expression, "!=") {
			operator = "!="
		}
		parts := strings.SplitN(expression, operator, 2)
		if len(parts) != 2 {
			return false, errorsNew("invalid comparison condition")
		}
		left, ok := valueAtPath(scope, strings.TrimSpace(parts[0]))
		if !ok {
			left = nil
		}
		right := parseLiteral(strings.TrimSpace(parts[1]))
		equal := jsonEqual(left, right)
		if operator == "!=" {
			return !equal, nil
		}
		return equal, nil
	}
	parts := strings.SplitN(expression, ":", 3)
	if err := ValidateCondition(expression); err != nil {
		return false, err
	}
	value, exists := valueAtPath(scope, parts[1])
	switch parts[0] {
	case "truthy":
		return truthy(value), nil
	case "exists":
		return exists, nil
	case "empty":
		return !exists || value == nil || fmt.Sprint(value) == "", nil
	case "equals":
		return jsonEqual(value, parseLiteral(parts[2])), nil
	case "notEquals":
		return !jsonEqual(value, parseLiteral(parts[2])), nil
	case "contains":
		needle := fmt.Sprint(parseLiteral(parts[2]))
		if text, ok := value.(string); ok {
			return strings.Contains(text, needle), nil
		}
		if list, ok := value.([]any); ok {
			for _, item := range list {
				if jsonEqual(item, parseLiteral(parts[2])) {
					return true, nil
				}
			}
		}
		return false, nil
	case "lessThan", "greaterThan":
		left, leftOK := numberValue(value)
		right, rightOK := numberValue(parseLiteral(parts[2]))
		if !leftOK || !rightOK {
			return false, nil
		}
		if parts[0] == "lessThan" {
			return left < right, nil
		}
		return left > right, nil
	default:
		return false, fmt.Errorf("unsupported condition operator: %s", parts[0])
	}
}

func valueAtPath(scope map[string]any, path string) (any, bool) {
	path = strings.TrimSpace(strings.TrimPrefix(path, "$"))
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return scope, true
	}
	var current any = scope
	for _, part := range strings.Split(path, ".") {
		switch object := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = object[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(object) {
				return nil, false
			}
			current = object[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func parseLiteral(value string) any {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	if value == "true" {
		return true
	}
	if value == "false" {
		return false
	}
	if value == "null" {
		return nil
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return number
	}
	return value
}

func jsonEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != "" && typed != "false" && typed != "0"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return true
	}
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func detectCycles(id string, adjacency map[string][]string, nodes map[string]model.WorkflowNode, state map[string]int, stack *[]string, validation *model.WorkflowValidation) {
	state[id] = 1
	*stack = append(*stack, id)
	for _, next := range adjacency[id] {
		if state[next] == 0 {
			detectCycles(next, adjacency, nodes, state, stack, validation)
			continue
		}
		if state[next] != 1 {
			continue
		}
		cycleHasLoop := false
		for index := len(*stack) - 1; index >= 0; index-- {
			if nodes[(*stack)[index]].Type == "loop" {
				cycleHasLoop = true
			}
			if (*stack)[index] == next {
				break
			}
		}
		if !cycleHasLoop {
			addError(validation, "graph.cycle.unbounded", "edges", "cycles must pass through a bounded loop node")
		}
	}
	*stack = (*stack)[:len(*stack)-1]
	state[id] = 2
}

func visitReachable(id string, adjacency map[string][]string, visited map[string]bool) {
	if visited[id] {
		return
	}
	visited[id] = true
	for _, next := range adjacency[id] {
		visitReachable(next, adjacency, visited)
	}
}

// The first executor keeps one semaphore per top-level parallel node. A
// nested parallel would require a second scheduling scope and could otherwise
// deadlock or exceed the configured parallelism. Reject it during validation
// so a saved workflow cannot fail only after execution has started.
func validateParallelNesting(nodes map[string]model.WorkflowNode, adjacency map[string][]string, validation *model.WorkflowValidation) {
	for rootID, root := range nodes {
		if root.Type != "parallel" {
			continue
		}
		visited := make(map[string]struct{})
		for _, branchID := range adjacency[rootID] {
			if containsNestedParallel(branchID, nodes, adjacency, visited) {
				addError(validation, "parallel.nested.unsupported", "nodes["+rootID+"]", "nested parallel nodes are not supported; connect branches to a join before starting another parallel block")
				break
			}
		}
	}
}

func containsNestedParallel(id string, nodes map[string]model.WorkflowNode, adjacency map[string][]string, visited map[string]struct{}) bool {
	if id == "" {
		return false
	}
	if _, seen := visited[id]; seen {
		return false
	}
	visited[id] = struct{}{}
	node, exists := nodes[id]
	if !exists {
		return false
	}
	if node.Type == "join" {
		return false
	}
	if node.Type == "parallel" {
		return true
	}
	for _, next := range adjacency[id] {
		if containsNestedParallel(next, nodes, adjacency, visited) {
			return true
		}
	}
	return false
}

func nodeType(nodes map[string]model.WorkflowNode, id string) string { return nodes[id].Type }

func addError(validation *model.WorkflowValidation, code, path, message string) {
	validation.Errors = append(validation.Errors, model.ValidationIssue{Code: code, Path: path, Message: message})
}

func addWarning(validation *model.WorkflowValidation, code, path, message string) {
	validation.Warnings = append(validation.Warnings, model.ValidationIssue{Code: code, Path: path, Message: message})
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }
