package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/orchestrator"
	"github.com/RuanFernandes/centurion/internal/security"
)

const (
	// MaxRequestBytes protects direct Builder messages from accidentally
	// receiving an unbounded payload. Keep it aligned with the Planner handoff
	// limit so a long, user-authored implementation brief is not rejected just
	// because it was entered directly in Build mode.
	MaxRequestBytes         = 64 * 1024
	MaxPlanningMessageBytes = 64 * 1024
	MaxHandoffMessageBytes  = 64 * 1024
	MaxOutputBytes          = 128 * 1024
	// Execution briefs are included in the initial prompt for every workflow
	// agent. Keep them intentionally compact; node prompts carry each step's
	// detailed instructions.
	MaxExecutionBriefBytes = 3 * 1024
	MaxAgents              = 24
	MaxWorkflowNodes       = 64
	MaxWorkflowEdges       = 128
)

type Catalog struct {
	Models     []model.ModelInfo
	MCPServers []model.MCPServer
}

// ApplyExecutionBrief persists the Builder's compact objective with the
// workflow. A raw Builder request can contain an entire planning transcript,
// so it is only a last-resort fallback; otherwise every runtime agent would
// receive that transcript again.
func ApplyExecutionBrief(proposal model.BuilderProposal, prompt string) model.BuilderProposal {
	briefSource := proposal.ExecutionBrief
	if strings.TrimSpace(briefSource) == "" && proposal.Workflow != nil {
		briefSource = proposal.Workflow.ExecutionBrief
	}
	fallback := boundedText(proposal.Summary, boundedText(prompt, "", MaxExecutionBriefBytes), MaxExecutionBriefBytes)
	brief := boundedText(briefSource, fallback, MaxExecutionBriefBytes)
	proposal.ExecutionBrief = brief
	if proposal.Workflow != nil {
		proposal.Workflow.ExecutionBrief = brief
	}
	return proposal
}

var allowedNodeTypes = map[string]struct{}{
	"agent": {}, "condition": {}, "parallel": {}, "join": {}, "loop": {}, "approval": {}, "tool": {}, "artifact": {},
}

// builtInPermissions are Centurion capability IDs. They are deliberately not
// App Server method or Codex tool names; the executor maps file-write access
// to its sandbox and the prompt explains how agents use actual exposed tools.
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
		"required":             []string{"schemaVersion", "summary", "notes", "agents", "workflow"},
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
					"required":             []string{"temporaryID", "name", "role", "instructions", "modelID", "reasoningEffort", "workspaceRoots", "toolAllowlist", "approvalProfile", "roomID", "avatarID", "maxDurationSeconds", "maxTurns", "maxAttempts"},
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
	proposalJSON := []byte(cleaned[start : end+1])
	proposalJSON, err := normalizeSchemaVersionJSON(proposalJSON)
	if err != nil {
		return model.BuilderProposal{}, err
	}
	if err := json.Unmarshal(proposalJSON, &proposal); err != nil {
		return model.BuilderProposal{}, fmt.Errorf("decode Codex builder proposal: %w", err)
	}
	applyAgentFieldAliases(&proposal, proposalJSON)
	if proposal.Workflow != nil && len(proposal.Workflow.Edges) == 0 {
		proposal.Workflow.Edges = parseWorkflowConnectionAliases(proposalJSON)
	}
	return proposal, nil
}

// normalizeSchemaVersionJSON accepts the common LLM variant
// {"schemaVersion":"1"}. The persisted contract remains numeric and later
// normalization still owns the accepted schema version.
func normalizeSchemaVersionJSON(raw []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode Codex builder proposal: %w", err)
	}
	value, ok := envelope["schemaVersion"]
	if !ok {
		return raw, nil
	}
	var stringVersion string
	if err := json.Unmarshal(value, &stringVersion); err != nil {
		return raw, nil
	}
	version, err := strconv.Atoi(strings.TrimSpace(stringVersion))
	if err != nil {
		return nil, fmt.Errorf("decode Codex builder proposal: schemaVersion must be an integer, got %q", stringVersion)
	}
	envelope["schemaVersion"] = json.RawMessage(strconv.AppendInt(nil, int64(version), 10))
	normalized, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("normalize Codex builder proposal: %w", err)
	}
	return normalized, nil
}

