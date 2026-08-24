package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/orchestrator"
	"github.com/RuanFernandes/centurion/internal/security"
)

const (
	MaxRequestBytes  = 8 * 1024
	MaxOutputBytes   = 128 * 1024
	MaxAgents        = 24
	MaxWorkflowNodes = 64
	MaxWorkflowEdges = 128
)

type Catalog struct {
	Models     []model.ModelInfo
	MCPServers []model.MCPServer
}

var allowedNodeTypes = map[string]struct{}{
	"agent": {}, "condition": {}, "parallel": {}, "join": {}, "loop": {}, "approval": {}, "tool": {}, "artifact": {},
}

var builtInPermissions = map[string]struct{}{
	"files.read":  {},
	"files.write": {},
	"shell.test":  {},
	"git.diff":    {},
	"web.search":  {},
}

func OutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schemaVersion", "summary", "agents", "workflow"},
		"properties": map[string]any{
			"schemaVersion": map[string]any{"type": "integer"},
			"summary":       map[string]any{"type": "string"},
			"notes": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"agents": map[string]any{
				"type":     "array",
				"maxItems": MaxAgents,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"temporaryID", "name", "role", "instructions", "approvalProfile"},
					"properties": map[string]any{
						"temporaryID":        map[string]any{"type": "string"},
						"name":               map[string]any{"type": "string"},
						"role":               map[string]any{"type": "string"},
						"instructions":       map[string]any{"type": "string"},
						"modelID":            map[string]any{"type": "string"},
						"reasoningEffort":    map[string]any{"type": "string"},
						"workspaceRoots":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"toolAllowlist":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"approvalProfile":    map[string]any{"type": "string"},
						"roomID":             map[string]any{"type": "string"},
						"avatarID":           map[string]any{"type": "string"},
						"maxDurationSeconds": map[string]any{"type": "integer"},
						"maxTurns":           map[string]any{"type": "integer"},
						"maxAttempts":        map[string]any{"type": "integer"},
					},
				},
			},
			// Workflow is intentionally open at the schema boundary. Go applies
			// the versioned workflow validator before anything is persisted.
			"workflow": map[string]any{
				"type": []string{"object", "null"},
			},
		},
	}
}

func ParseProposal(output string) (model.BuilderProposal, error) {
	if len([]byte(output)) > MaxOutputBytes {
		return model.BuilderProposal{}, errors.New("Codex builder response is too large")
	}
	cleaned := strings.TrimSpace(output)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```JSON")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), "```")
	cleaned = strings.TrimSpace(cleaned)
	start := strings.IndexByte(cleaned, '{')
	end := strings.LastIndexByte(cleaned, '}')
	if start < 0 || end <= start {
		return model.BuilderProposal{}, errors.New("Codex builder did not return a JSON proposal")
	}
	var proposal model.BuilderProposal
	if err := json.Unmarshal([]byte(cleaned[start:end+1]), &proposal); err != nil {
		return model.BuilderProposal{}, fmt.Errorf("decode Codex builder proposal: %w", err)
	}
	return proposal, nil
}

