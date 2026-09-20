package model

const (
	AuthStatusOffline   = "offline"
	AuthStatusChecking  = "checking"
	AuthStatusLoggedOut = "logged_out"
	AuthStatusLoggedIn  = "logged_in"
	AuthStatusError     = "error"
)

const (
	RunStatusQueued          = "queued"
	RunStatusRunning         = "running"
	RunStatusPaused          = "paused"
	RunStatusWaitingApproval = "waiting_approval"
	RunStatusCompleted       = "completed"
	RunStatusBlocked         = "blocked"
	RunStatusFailed          = "failed"
	RunStatusCanceled        = "canceled"
	RunStatusInterrupted     = "interrupted"
)

const (
	AgentStateIdle            = "idle"
	AgentStateWorking         = "working"
	AgentStateWaitingApproval = "waiting_approval"
	AgentStateBlocked         = "blocked"
	AgentStateRunningTool     = "running_tool"
	AgentStateHandoff         = "handoff"
	AgentStateSuccess         = "success"
	AgentStateError           = "error"
)

type AuthState struct {
	Status             string             `json:"status"`
	AccountType        string             `json:"accountType,omitempty"`
	Email              string             `json:"email,omitempty"`
	Plan               string             `json:"plan,omitempty"`
	RequiresOpenAIAuth bool               `json:"requiresOpenAIAuth"`
	RateLimit          *RateLimitSnapshot `json:"rateLimit,omitempty"`
	Error              string             `json:"error,omitempty"`
	UpdatedAt          string             `json:"updatedAt"`
}

type RateLimitSnapshot struct {
	LimitID              string            `json:"limitId,omitempty"`
	LimitName            string            `json:"limitName,omitempty"`
	UsedPercent          int               `json:"usedPercent"`
	WindowDurationMins   int               `json:"windowDurationMins"`
	ResetsAt             int64             `json:"resetsAt,omitempty"`
	RateLimitReachedType string            `json:"rateLimitReachedType,omitempty"`
	Secondary            *RateLimitWindow  `json:"secondary,omitempty"`
	Buckets              []RateLimitBucket `json:"buckets,omitempty"`
}

type RateLimitWindow struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt,omitempty"`
}

type RateLimitBucket struct {
	LimitID              string           `json:"limitId,omitempty"`
	LimitName            string           `json:"limitName,omitempty"`
	Primary              *RateLimitWindow `json:"primary,omitempty"`
	Secondary            *RateLimitWindow `json:"secondary,omitempty"`
	RateLimitReachedType string           `json:"rateLimitReachedType,omitempty"`
}