// applyAgentFieldAliases keeps proposals produced by older Builder prompts
// compatible with the canonical model. The aliases are accepted only while
// parsing the external proposal; persisted profiles always use the current
// field names and validation rules.
func applyAgentFieldAliases(proposal *model.BuilderProposal, raw []byte) {
	if proposal == nil {
		return
	}
	var envelope struct {
		Agents []json.RawMessage `json:"agents"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	for index := range proposal.Agents {
		if index >= len(envelope.Agents) {
			break
		}
		var aliases struct {
			Model       string   `json:"model"`
			Prompt      string   `json:"prompt"`
			Permissions []string `json:"permissions"`
			MCPTools    []string `json:"mcpTools"`
			Role        string   `json:"role"`
		}
		if json.Unmarshal(envelope.Agents[index], &aliases) != nil {
			continue
		}
		agent := &proposal.Agents[index]
		if strings.TrimSpace(agent.ModelID) == "" {
			agent.ModelID = strings.TrimSpace(aliases.Model)
		}
		if strings.TrimSpace(agent.Instructions) == "" {
			agent.Instructions = strings.TrimSpace(aliases.Prompt)
		}
		if strings.TrimSpace(agent.Role) == "" {
			agent.Role = firstNonEmptyAgentRole(aliases.Role, agent.AvatarID)
		}
		agent.ToolAllowlist = appendUniqueStrings(agent.ToolAllowlist, aliases.Permissions...)
		agent.ToolAllowlist = appendUniqueStrings(agent.ToolAllowlist, aliases.MCPTools...)
	}
}

func firstNonEmptyAgentRole(role, avatarID string) string {
	if strings.TrimSpace(role) != "" {
		return strings.TrimSpace(role)
	}
	switch strings.ToLower(strings.TrimSpace(avatarID)) {
	case "supervisor":
		return "Operations supervisor"
	case "researcher":
		return "Researcher"
	case "reviewer":
		return "Quality reviewer"
	case "builder":
		return "Software implementer"
	default:
		return ""
	}
}

func appendUniqueStrings(values []string, additions ...string) []string {
	result := append([]string(nil), values...)
	seen := make(map[string]struct{}, len(result)+len(additions))
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if result[index] != "" {
			seen[strings.ToLower(result[index])] = struct{}{}
		}
	}
	for _, value := range additions {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

// parseWorkflowConnectionAliases keeps the Builder tolerant of the two terms
// models commonly use for the same graph concept. The canonical persisted
// field remains workflow.edges; connections and links are accepted only at
// the proposal boundary.
func parseWorkflowConnectionAliases(raw []byte) []model.WorkflowEdge {
	var envelope struct {
		Workflow json.RawMessage `json:"workflow"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Workflow) == 0 || string(envelope.Workflow) == "null" {
		return nil
	}
	var workflow map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Workflow, &workflow); err != nil {
		return nil
	}
	for _, key := range []string{"connections", "links"} {
		if encoded, ok := workflow[key]; ok {
			if edges := decodeWorkflowConnections(encoded); len(edges) > 0 {
				return edges
			}
		}
	}
	// Some Builder responses describe a linear or branching route as a
	// `next` field on each node instead of emitting the canonical edges array.
	// Convert that compatibility form at the proposal boundary so the
	// persisted workflow and executor always use one graph representation.
	encodedNodes, ok := workflow["nodes"]
	if !ok {
		return nil
	}
	var nodes []map[string]json.RawMessage
	if json.Unmarshal(encodedNodes, &nodes) != nil {
		return nil
	}
	edges := make([]model.WorkflowEdge, 0)
	seen := make(map[string]struct{})
	for _, node := range nodes {
		from := connectionString(node, "id", "nodeID")
		if from == "" {
			continue
		}
		for _, key := range []string{"next", "nextNodeID", "nextNodeIDs"} {
			next, exists := node[key]
			if !exists {
				continue
			}
			for _, target := range decodeNodeNextTargets(next) {
				to := connectionNodeIDFromRaw(target.raw)
				if to == "" || to == from {
					continue
				}
				condition := connectionString(target.object, "condition", "when", "expression")
				key := from + "\x00" + to + "\x00" + condition
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				edges = append(edges, model.WorkflowEdge{ID: fmt.Sprintf("edge-next-%d", len(edges)+1), From: from, To: to, Condition: condition})
			}
		}
	}
	return edges
}