func NormalizeProposal(proposal model.BuilderProposal, project model.Project, catalog Catalog) (model.BuilderProposal, error) {
	proposal.SchemaVersion = 1
	proposal.Summary = boundedText(proposal.Summary, "Proposed Centurion workspace", 1200)
	if proposal.Summary == "" {
		return model.BuilderProposal{}, errors.New("builder proposal summary is required")
	}
	if len(proposal.Agents) > MaxAgents {
		return model.BuilderProposal{}, fmt.Errorf("builder proposal contains more than %d agents", MaxAgents)
	}
	proposal.Notes = normalizeNotes(proposal.Notes)

	allowedModels := make(map[string]model.ModelInfo, len(catalog.Models)*2)
	for _, entry := range catalog.Models {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		allowedModels[strings.ToLower(entry.ID)] = entry
		allowedModels[strings.ToLower(entry.DisplayName)] = entry
	}
	allowedTools := catalogTools(catalog.MCPServers)
	agentIDs := make(map[string]struct{}, len(proposal.Agents))
	firstAgentID := ""
	for index := range proposal.Agents {
		agent := &proposal.Agents[index]
		agent.TemporaryID = normalizeID(agent.TemporaryID, fmt.Sprintf("agent-%d", index+1))
		if _, exists := agentIDs[agent.TemporaryID]; exists {
			return model.BuilderProposal{}, fmt.Errorf("duplicate agent temporaryID %q", agent.TemporaryID)
		}
		agentIDs[agent.TemporaryID] = struct{}{}
		if firstAgentID == "" {
			firstAgentID = agent.TemporaryID
		}
		agent.Name = boundedText(agent.Name, fmt.Sprintf("Agent %d", index+1), 120)
		agent.Role = boundedText(agent.Role, "Operations specialist", 180)
		agent.Instructions = boundedText(agent.Instructions, "Work only inside the configured workspace and report a concise, verifiable result.", 16*1024)
		agent.ApprovalProfile = normalizeApprovalProfile(agent.ApprovalProfile)
		agent.RoomID = normalizeRoom(agent.RoomID)
		agent.AvatarID = boundedText(agent.AvatarID, "operator", 64)
		agent.MaxDurationSeconds = clampInt(agent.MaxDurationSeconds, 1800, 30, 86400)
		agent.MaxTurns = clampInt(agent.MaxTurns, 12, 1, 50)
		agent.MaxAttempts = clampInt(agent.MaxAttempts, 2, 1, 5)
		requestedModelID := strings.TrimSpace(agent.ModelID)
		agent.ModelID = normalizeModelID(requestedModelID, allowedModels)
		if agent.ModelID == "" && requestedModelID != "" {
			proposal.Notes = addNote(proposal.Notes, "One or more requested agent models were not available; those agents will inherit the builder model.")
		}
		agent.ReasoningEffort = normalizeEffort(agent.ReasoningEffort, allowedModels[strings.ToLower(agent.ModelID)])
		agent.WorkspaceRoots = normalizeWorkspaceRoots(agent.WorkspaceRoots, project.Folders, &proposal.Notes)
		agent.ToolAllowlist = normalizeTools(agent.ToolAllowlist, allowedTools, &proposal.Notes)
	}

	if proposal.Workflow != nil {
		if err := normalizeWorkflow(proposal.Workflow, agentIDs, firstAgentID, project.Folders, allowedTools, &proposal.Notes); err != nil {
			return model.BuilderProposal{}, err
		}
	}
	return proposal, nil
}

// ApplyAgentDefaults makes the model used to draft the proposal the default
// for every generated agent. An explicit, catalog-valid model returned for an
// individual agent is preserved.
func ApplyAgentDefaults(proposal model.BuilderProposal, modelID, reasoningEffort string) model.BuilderProposal {
	modelID = strings.TrimSpace(modelID)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	for index := range proposal.Agents {
		if strings.TrimSpace(proposal.Agents[index].ModelID) == "" {
			proposal.Agents[index].ModelID = modelID
		}
		if strings.TrimSpace(proposal.Agents[index].ReasoningEffort) == "" {
			proposal.Agents[index].ReasoningEffort = reasoningEffort
		}
	}
	return proposal
}