func (snapshot RateLimitSnapshot) IsReached() bool {
	if snapshot.RateLimitReachedType != "" || snapshot.UsedPercent >= 100 {
		return true
	}
	if snapshot.Secondary != nil && snapshot.Secondary.UsedPercent >= 100 {
		return true
	}
	for _, bucket := range snapshot.Buckets {
		if bucket.RateLimitReachedType != "" {
			return true
		}
		if bucket.Primary != nil && bucket.Primary.UsedPercent >= 100 {
			return true
		}
		if bucket.Secondary != nil && bucket.Secondary.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

type ReasoningEffort struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description,omitempty"`
}

type ModelInfo struct {
	ID                        string            `json:"id"`
	DisplayName               string            `json:"displayName"`
	Description               string            `json:"description,omitempty"`
	DefaultReasoningEffort    string            `json:"defaultReasoningEffort,omitempty"`
	SupportedReasoningEfforts []ReasoningEffort `json:"supportedReasoningEfforts,omitempty"`
	InputModalities           []string          `json:"inputModalities,omitempty"`
	SupportsPersonality       bool              `json:"supportsPersonality"`
	IsDefault                 bool              `json:"isDefault"`
}

type AgentProfile struct {
	ID                 string   `json:"id"`
	ProjectID          string   `json:"projectID,omitempty"`
	Name               string   `json:"name"`
	Role               string   `json:"role"`
	Instructions       string   `json:"instructions"`
	ModelID            string   `json:"modelID"`
	ReasoningEffort    string   `json:"reasoningEffort"`
	WorkspaceRoots     []string `json:"workspaceRoots"`
	ToolAllowlist      []string `json:"toolAllowlist"`
	ApprovalProfile    string   `json:"approvalProfile"`
	RoomID             string   `json:"roomID"`
	AvatarID           string   `json:"avatarID"`
	SpriteID           string   `json:"spriteID"`
	VisualState        string   `json:"visualState"`
	MaxDurationSeconds int      `json:"maxDurationSeconds"`
	MaxTurns           int      `json:"maxTurns"`
	MaxAttempts        int      `json:"maxAttempts"`
	MemorySummary      string   `json:"memorySummary"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

// BuilderRequest is the user-authored input sent to the in-app Codex
// configuration builder. Handoff marks the controlled Planner-to-Builder
// transition, which is allowed a larger bounded payload than a direct build
// request because it carries a compacted planning transcript.
type BuilderRequest struct {
	Prompt                  string `json:"prompt"`
	ProjectID               string `json:"projectID,omitempty"`
	ThreadID                string `json:"threadID,omitempty"`
	ModelID                 string `json:"modelID,omitempty"`
	ReasoningEffort         string `json:"reasoningEffort,omitempty"`
	SubagentApprovalProfile string `json:"subagentApprovalProfile,omitempty"`
	Handoff                 bool   `json:"handoff,omitempty"`
}

// PlanningRequest is a user-authored message sent to the read-only planning
// room. The selected agent contributes its identity and instructions; an
// empty model selection delegates model and automatic-effort resolution to
// the user's Codex App Server configuration. Workspace and tool access are
// always stripped before opening the Codex turn.
type PlanningRequest struct {
	Prompt          string `json:"prompt"`
	ProjectID       string `json:"projectID,omitempty"`
	ThreadID        string `json:"threadID,omitempty"`
	PlannerAgentID  string `json:"plannerAgentID,omitempty"`
	ModelID         string `json:"modelID,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type PlanningResponse struct {
	ThreadID string `json:"threadID"`
	Reply    string `json:"reply"`
}

// BuilderAgentDraft contains only declarative profile data. TemporaryID is
// used for references inside the proposed workflow and is replaced by a
// server-generated UUID when the proposal is applied.
type BuilderAgentDraft struct {
	TemporaryID        string   `json:"temporaryID"`
	Name               string   `json:"name"`
	Role               string   `json:"role"`
	Instructions       string   `json:"instructions"`
	ModelID            string   `json:"modelID,omitempty"`
	ReasoningEffort    string   `json:"reasoningEffort,omitempty"`
	WorkspaceRoots     []string `json:"workspaceRoots,omitempty"`
	ToolAllowlist      []string `json:"toolAllowlist,omitempty"`
	ApprovalProfile    string   `json:"approvalProfile"`
	RoomID             string   `json:"roomID,omitempty"`
	AvatarID           string   `json:"avatarID,omitempty"`
	MaxDurationSeconds int      `json:"maxDurationSeconds,omitempty"`
	MaxTurns           int      `json:"maxTurns,omitempty"`
	MaxAttempts        int      `json:"maxAttempts,omitempty"`
}

// BuilderProposal is returned by Codex and remains inert until explicitly
// applied by the user. Workflow references point to BuilderAgentDraft IDs.
type BuilderProposal struct {
	SchemaVersion  int                 `json:"schemaVersion"`
	Summary        string              `json:"summary"`
	ExecutionBrief string              `json:"executionBrief,omitempty"`
	Notes          []string            `json:"notes,omitempty"`
	Agents         []BuilderAgentDraft `json:"agents"`
	Workflow       *WorkflowDefinition `json:"workflow,omitempty"`
}

type BuilderResponse struct {
	ThreadID string          `json:"threadID"`
	Reply    string          `json:"reply"`
	Proposal BuilderProposal `json:"proposal"`
}

// BuilderStatus lets the UI reconnect to a planning or build request after
// navigation. The final response is retained in memory until the next
// request, while private notification payloads and reasoning text are never
// stored here.
type BuilderStatus struct {
	SchemaVersion    int               `json:"schemaVersion"`
	Timestamp        string            `json:"timestamp"`
	Sequence         int64             `json:"sequence"`
	Source           string            `json:"source"`
	RequestID        string            `json:"requestID"`
	ProjectID        string            `json:"projectID,omitempty"`
	Scope            string            `json:"scope"`
	State            string            `json:"state"`
	Activity         string            `json:"activity"`
	Detail           string            `json:"detail,omitempty"`
	ThreadID         string            `json:"threadID,omitempty"`
	TurnID           string            `json:"turnID,omitempty"`
	StartedAt        string            `json:"startedAt"`
	UpdatedAt        string            `json:"updatedAt"`
	Error            string            `json:"error,omitempty"`
	PlanningResponse *PlanningResponse `json:"planningResponse,omitempty"`
	BuilderResponse  *BuilderResponse  `json:"builderResponse,omitempty"`
}

type BuilderApplyRequest struct {
	ProjectID string          `json:"projectID,omitempty"`
	Proposal  BuilderProposal `json:"proposal"`
}

type BuilderApplyResult struct {
	Agents   []AgentProfile      `json:"agents"`
	Workflow *WorkflowDefinition `json:"workflow,omitempty"`
}

type Project struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Folders      []string `json:"folders"`
	ManifestPath string   `json:"manifestPath,omitempty"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
	LastOpenedAt string   `json:"lastOpenedAt,omitempty"`
}

type LearnSystemRequest struct {
	ProjectID       string `json:"projectID,omitempty"`
	ModelID         string `json:"modelID,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type LearnSystemResult struct {
	ProjectID       string   `json:"projectID"`
	Status          string   `json:"status"`
	PrimaryFolder   string   `json:"primaryFolder"`
	DocsPath        string   `json:"docsPath"`
	DocsFiles       []string `json:"docsFiles"`
	MissingFiles    []string `json:"missingFiles,omitempty"`
	ModelID         string   `json:"modelID,omitempty"`
	ReasoningEffort string   `json:"reasoningEffort,omitempty"`
	ThreadID        string   `json:"threadID,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	DurationMS      int64    `json:"durationMs"`
}

type ProjectSnapshot struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ExportedAt    string               `json:"exportedAt"`
	Project       Project              `json:"project"`
	Agents        []AgentProfile       `json:"agents"`
	Workflows     []WorkflowDefinition `json:"workflows"`
	Schedules     []Schedule           `json:"schedules"`
	History       []HistoryEntry       `json:"history"`
}

// ProjectConfig is the portable, project-owned catalog stored under the
// primary workspace folder's .centurion directory. Runs, history, and audit
// records remain in the local SQLite database because they are runtime data.
type ProjectConfig struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ProjectID     string               `json:"projectID"`
	Folders       []string             `json:"folders"`
	UpdatedAt     string               `json:"updatedAt"`
	Agents        []AgentProfile       `json:"agents"`
	Workflows     []WorkflowDefinition `json:"workflows"`
	Schedules     []Schedule           `json:"schedules"`
}

type WorkflowLimits struct {
	MaxDurationSeconds int `json:"maxDurationSeconds"`
	MaxParallel        int `json:"maxParallel"`
	MaxTurns           int `json:"maxTurns"`
	MaxPromptTokens    int `json:"maxPromptTokens"`
}

type RetryPolicy struct {
	MaxAttempts    int  `json:"maxAttempts"`
	BackoffSeconds int  `json:"backoffSeconds"`
	Idempotent     bool `json:"idempotent"`
}

type WorkflowNode struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	Label          string         `json:"label"`
	AgentID        string         `json:"agentID,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Condition      string         `json:"condition,omitempty"`
	ToolName       string         `json:"toolName,omitempty"`
	ArtifactPath   string         `json:"artifactPath,omitempty"`
	MaxIterations  int            `json:"maxIterations,omitempty"`
	TimeoutSeconds int            `json:"timeoutSeconds,omitempty"`
	Retry          RetryPolicy    `json:"retry"`
	Config         map[string]any `json:"config,omitempty"`
}

