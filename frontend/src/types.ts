export type JsonMap = Record<string, unknown>

export type AuthState = {
  status: string
  accountType?: string
  email?: string
  plan?: string
  requiresOpenAIAuth?: boolean
  rateLimit?: {
    limitId?: string
    limitName?: string
    usedPercent: number
    windowDurationMins: number
    resetsAt?: number
    rateLimitReachedType?: string
    secondary?: RateLimitWindow
    buckets?: RateLimitBucket[]
  }
  error?: string
  updatedAt: string
}

export type RateLimitWindow = {
  usedPercent: number
  windowDurationMins: number
  resetsAt?: number
}

export type RateLimitBucket = {
  limitId?: string
  limitName?: string
  primary?: RateLimitWindow
  secondary?: RateLimitWindow
  rateLimitReachedType?: string
}

export type ModelInfo = {
  id: string
  displayName: string
  description?: string
  defaultReasoningEffort?: string
  supportedReasoningEfforts?: { reasoningEffort: string; description?: string }[]
  inputModalities?: string[]
  supportsPersonality?: boolean
  isDefault?: boolean
}

export type SystemPrompt = {
  id: string
  name: string
  description: string
  template: string
  defaultTemplate: string
  variables: string[]
  isCustomized: boolean
  updatedAt?: string
}

export type AgentProfile = {
  id: string
  projectID?: string
  name: string
  role: string
  instructions: string
  modelID: string
  reasoningEffort: string
  workspaceRoots: string[]
  toolAllowlist: string[]
  approvalProfile: string
  roomID: string
  avatarID: string
  spriteID: string
  visualState: string
  maxDurationSeconds: number
  maxTurns: number
  maxAttempts: number
  memorySummary: string
  createdAt: string
  updatedAt: string
}

export type BuilderRequest = {
  prompt: string
  projectID?: string
  threadID?: string
  modelID?: string
  reasoningEffort?: string
  subagentApprovalProfile?: string
  handoff?: boolean
}

export type PlanningRequest = {
  prompt: string
  projectID?: string
  threadID?: string
  plannerAgentID?: string
  modelID?: string
  reasoningEffort?: string
}

export type PlanningResponse = {
  threadID: string
  reply: string
}

export type BuilderAgentDraft = {
  temporaryID: string
  name: string
  role: string
  instructions: string
  modelID?: string
  reasoningEffort?: string
  workspaceRoots?: string[]
  toolAllowlist?: string[]
  approvalProfile: string
  roomID?: string
  avatarID?: string
  maxDurationSeconds?: number
  maxTurns?: number
  maxAttempts?: number
}

export type BuilderProposal = {
  schemaVersion: number
  summary: string
  executionBrief?: string
  notes?: string[]
  agents: BuilderAgentDraft[]
  workflow?: WorkflowDefinition | null
}

export type BuilderResponse = {
  threadID: string
  reply: string
  proposal: BuilderProposal
}

export type BuilderActivityEvent = {
  schemaVersion: number
  timestamp: string
  sequence: number
  source: string
  scope: BuilderMode
  threadID?: string
  turnID?: string
  state: 'working' | 'completed' | 'error' | string
  activity: string
  detail?: string
}

export type BuilderMode = 'planning' | 'building'

export type BuilderStatus = {
  schemaVersion: number
  timestamp: string
  sequence: number
  source: string
  requestID: string
  projectID?: string
  scope: BuilderMode | string
  state: 'idle' | 'working' | 'completed' | 'error' | string
  activity: string
  detail?: string
  threadID?: string
  turnID?: string
  startedAt: string
  updatedAt: string
  error?: string
  planningResponse?: PlanningResponse
  builderResponse?: BuilderResponse
}

export type BuilderApplyRequest = {
  projectID?: string
  proposal: BuilderProposal
}

export type BuilderApplyResult = {
  agents: AgentProfile[]
  workflow?: WorkflowDefinition | null
}

export type Project = {
  id: string
  name: string
  description?: string
  folders: string[]
  manifestPath?: string
  createdAt: string
  updatedAt: string
  lastOpenedAt?: string
}

export type LearnSystemRequest = {
  projectID?: string
  modelID?: string
  reasoningEffort?: string
}

export type LearnSystemResult = {
  projectID: string
  status: string
  primaryFolder: string
  docsPath: string
  docsFiles: string[]
  missingFiles?: string[]
  modelID?: string
  reasoningEffort?: string
  threadID?: string
  summary?: string
  durationMs: number
}

export type RetryPolicy = {
  maxAttempts: number
  backoffSeconds: number
  idempotent: boolean
}

export type WorkflowNode = {
  id: string
  type: string
  label: string
  agentID?: string
  prompt?: string
  condition?: string
  toolName?: string
  artifactPath?: string
  maxIterations?: number
  timeoutSeconds?: number
  retry: RetryPolicy
  config?: JsonMap
}

export type WorkflowEdge = {
  id: string
  from: string
  to: string
  condition?: string
}

export type WorkflowDefinition = {
  id: string
  name: string
  version: number
  description?: string
  executionBrief?: string
  entryNodeID: string
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  globalLimits: {
    maxDurationSeconds: number
    maxParallel: number
    maxTurns: number
    maxPromptTokens: number
  }
  errorPolicy: string
  createdAt: string
  updatedAt: string
}