func normalizeWorkflow(workflow *model.WorkflowDefinition, agentIDs map[string]struct{}, firstAgentID string, projectRoots, allowedTools []string, notes *[]string) error {
	if workflow == nil {
		return nil
	}
	if len(workflow.Nodes) == 0 {
		return errors.New("builder workflow must contain at least one node")
	}
	if len(workflow.Nodes) > MaxWorkflowNodes {
		return fmt.Errorf("builder workflow contains more than %d nodes", MaxWorkflowNodes)
	}
	if len(workflow.Edges) > MaxWorkflowEdges {
		return fmt.Errorf("builder workflow contains more than %d edges", MaxWorkflowEdges)
	}
	workflow.ID = normalizeID(workflow.ID, "workflow-draft")
	workflow.Name = boundedText(workflow.Name, "Agent workflow", 160)
	workflow.Description = boundedText(workflow.Description, "", 1200)
	workflow.Version = 1
	workflow.GlobalLimits.MaxDurationSeconds = clampInt(workflow.GlobalLimits.MaxDurationSeconds, 3600, 30, 7*24*3600)
	workflow.GlobalLimits.MaxParallel = clampInt(workflow.GlobalLimits.MaxParallel, 2, 1, 16)
	workflow.GlobalLimits.MaxTurns = clampInt(workflow.GlobalLimits.MaxTurns, 24, 1, 200)
	if strings.TrimSpace(workflow.ErrorPolicy) == "" {
		workflow.ErrorPolicy = "stop"
	}

	nodeIDs := make(map[string]struct{}, len(workflow.Nodes))
	for index := range workflow.Nodes {
		node := &workflow.Nodes[index]
		node.ID = normalizeID(node.ID, fmt.Sprintf("node-%d", index+1))
		if _, exists := nodeIDs[node.ID]; exists {
			return fmt.Errorf("duplicate workflow node id %q", node.ID)
		}
		nodeIDs[node.ID] = struct{}{}
		node.Type = strings.ToLower(strings.TrimSpace(node.Type))
		if _, ok := allowedNodeTypes[node.Type]; !ok {
			return fmt.Errorf("unsupported builder node type %q", node.Type)
		}
		node.Label = boundedText(node.Label, strings.Title(node.Type), 160)
		node.TimeoutSeconds = clampInt(node.TimeoutSeconds, 0, 0, 86400)
		node.Retry.MaxAttempts = clampInt(node.Retry.MaxAttempts, 1, 1, 5)
		node.Retry.BackoffSeconds = clampInt(node.Retry.BackoffSeconds, 0, 0, 300)
		if node.Type == "agent" {
			node.AgentID = strings.TrimSpace(node.AgentID)
			if node.AgentID == "" {
				node.AgentID = firstAgentID
			}
			if _, ok := agentIDs[node.AgentID]; !ok {
				return fmt.Errorf("workflow node %q references unknown agent %q", node.ID, node.AgentID)
			}
		}
		if node.Type == "loop" {
			node.MaxIterations = clampInt(node.MaxIterations, 3, 1, 20)
		}
		if node.Type == "tool" {
			node.ToolName = boundedText(node.ToolName, "", 200)
			if node.ToolName == "" {
				return fmt.Errorf("tool node %q requires a toolName", node.ID)
			}
			if len(allowedTools) > 0 && !containsString(allowedTools, node.ToolName) {
				*notes = addNote(*notes, fmt.Sprintf("Tool %q is not in the current catalog; it will require approval at runtime.", node.ToolName))
			}
		}
		if node.Type == "artifact" && strings.TrimSpace(node.ArtifactPath) != "" && len(projectRoots) > 0 {
			if _, err := security.ValidateArtifactPath(node.ArtifactPath, projectRoots); err != nil {
				return fmt.Errorf("artifact node %q has an invalid path: %w", node.ID, err)
			}
		}
		node.Condition = boundedText(node.Condition, "", 400)
	}
	for index := range workflow.Nodes {
		if workflow.Nodes[index].Config != nil {
			workflow.Nodes[index].Config = safeNodeConfig(workflow.Nodes[index].Config, nodeIDs)
		}
	}
	if workflow.EntryNodeID == "" {
		workflow.EntryNodeID = workflow.Nodes[0].ID
	}
	if _, ok := nodeIDs[workflow.EntryNodeID]; !ok {
		return fmt.Errorf("workflow entryNodeID %q does not reference a node", workflow.EntryNodeID)
	}

	edges := make([]model.WorkflowEdge, 0, len(workflow.Edges))
	edgeIDs := make(map[string]struct{}, len(workflow.Edges))
	for index := range workflow.Edges {
		edge := workflow.Edges[index]
		edge.ID = normalizeID(edge.ID, fmt.Sprintf("edge-%d", index+1))
		if _, exists := edgeIDs[edge.ID]; exists {
			return fmt.Errorf("duplicate workflow edge id %q", edge.ID)
		}
		edgeIDs[edge.ID] = struct{}{}
		if _, ok := nodeIDs[edge.From]; !ok {
			return fmt.Errorf("workflow edge %q references unknown source %q", edge.ID, edge.From)
		}
		if _, ok := nodeIDs[edge.To]; !ok {
			return fmt.Errorf("workflow edge %q references unknown destination %q", edge.ID, edge.To)
		}
		edge.Condition = boundedText(edge.Condition, "", 400)
		edges = append(edges, edge)
	}
	workflow.Edges = edges
	validation := orchestrator.ValidateWorkflow(*workflow)
	if !validation.Valid {
		if len(validation.Errors) > 0 {
			return fmt.Errorf("builder workflow is invalid: %s", validation.Errors[0].Message)
		}
		return errors.New("builder workflow is invalid")
	}
	return nil
}