type nodeNextTarget struct {
	raw    json.RawMessage
	object map[string]json.RawMessage
}

func decodeNodeNextTargets(raw json.RawMessage) []nodeNextTarget {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		values = []json.RawMessage{raw}
	}
	result := make([]nodeNextTarget, 0, len(values))
	for _, value := range values {
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil {
			object = nil
		}
		result = append(result, nodeNextTarget{raw: value, object: object})
	}
	return result
}

func decodeWorkflowConnections(raw json.RawMessage) []model.WorkflowEdge {
	result := make([]model.WorkflowEdge, 0)
	seen := make(map[string]struct{})
	appendEdge := func(id, from, to, condition string) {
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if from == "" || to == "" || from == to {
			return
		}
		key := from + "\x00" + to + "\x00" + strings.TrimSpace(condition)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("edge-%d", len(result)+1)
		}
		result = append(result, model.WorkflowEdge{ID: id, From: from, To: to, Condition: strings.TrimSpace(condition)})
	}

	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		for _, item := range items {
			var object map[string]json.RawMessage
			if json.Unmarshal(item, &object) != nil {
				continue
			}
			from := connectionNodeID(object, "from", "source", "sourceID", "output")
			condition := connectionString(object, "condition", "when", "expression")
			to := connectionNodeID(object, "to", "target", "targetID", "input", "node")
			if to != "" {
				appendEdge(connectionString(object, "id", "edgeID"), from, to, condition)
				continue
			}
			for _, key := range []string{"targets", "toNodes", "destinations"} {
				encoded, ok := object[key]
				if !ok {
					continue
				}
				var targets []json.RawMessage
				if json.Unmarshal(encoded, &targets) != nil {
					continue
				}
				for _, target := range targets {
					appendEdge("", from, connectionNodeIDFromRaw(target), condition)
				}
			}
		}
		return result
	}

	var adjacency map[string]json.RawMessage
	if json.Unmarshal(raw, &adjacency) != nil {
		return result
	}
	for from, encoded := range adjacency {
		var targets []json.RawMessage
		if json.Unmarshal(encoded, &targets) != nil {
			targets = []json.RawMessage{encoded}
		}
		for _, target := range targets {
			var object map[string]json.RawMessage
			if json.Unmarshal(target, &object) == nil && object != nil {
				condition := connectionString(object, "condition", "when", "expression")
				appendEdge(connectionString(object, "id", "edgeID"), from, connectionNodeID(object, "to", "target", "targetID", "node", "input"), condition)
				continue
			}
			appendEdge("", from, connectionNodeIDFromRaw(target), "")
		}
	}
	return result
}