export type ValidationIssue = { code: string; path?: string; message: string }
export type WorkflowValidation = { valid: boolean; errors?: ValidationIssue[]; warnings?: ValidationIssue[] }

export type Run = {
  id: string
  projectID?: string
  workflowID: string
  status: string
  input?: JsonMap
  output?: JsonMap
  currentNodeID?: string
  startedAt: string
  updatedAt: string
  completedAt?: string
  error?: string
  cancelRequested?: boolean
  paused?: boolean
  promptTokensUsed?: number
  promptTokenBudget?: number
  outputBytes?: number
}

export type RunStep = {
  runID: string
  nodeID: string
  status: string
  attempt: number
  threadID?: string
  turnID?: string
  output?: JsonMap
  error?: string
  startedAt?: string
  completedAt?: string
}

export type AuditEntry = {
  id: string
  projectID?: string
  runID?: string
  kind: string
  actor: string
  target?: string
  decision?: string
  detail?: string
  metadata?: JsonMap
  createdAt: string
}

export type RuntimeStatus = {
  connected: boolean
  codexCommand: string
  appServerPID?: number
  activeSessions: number
  maxSessions: number
  activeRuns: number
  authStatus: string
  updatedAt: string
}

export type HistoryEntry = {
  id: string
  projectID?: string
  kind: string
  title: string
  content?: string
  metadata?: JsonMap
  createdAt: string
}

export type TerminalResult = {
  command: string
  workingDir: string
  output: string
  exitCode: number
  durationMs: number
  truncated: boolean
}

export type RunEvent = {
  schemaVersion: number
  timestamp: string
  sequence: number
  source: string
  runID: string
  type: string
  level: string
  nodeID?: string
  agentID?: string
  message: string
  data?: JsonMap
}

export type ApprovalRequest = {
  schemaVersion: number
  timestamp: string
  id: string
  kind: string
  title: string
  detail?: string
  runID?: string
  threadID?: string
  turnID?: string
  itemID?: string
  choices?: string[]
  expiresAt?: string
}

export type ApprovalDecision = { id: string; decision: string }

export type MCPServer = {
  id: string
  name: string
  status: string
  authStatus?: string
  tools?: { name: string; description?: string; requiresApproval?: boolean }[]
  resources?: string[]
  error?: string
}

export type Schedule = {
  id: string
  name: string
  workflowID: string
  cron: string
  timezone: string
  enabled: boolean
  nextRunAt?: string
  lastRunAt?: string
  createdAt: string
  updatedAt: string
}

export const defaultAgent: AgentProfile = {
  id: '',
  name: 'New agent',
  role: 'Operations specialist',
  instructions: 'Describe how this agent should work, which limits to respect, and how to report results.',
  modelID: '',
  reasoningEffort: '',
  workspaceRoots: [],
  toolAllowlist: [],
  approvalProfile: 'on_request',
  roomID: 'workshop',
  avatarID: 'operator',
  spriteID: '',
  visualState: 'idle',
  maxDurationSeconds: 1800,
  maxTurns: 12,
  maxAttempts: 2,
  memorySummary: '',
  createdAt: '',
  updatedAt: '',
}

// Agent data can come from older project manifests or from optional fields in
// a Builder proposal. Normalize it before rendering so the editor never has
// to trust nullable JSON arrays from the native bridge.
export function normalizeAgent(value: unknown): AgentProfile {
  const source = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const text = (key: string, fallback: string) => typeof source[key] === 'string' ? source[key] as string : fallback
  const list = (key: string) => Array.isArray(source[key])
    ? (source[key] as unknown[]).filter((item): item is string => typeof item === 'string')
    : []
  const number = (key: string, fallback: number) => typeof source[key] === 'number' && Number.isFinite(source[key]) ? source[key] as number : fallback

  return {
    ...defaultAgent,
    id: text('id', ''),
    projectID: text('projectID', '') || undefined,
    name: text('name', defaultAgent.name),
    role: text('role', defaultAgent.role),
    instructions: text('instructions', defaultAgent.instructions),
    modelID: text('modelID', ''),
    reasoningEffort: text('reasoningEffort', ''),
    workspaceRoots: list('workspaceRoots'),
    toolAllowlist: list('toolAllowlist'),
    approvalProfile: text('approvalProfile', defaultAgent.approvalProfile),
    roomID: text('roomID', defaultAgent.roomID),
    avatarID: text('avatarID', defaultAgent.avatarID),
    spriteID: text('spriteID', ''),
    visualState: text('visualState', defaultAgent.visualState),
    maxDurationSeconds: number('maxDurationSeconds', defaultAgent.maxDurationSeconds),
    maxTurns: number('maxTurns', defaultAgent.maxTurns),
    maxAttempts: number('maxAttempts', defaultAgent.maxAttempts),
    memorySummary: text('memorySummary', ''),
    createdAt: text('createdAt', ''),
    updatedAt: text('updatedAt', ''),
  }
}

export const defaultProject: Project = {
  id: '',
  name: 'New project',
  description: '',
  folders: [],
  createdAt: '',
  updatedAt: '',
}