func safeNodeConfig(config map[string]any, nodeIDs map[string]struct{}) map[string]any {
	result := make(map[string]any)
	if value, ok := config["onExhausted"].(string); ok {
		value = strings.TrimSpace(value)
		if value != "" {
			if _, exists := nodeIDs[value]; exists {
				result["onExhausted"] = value
			}
		}
	}
	if raw, ok := config["milliseconds"]; ok {
		switch value := raw.(type) {
		case float64:
			if value >= 0 && value <= 300000 {
				result["milliseconds"] = value
			}
		case int:
			if value >= 0 && value <= 300000 {
				result["milliseconds"] = value
			}
		}
	}
	return result
}

func catalogTools(servers []model.MCPServer) []string {
	seen := make(map[string]struct{}, len(builtInPermissions))
	for permission := range builtInPermissions {
		seen[permission] = struct{}{}
	}
	for _, server := range servers {
		for _, tool := range server.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
			if server.ID != "" {
				seen[server.ID+"."+name] = struct{}{}
			}
			if server.Name != "" {
				seen[server.Name+"."+name] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for permission := range seen {
		result = append(result, permission)
	}
	return result
}

func normalizeWorkspaceRoots(roots, projectRoots []string, notes *[]string) []string {
	if len(roots) == 0 {
		return append([]string(nil), projectRoots...)
	}
	normalized, err := security.NormalizeRoots(roots)
	if err != nil {
		*notes = addNote(*notes, "Some requested workspace roots were invalid; the project folders were used instead.")
		return append([]string(nil), projectRoots...)
	}
	if len(projectRoots) == 0 {
		return normalized
	}
	allowed := make([]string, 0, len(normalized))
	for _, root := range normalized {
		if security.IsWithinRoots(root, projectRoots) {
			allowed = append(allowed, root)
		}
	}
	if len(allowed) == 0 {
		*notes = addNote(*notes, "Requested workspace roots were outside the active project; the project folders were used instead.")
		return append([]string(nil), projectRoots...)
	}
	return allowed
}

func normalizeTools(tools, allowed []string, notes *[]string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, tool := range allowed {
		allowedSet[tool] = struct{}{}
	}
	result := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, raw := range tools {
		tool := strings.TrimSpace(raw)
		if tool == "" || len(tool) > 200 || strings.IndexFunc(tool, unicode.IsControl) >= 0 {
			continue
		}
		if len(allowedSet) > 0 {
			if _, ok := allowedSet[tool]; !ok {
				*notes = addNote(*notes, fmt.Sprintf("Permission %q was removed because it is not in the current catalog.", tool))
				continue
			}
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		result = append(result, tool)
		if len(result) >= 64 {
			break
		}
	}
	return result
}

func normalizeModelID(value string, allowed map[string]model.ModelInfo) string {
	value = strings.TrimSpace(value)
	if value == "" || len(allowed) == 0 {
		return value
	}
	entry, ok := allowed[strings.ToLower(value)]
	if !ok {
		return ""
	}
	return entry.ID
}

func normalizeEffort(value string, modelInfo model.ModelInfo) string {
	value = strings.TrimSpace(value)
	if value == "" || len(modelInfo.SupportedReasoningEfforts) == 0 {
		return value
	}
	for _, effort := range modelInfo.SupportedReasoningEfforts {
		if strings.EqualFold(effort.ReasoningEffort, value) {
			return effort.ReasoningEffort
		}
	}
	return ""
}

func normalizeApprovalProfile(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "autonomous", "full", "full_access", "full access", "never":
		return "autonomous"
	default:
		return "on_request"
	}
}

func normalizeRoom(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "strategy", "library":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "workshop"
	}
}

func normalizeID(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 120 {
		return value[:120]
	}
	return value
}

func boundedText(value, fallback string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len([]byte(value)) <= maxBytes {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && len([]byte(string(runes))) > maxBytes {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func clampInt(value, fallback, min, max int) int {
	if value == 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func normalizeNotes(notes []string) []string {
	result := make([]string, 0, minInt(len(notes), 12))
	for _, note := range notes {
		note = boundedText(note, "", 500)
		if note != "" {
			result = append(result, note)
		}
		if len(result) >= 12 {
			break
		}
	}
	return result
}

func addNote(notes []string, note string) []string {
	for _, existing := range notes {
		if existing == note {
			return notes
		}
	}
	if len(notes) >= 12 {
		return notes
	}
	return append(notes, note)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