func connectionString(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if encoded, ok := object[key]; ok {
			var value string
			if json.Unmarshal(encoded, &value) == nil {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func connectionNodeID(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if encoded, ok := object[key]; ok {
			if value := connectionNodeIDFromRaw(encoded); value != "" {
				return value
			}
		}
	}
	return ""
}

func connectionNodeIDFromRaw(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	return connectionString(object, "id", "node", "nodeID", "name", "to", "target")
}

func NormalizeProposal(proposal model.BuilderProposal, project model.Project, catalog Catalog) (model.BuilderProposal, error) {
	proposal.SchemaVersion = 1
	proposal.Summary = boundedText(proposal.Summary, "Proposed Centurion workspace", 1200)
	proposal.ExecutionBrief = boundedText(proposal.ExecutionBrief, "", MaxExecutionBriefBytes)
	if proposal.Workflow != nil {
		proposal.Workflow.ExecutionBrief = boundedText(proposal.Workflow.ExecutionBrief, proposal.ExecutionBrief, MaxExecutionBriefBytes)
	}
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
		agent.AvatarID = normalizeAgentAvatar(agent.AvatarID, agent.Role, agent.Instructions)
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
		completeWorkflowAgentReferences(&proposal, agentIDs, project.Folders, &proposal.Notes)
		if err := normalizeWorkflow(proposal.Workflow, agentIDs, firstAgentID, project.Folders, allowedTools, &proposal.Notes); err != nil {
			return model.BuilderProposal{}, err
		}
	}
	return proposal, nil
}

// completeWorkflowAgentReferences repairs the common case where the model
// gives a workflow node a suffixed variant of an agent ID, or forgets to emit
// the corresponding draft agent. The generated placeholder is deliberately
// approval-gated and remains visible in the reviewable proposal.
func completeWorkflowAgentReferences(proposal *model.BuilderProposal, agentIDs map[string]struct{}, projectRoots []string, notes *[]string) {
	if proposal == nil || proposal.Workflow == nil {
		return
	}
	for index := range proposal.Workflow.Nodes {
		node := &proposal.Workflow.Nodes[index]
		if strings.ToLower(strings.TrimSpace(node.Type)) != "agent" {
			continue
		}
		reference := strings.TrimSpace(node.AgentID)
		if reference == "" {
			continue
		}
		if resolved := resolveAgentReference(reference, agentIDs); resolved != "" {
			if resolved != reference {
				*notes = addNote(*notes, fmt.Sprintf("Workflow node %q was mapped from missing agent reference %q to %q.", node.ID, reference, resolved))
				node.AgentID = resolved
			}
			continue
		}

		generatedID := normalizeID(reference, fmt.Sprintf("agent-workflow-%d", len(proposal.Agents)+1))
		if _, exists := agentIDs[generatedID]; exists {
			generatedID = fmt.Sprintf("%s-%d", generatedID, len(proposal.Agents)+1)
		}
		proposal.Agents = append(proposal.Agents, model.BuilderAgentDraft{
			TemporaryID:        generatedID,
			Name:               fmt.Sprintf("Workflow agent %d", len(proposal.Agents)+1),
			Role:               "Workflow specialist",
			Instructions:       "Complete the responsibilities implied by the workflow node, stay inside the configured workspace, and report a verifiable result.",
			WorkspaceRoots:     append([]string(nil), projectRoots...),
			ApprovalProfile:    "on_request",
			RoomID:             "workshop",
			AvatarID:           "operator",
			MaxDurationSeconds: 1800,
			MaxTurns:           12,
			MaxAttempts:        2,
		})
		agentIDs[generatedID] = struct{}{}
		node.AgentID = generatedID
		*notes = addNote(*notes, fmt.Sprintf("Workflow node %q referenced missing agent %q; Centurion added an approval-gated placeholder for review.", node.ID, reference))
	}
}

func resolveAgentReference(reference string, agentIDs map[string]struct{}) string {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return ""
	}
	if _, exists := agentIDs[reference]; exists {
		return reference
	}
	lowerReference := strings.ToLower(reference)
	for candidate := range agentIDs {
		if strings.ToLower(candidate) == lowerReference {
			return candidate
		}
	}
	resolved := ""
	for candidate := range agentIDs {
		lowerCandidate := strings.ToLower(strings.TrimSpace(candidate))
		if lowerCandidate == "" || len(lowerCandidate) <= len(resolved) {
			continue
		}
		if strings.HasPrefix(lowerReference, lowerCandidate+"-") || strings.HasPrefix(lowerReference, lowerCandidate+"_") {
			resolved = candidate
		}
	}
	return resolved
}

