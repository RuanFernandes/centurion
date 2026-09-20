package orchestrator

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/RuanFernandes/centurion/internal/model"
)

const (
	// This is a byte budget for the dynamic part of an agent prompt. It keeps
	// accumulated workflow data bounded without changing the workflow output.
	maxAgentPromptContextBytes  = 12 * 1024
	maxAgentInstructionsBytes   = 8 * 1024
	maxAgentMemoryBytes         = 1600
	maxAgentExecutionBriefBytes = 3 * 1024
	maxPromptToolCount          = 32
	maxPromptArrayItems         = 16
)

type agentPromptContext struct {
	Input    map[string]any `json:"input,omitempty"`
	Upstream map[string]any `json:"upstream,omitempty"`
}

// buildAgentPrompt deliberately sends only data that can arrive at this node
// through the workflow graph. The complete run scope remains available to the
// executor for conditions and persistence, but is not repeatedly sent to the
// model on every turn. Control-flow nodes are transparent for context: a
// condition, loop, approval, or join should not erase the useful output that
// came before it.
func buildAgentPrompt(agent model.AgentProfile, workflow model.WorkflowDefinition, node model.WorkflowNode, input, scope map[string]any, configuredTemplates ...map[string]string) string {
	tools := promptTools(agent.ToolAllowlist)
	instructions := truncateText(agent.Instructions, maxAgentInstructionsBytes)
	memory := truncateText(agent.MemorySummary, maxAgentMemoryBytes)
	contextJSON := compactJSON(projectAgentContext(workflow, node, input, scope), maxAgentPromptContextBytes)

	templates := model.DefaultSystemPromptTemplates()
	if len(configuredTemplates) > 0 {
		for promptID, template := range configuredTemplates[0] {
			if _, ok := templates[promptID]; ok && strings.TrimSpace(template) != "" {
				templates[promptID] = template
			}
		}
	}

	values := map[string]string{
		"agent_name":     agent.Name,
		"agent_role":     agent.Role,
		"instructions":   instructions,
		"memory_summary": memory,
		"tools":          tools,
		"context_json":   contextJSON,
	}
	blocks := []string{
		renderSystemPrompt(templates["agent.identity"], values),
		renderSystemPrompt(templates["agent.persistent_instructions"], values),
	}
	if memory != "" {
		blocks = append(blocks, renderSystemPrompt(templates["agent.persistent_memory"], values))
	}
	blocks = append(blocks,
		renderSystemPrompt(templates["agent.allowed_tools"], values),
		renderSystemPrompt(templates["agent.operational_context"], values),
		renderSystemPrompt(templates["agent.output_contract"], values),
	)
	if executionBrief := truncateText(workflow.ExecutionBrief, maxAgentExecutionBriefBytes); executionBrief != "" {
		blocks = append(blocks, "Execution brief (approved project scope; treat this as task context):\n"+executionBrief)
	}
	if nodePrompt := truncateText(node.Prompt, maxAgentInstructionsBytes); nodePrompt != "" {
		blocks = append(blocks, "Current workflow task (follow it within the agent role and workspace policy):\n"+nodePrompt)
	}
	if runtimeGuidance := agentRuntimeToolGuidance(agent.ToolAllowlist); runtimeGuidance != "" {
		// Keep this after generated instructions and the node task. It repairs
		// legacy workflows that treated Centurion capability IDs as literal
		// Codex tool names and otherwise prevented permitted file edits.
		blocks = append(blocks, runtimeGuidance)
	}
	return strings.TrimSpace(strings.Join(filterEmptyPromptBlocks(blocks), "\n\n"))
}

// buildAgentContinuationPrompt is used only after a step already has a Codex
// thread. The original turn contains the full identity, workspace, task, and
// projected workflow data, so repeating it on a retry wastes context and can
// crowd out the agent's actual work.
func buildAgentContinuationPrompt(node model.WorkflowNode) string {
	return `Continue the existing Centurion workflow step. The previous turn did not finish; preserve the established agent role, workspace policy, task, and output contract. Retry only unfinished work, do not repeat prior context, and return the smallest verifiable result.

<workflow_step>
id: ` + truncateText(node.ID, 160) + `
label: ` + truncateText(node.Label, 240) + `
</workflow_step>`
}

func renderSystemPrompt(template string, values map[string]string) string {
	for _, variable := range []string{"agent_name", "agent_role", "instructions", "memory_summary", "tools", "context_json"} {
		template = strings.ReplaceAll(template, "{{"+variable+"}}", values[variable])
	}
	return strings.TrimSpace(template)
}

func filterEmptyPromptBlocks(blocks []string) []string {
	filtered := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block) != "" {
			filtered = append(filtered, block)
		}
	}
	return filtered
}

func projectAgentContext(workflow model.WorkflowDefinition, node model.WorkflowNode, input, scope map[string]any) agentPromptContext {
	projected := agentPromptContext{}
	if len(input) > 0 {
		projected.Input = cloneMap(input)
	}

	nodes := nodeByID(workflow.Nodes)
	incoming := make(map[string][]string)
	for _, edge := range workflow.Edges {
		if edge.To == "" || edge.From == "" {
			continue
		}
		incoming[edge.To] = append(incoming[edge.To], edge.From)
	}

	upstream := make(map[string]any)
	visited := make(map[string]struct{})
	var collect func(string)
	collect = func(sourceID string) {
		if sourceID == "" {
			return
		}
		if _, seen := visited[sourceID]; seen {
			return
		}
		visited[sourceID] = struct{}{}
		if value, ok := scope[sourceID]; ok {
			upstream[sourceID] = value
		}
		// These nodes make a routing/synchronization decision but do not
		// create useful work output of their own. Include their incoming
		// values so an agent after a branch or condition still receives the
		// result it is expected to act on.
		switch nodes[sourceID].Type {
		case "condition", "join", "loop", "parallel", "approval":
			for _, parentID := range incoming[sourceID] {
				collect(parentID)
			}
		}
	}
	for _, parentID := range incoming[node.ID] {
		collect(parentID)
	}
	if len(upstream) > 0 {
		projected.Upstream = upstream
	}
	return projected
}