type WorkflowEdge struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

type WorkflowDefinition struct {
	ID             string         `json:"id"`
	ProjectID      string         `json:"projectID,omitempty"`
	Name           string         `json:"name"`
	Version        int            `json:"version"`
	Description    string         `json:"description,omitempty"`
	ExecutionBrief string         `json:"executionBrief,omitempty"`
	EntryNodeID    string         `json:"entryNodeID"`
	Nodes          []WorkflowNode `json:"nodes"`
	Edges          []WorkflowEdge `json:"edges"`
	GlobalLimits   WorkflowLimits `json:"globalLimits"`
	ErrorPolicy    string         `json:"errorPolicy"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
}

type ValidationIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type WorkflowValidation struct {
	Valid    bool              `json:"valid"`
	Errors   []ValidationIssue `json:"errors,omitempty"`
	Warnings []ValidationIssue `json:"warnings,omitempty"`
}

type Run struct {
	ID                string         `json:"id"`
	ProjectID         string         `json:"projectID,omitempty"`
	WorkflowID        string         `json:"workflowID"`
	Status            string         `json:"status"`
	Input             map[string]any `json:"input,omitempty"`
	Output            map[string]any `json:"output,omitempty"`
	CurrentNodeID     string         `json:"currentNodeID,omitempty"`
	StartedAt         string         `json:"startedAt"`
	UpdatedAt         string         `json:"updatedAt"`
	CompletedAt       string         `json:"completedAt,omitempty"`
	Error             string         `json:"error,omitempty"`
	CancelRequested   bool           `json:"cancelRequested"`
	Paused            bool           `json:"paused"`
	PromptTokensUsed  int            `json:"promptTokensUsed"`
	PromptTokenBudget int            `json:"promptTokenBudget"`
	OutputBytes       int            `json:"outputBytes"`
}

// RunStep is the durable checkpoint for one workflow node. It is exposed to
// the Office inspector so users can understand what each agent actually did.
type RunStep struct {
	RunID       string         `json:"runID"`
	NodeID      string         `json:"nodeID"`
	Status      string         `json:"status"`
	Attempt     int            `json:"attempt"`
	ThreadID    string         `json:"threadID,omitempty"`
	TurnID      string         `json:"turnID,omitempty"`
	Output      map[string]any `json:"output,omitempty"`
	Error       string         `json:"error,omitempty"`
	StartedAt   string         `json:"startedAt,omitempty"`
	CompletedAt string         `json:"completedAt,omitempty"`
}

type RunFilter struct {
	ProjectID  string `json:"projectID,omitempty"`
	WorkflowID string `json:"workflowID,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type HistoryEntry struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"projectID,omitempty"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Content   string         `json:"content,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

type HistoryFilter struct {
	ProjectID string `json:"projectID,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type AuditEntry struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"projectID,omitempty"`
	RunID     string         `json:"runID,omitempty"`
	Kind      string         `json:"kind"`
	Actor     string         `json:"actor"`
	Target    string         `json:"target,omitempty"`
	Decision  string         `json:"decision,omitempty"`
	Detail    string         `json:"detail,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

type AuditFilter struct {
	ProjectID string `json:"projectID,omitempty"`
	RunID     string `json:"runID,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type RuntimeStatus struct {
	Connected      bool   `json:"connected"`
	CodexCommand   string `json:"codexCommand"`
	AppServerPID   int    `json:"appServerPID,omitempty"`
	ActiveSessions int    `json:"activeSessions"`
	MaxSessions    int    `json:"maxSessions"`
	ActiveRuns     int    `json:"activeRuns"`
	AuthStatus     string `json:"authStatus"`
	UpdatedAt      string `json:"updatedAt"`
}

type TerminalResult struct {
	Command    string `json:"command"`
	WorkingDir string `json:"workingDir"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exitCode"`
	DurationMS int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated"`
}

type RunEvent struct {
	SchemaVersion int            `json:"schemaVersion"`
	Timestamp     string         `json:"timestamp"`
	Sequence      int64          `json:"sequence"`
	Source        string         `json:"source"`
	RunID         string         `json:"runID"`
	Type          string         `json:"type"`
	Level         string         `json:"level"`
	NodeID        string         `json:"nodeID,omitempty"`
	AgentID       string         `json:"agentID,omitempty"`
	Message       string         `json:"message"`
	Data          map[string]any `json:"data,omitempty"`
}

type AgentStateEvent struct {
	SchemaVersion int    `json:"schemaVersion"`
	Timestamp     string `json:"timestamp"`
	Sequence      int64  `json:"sequence"`
	Source        string `json:"source"`
	RunID         string `json:"runID,omitempty"`
	AgentID       string `json:"agentID"`
	State         string `json:"state"`
	NodeID        string `json:"nodeID,omitempty"`
	Message       string `json:"message,omitempty"`
}

type ApprovalRequest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Timestamp     string   `json:"timestamp"`
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Detail        string   `json:"detail,omitempty"`
	RunID         string   `json:"runID,omitempty"`
	ThreadID      string   `json:"threadID,omitempty"`
	TurnID        string   `json:"turnID,omitempty"`
	ItemID        string   `json:"itemID,omitempty"`
	Choices       []string `json:"choices,omitempty"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
}

type ApprovalDecision struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
}

type MCPTool struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	RequiresApproval bool   `json:"requiresApproval"`
}

type MCPServer struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	AuthStatus string    `json:"authStatus,omitempty"`
	Tools      []MCPTool `json:"tools,omitempty"`
	Resources  []string  `json:"resources,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type Schedule struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	WorkflowID string `json:"workflowID"`
	Cron       string `json:"cron"`
	Timezone   string `json:"timezone"`
	Enabled    bool   `json:"enabled"`
	NextRunAt  string `json:"nextRunAt,omitempty"`
	LastRunAt  string `json:"lastRunAt,omitempty"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

type LoginStart struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId,omitempty"`
	AuthURL         string `json:"authUrl,omitempty"`
	VerificationURL string `json:"verificationUrl,omitempty"`
	UserCode        string `json:"userCode,omitempty"`
}