// ApplyAgentModelSelection makes the application's model selection
// authoritative for every generated agent. This prevents the Builder model
// from silently choosing a different model or reasoning effort per agent.
// Empty values intentionally clear model metadata so runtime agents inherit
// the user's Codex configuration.
func ApplyAgentModelSelection(proposal model.BuilderProposal, modelID, reasoningEffort string) model.BuilderProposal {
	modelID = strings.TrimSpace(modelID)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	for index := range proposal.Agents {
		proposal.Agents[index].ModelID = modelID
		proposal.Agents[index].ReasoningEffort = reasoningEffort
	}
	return proposal
}

// NormalizeSubagentApprovalProfile converts the UI choice into the only two
// approval modes supported by generated agents. Empty input is intentionally
// conservative because BuilderRequest can also be called by older clients.
func NormalizeSubagentApprovalProfile(value string) string {
	if strings.TrimSpace(value) == "" {
		return "on_request"
	}
	return normalizeApprovalProfile(value)
}

// ApplySubagentPermissions makes the user's Builder setting authoritative for
// every generated profile. The model may suggest permissions in its JSON, but
// it must not silently elevate or downgrade the explicit UI choice.
func ApplySubagentPermissions(proposal model.BuilderProposal, value string) model.BuilderProposal {
	profile := NormalizeSubagentApprovalProfile(value)
	for index := range proposal.Agents {
		proposal.Agents[index].ApprovalProfile = profile
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
	workflow.ExecutionBrief = boundedText(workflow.ExecutionBrief, "", MaxExecutionBriefBytes)
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
		node.Prompt = boundedText(node.Prompt, "", 8*1024)
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
	if len(workflow.Nodes) > 1 && len(workflow.Edges) == 0 {
		workflow.Edges = sequentialWorkflowEdges(workflow.Nodes)
		*notes = addNote(*notes, "The Builder returned multiple steps without connections; Centurion linked them in declared order. Review the route before running.")
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

func sequentialWorkflowEdges(nodes []model.WorkflowNode) []model.WorkflowEdge {
	if len(nodes) < 2 {
		return nil
	}
	edges := make([]model.WorkflowEdge, 0, len(nodes)-1)
	for index := 0; index < len(nodes)-1; index++ {
		edges = append(edges, model.WorkflowEdge{
			ID:   fmt.Sprintf("edge-auto-%d", index+1),
			From: nodes[index].ID,
			To:   nodes[index+1].ID,
		})
	}
	return edges
}

func safeNodeConfig(config map[string]any, nodeIDs map[string]struct{}) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"onExhausted", "fallbackNodeID"} {
		if value, ok := config[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				if _, exists := nodeIDs[value]; exists {
					result[key] = value
				}
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

func normalizeAgentAvatar(value, role, instructions string) string {
	context := strings.ToLower(strings.TrimSpace(role + " " + instructions))
	switch {
	case strings.Contains(context, "research"), strings.Contains(context, "analys"), strings.Contains(context, "evidence"), strings.Contains(context, "investigat"):
		return "researcher"
	case strings.Contains(context, "review"), strings.Contains(context, "quality"), strings.Contains(context, "security"), strings.Contains(context, "audit"), strings.Contains(context, "test"):
		return "reviewer"
	case strings.Contains(context, "supervis"), strings.Contains(context, "tech lead"), strings.Contains(context, "team lead"), strings.Contains(context, "architect"), strings.Contains(context, "orchestrat"), strings.Contains(context, "coordinat"), strings.Contains(context, "planner"):
		return "supervisor"
	}

	switch strings.ToLower(strings.TrimSpace(value)) {
	case "supervisor", "builder", "researcher", "reviewer":
		return strings.ToLower(strings.TrimSpace(value))
	case "architect":
		return "supervisor"
	}
	return "builder"
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