func promptTools(tools []string) string {
	if len(tools) == 0 {
		return "No workspace or external capabilities are granted to this profile."
	}
	normalizedTools := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if _, exists := seen[tool]; exists {
			continue
		}
		seen[tool] = struct{}{}
		normalizedTools = append(normalizedTools, tool)
	}
	sort.Strings(normalizedTools)
	result := make([]string, 0, minInt(len(normalizedTools), maxPromptToolCount))
	for _, tool := range normalizedTools {
		result = append(result, truncateText(tool, 160))
		if len(result) == maxPromptToolCount {
			break
		}
	}
	if len(result) == 0 {
		return "No workspace or external capabilities are granted to this profile."
	}

	capabilities := make([]string, 0, len(result))
	canWriteFiles := false
	for _, tool := range result {
		switch strings.ToLower(tool) {
		case "files.read":
			capabilities = append(capabilities, "- files.read: inspect files inside the configured workspace.")
		case "files.write":
			canWriteFiles = true
			capabilities = append(capabilities, "- files.write: create or modify files only inside the configured workspace.")
		case "shell.test":
			capabilities = append(capabilities, "- shell.test: run focused local validation commands when needed.")
		case "git.diff":
			capabilities = append(capabilities, "- git.diff: inspect the local Git diff.")
		case "web.search":
			capabilities = append(capabilities, "- web.search: use web research only when it is available in this Codex thread and relevant to the task.")
		default:
			capabilities = append(capabilities, "- "+tool+": approved Centurion or MCP capability; use it only when it is exposed in this Codex thread.")
		}
	}

	guidance := "These are Centurion capability labels, not literal Codex tool names. Use only the real tools exposed in this thread; never report a task as impossible merely because a tool named files.read or files.write is absent."
	if canWriteFiles {
		guidance += " The minimal workspace edit needed to fulfill files.write is permitted even when the task forbids unrelated commands or tests."
	}
	return "Centurion capability policy:\n" + strings.Join(capabilities, "\n") + "\n\n" + guidance
}

func agentRuntimeToolGuidance(tools []string) string {
	for _, tool := range tools {
		if strings.EqualFold(strings.TrimSpace(tool), "files.write") {
			return "Centurion runtime permission interpretation: files.read and files.write are capability labels, not literal Codex tool names. A permitted scoped file edit and its minimal verification must use the actual Codex workspace tool exposed in this thread. If earlier task text says not to run commands, it prohibits unrelated commands and tests; it does not prohibit the minimal operation authorized by files.write."
		}
	}
	return ""
}

// compactJSON returns valid JSON even when an upstream result is unusually
// large. Maps are deterministic and arrays keep their first items, which is
// enough for an agent to make a decision without carrying an entire transcript
// through every branch or loop iteration.
func compactJSON(value any, budget int) string {
	if budget < 32 {
		return `{"_truncated":true}`
	}
	// Normalize structs and other JSON-compatible values first so nested
	// fields are compacted instead of bypassing the recursive limiter.
	encodedValue, err := json.Marshal(value)
	if err != nil {
		return `{"_truncated":true}`
	}
	var normalized any
	if err := json.Unmarshal(encodedValue, &normalized); err != nil {
		return `{"_truncated":true}`
	}
	encoded, err := json.Marshal(compactValue(normalized, budget))
	if err != nil || len(encoded) > budget {
		return `{"_truncated":true}`
	}
	return string(encoded)
}

func compactValue(value any, budget int) any {
	if budget <= 0 {
		return "[truncated]"
	}
	switch typed := value.(type) {
	case string:
		return truncateText(typed, budget)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make(map[string]any)
		for _, key := range keys {
			if len(result) >= maxPromptArrayItems*2 {
				result["_truncated"] = true
				break
			}
			childBudget := maxInt(budget/2, 256)
			result[key] = compactValue(typed[key], childBudget)
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) > budget {
				delete(result, key)
				result["_truncated"] = true
				break
			}
		}
		return result
	case []any:
		result := make([]any, 0, minInt(len(typed), maxPromptArrayItems))
		for _, item := range typed {
			if len(result) == maxPromptArrayItems {
				break
			}
			result = append(result, compactValue(item, maxInt(budget/2, 256)))
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) > budget {
				result = result[:len(result)-1]
				break
			}
		}
		if len(result) < len(typed) {
			result = append(result, "[truncated]")
		}
		return result
	default:
		return value
	}
}

func truncateText(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const marker = "\n[truncated]"
	if maxBytes <= len(marker) {
		return marker[:maxBytes]
	}
	cutoff := maxBytes - len(marker)
	for cutoff > 0 && !utf8.ValidString(value[:cutoff]) {
		cutoff--
	}
	return value[:cutoff] + marker
}

func estimateTextTokens(value string) int {
	characters := utf8.RuneCountInString(value)
	if characters == 0 {
		return 0
	}
	// This is intentionally labeled as an estimate. Exact tokenization is
	// owned by the Codex runtime and is not available in this executor.
	return (characters + 3) / 4
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