type AppCapabilities struct {
	ExperimentalAPI bool     `json:"experimentalApi"`
	Methods         []string `json:"methods,omitempty"`
}

type CodexNotification struct {
	SchemaVersion int            `json:"schemaVersion"`
	Timestamp     string         `json:"timestamp"`
	Sequence      int64          `json:"sequence"`
	Source        string         `json:"source"`
	Method        string         `json:"method"`
	Params        map[string]any `json:"params,omitempty"`
}

// BuilderActivityEvent is a deliberately summarized view of a planner or
// builder turn. It reports an operational phase, never raw notification
// parameters or private model reasoning.
type BuilderActivityEvent struct {
	SchemaVersion int    `json:"schemaVersion"`
	Timestamp     string `json:"timestamp"`
	Sequence      int64  `json:"sequence"`
	Source        string `json:"source"`
	Scope         string `json:"scope"`
	ThreadID      string `json:"threadID,omitempty"`
	TurnID        string `json:"turnID,omitempty"`
	State         string `json:"state"`
	Activity      string `json:"activity"`
	Detail        string `json:"detail,omitempty"`
}

type ModelsUpdatedEvent struct {
	SchemaVersion int         `json:"schemaVersion"`
	Timestamp     string      `json:"timestamp"`
	Sequence      int64       `json:"sequence"`
	Source        string      `json:"source"`
	Models        []ModelInfo `json:"models"`
}

type MCPStatusEvent struct {
	SchemaVersion int         `json:"schemaVersion"`
	Timestamp     string      `json:"timestamp"`
	Sequence      int64       `json:"sequence"`
	Source        string      `json:"source"`
	Servers       []MCPServer `json:"servers"`
}

type RunUpdateEvent struct {
	SchemaVersion int    `json:"schemaVersion"`
	Timestamp     string `json:"timestamp"`
	Sequence      int64  `json:"sequence"`
	Source        string `json:"source"`
	RunID         string `json:"runID"`
	Event         string `json:"event"`
	RunStatus     string `json:"runStatus,omitempty"`
}

type SchedulerEvent struct {
	SchemaVersion int    `json:"schemaVersion"`
	Timestamp     string `json:"timestamp"`
	Sequence      int64  `json:"sequence"`
	Source        string `json:"source"`
	Action        string `json:"action"`
	ScheduleID    string `json:"scheduleID"`
}
