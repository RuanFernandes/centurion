import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Events, Window as WailsWindow } from '@wailsio/runtime'
import { api } from './api'
import type {
  AgentProfile,
	AuditEntry,
  ApprovalRequest,
  AuthState,
  BuilderActivityEvent,
  BuilderApplyResult,
  BuilderProposal,
  BuilderStatus,
  HistoryEntry,
  MCPServer,
  ModelInfo,
  Project,
	Run,
	RunEvent,
	RunStep,
	RuntimeStatus,
  Schedule,
  SystemPrompt,
  TerminalResult,
  WorkflowDefinition,
  WorkflowNode,
  WorkflowValidation,
} from './types'
import { defaultAgent, defaultProject, normalizeAgent } from './types'
import './styles.css'

type View = 'projects' | 'office' | 'workflows' | 'builder' | 'agents' | 'runs' | 'terminal' | 'history' | 'schedules' | 'mcp' | 'settings'

const navItems: { id: View; label: string; short: string }[] = [
  { id: 'projects', label: 'Projects', short: 'P' },
  { id: 'office', label: 'Office', short: '⌂' },
  { id: 'workflows', label: 'Workflow', short: '↗' },
  { id: 'builder', label: 'Build with Codex', short: '✦' },
  { id: 'agents', label: 'Agents', short: 'A' },
  { id: 'runs', label: 'Runs', short: '▶' },
  { id: 'terminal', label: 'Terminal', short: '>' },
  { id: 'history', label: 'History', short: 'H' },
  { id: 'schedules', label: 'Schedules', short: '◷' },
  { id: 'mcp', label: 'MCP', short: 'M' },
  { id: 'settings', label: 'Settings', short: '⚙' },
]

const stateLabels: Record<string, string> = {
  idle: 'Idle',
  working: 'Working',
  waiting_approval: 'Waiting for approval',
  blocked: 'Blocked',
  running_tool: 'Running tool',
  handoff: 'Handing off',
  success: 'Completed',
  error: 'Error',
}

const runLabels: Record<string, string> = {
  queued: 'Queued',
  running: 'Running',
  paused: 'Paused',
  waiting_approval: 'Waiting for approval',
  completed: 'Completed',
  blocked: 'Blocked',
  failed: 'Failed',
  canceled: 'Canceled',
  interrupted: 'Interrupted',
}

function eventValue<T>(value: unknown): T {
  const candidate = value as { data?: T }
  return candidate?.data ?? (value as T)
}

function errorText(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  if (error && typeof error === 'object' && 'message' in error) return String(error.message)
  return 'Could not complete the operation.'
}

function mergeRunSnapshot(current: Run[], incoming: Run): Run[] {
  const index = current.findIndex((run) => run.id === incoming.id)
  if (index < 0) return [incoming, ...current]
  const existing = current[index]
  const existingTime = Date.parse(existing.updatedAt)
  const incomingTime = Date.parse(incoming.updatedAt)
  if (existing.status !== 'queued' && incoming.status === 'queued') return current
  if (Number.isFinite(existingTime) && Number.isFinite(incomingTime) && existingTime > incomingTime) return current
  const next = [...current]
  next[index] = incoming
  return next
}

async function copyToClipboard(value: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    // Fall through to the legacy clipboard path for desktop webviews.
  }

  const textarea = document.createElement('textarea')
  textarea.value = value
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  try {
    textarea.select()
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    textarea.remove()
  }
}

function formatTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })
}

function formatUsageWindow(minutes?: number): string {
  if (!minutes || minutes <= 0) return 'quota'
  if (minutes % 1440 === 0) return `${minutes / 1440}d`
  if (minutes % 60 === 0) return `${minutes / 60}h`
  return `${minutes}m`
}

function formatResetCountdown(resetsAt: number | undefined, now: number): string {
  if (!resetsAt) return '—'
  const seconds = Math.max(0, resetsAt - Math.floor(now / 1000))
  if (seconds === 0) return 'resetting'
  const minutes = Math.ceil(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return remainingMinutes ? `${hours}h ${remainingMinutes}m` : `${hours}h`
}

function UsageMonitor({ auth }: { auth: AuthState }) {
  const [now, setNow] = useState(() => Date.now())
  const rateLimit = auth.rateLimit
  const hasUsageData = Boolean(rateLimit && (
    rateLimit.windowDurationMins > 0 ||
    rateLimit.resetsAt ||
    rateLimit.usedPercent > 0 ||
    rateLimit.rateLimitReachedType ||
    rateLimit.buckets?.some((bucket) => bucket.primary?.windowDurationMins || bucket.primary?.resetsAt || bucket.primary?.usedPercent)
  ))

  useEffect(() => {
    const interval = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(interval)
  }, [])

	if (!rateLimit || !hasUsageData) {
		const state = auth.status === 'checking' ? 'Checking Codex' : auth.status === 'logged_in' ? 'No data' : 'Codex offline'
    return <div className="usage-monitor usage-neutral" role="status" aria-label={`Codex usage: ${state}`} title={`Codex usage: ${state}`}><span className="usage-monitor-signal" aria-hidden="true" /><span className="usage-monitor-name">Usage</span><span className="usage-monitor-state">{state}</span></div>
  }

  const percentage = Math.max(0, Math.min(100, Math.round(rateLimit.usedPercent)))
  const reached = Boolean(rateLimit.rateLimitReachedType) || percentage >= 100 || Boolean(rateLimit.secondary && rateLimit.secondary.usedPercent >= 100) || Boolean(rateLimit.buckets?.some((bucket) => bucket.rateLimitReachedType || bucket.primary?.usedPercent === 100 || bucket.secondary?.usedPercent === 100))
  const tone = reached ? 'danger' : percentage >= 85 ? 'warning' : 'success'
  const reset = formatResetCountdown(rateLimit.resetsAt, now)
  const quotaWindow = formatUsageWindow(rateLimit.windowDurationMins)
  const bucketDetails = (rateLimit.buckets ?? []).map((bucket) => {
    const name = bucket.limitName || bucket.limitId || 'Codex'
    const primary = bucket.primary
    if (!primary) return name
    return `${name}: ${Math.round(primary.usedPercent)}% of ${formatUsageWindow(primary.windowDurationMins)}, resets in ${formatResetCountdown(primary.resetsAt, now)}`
  })
  const title = [`Codex usage: ${percentage}% of ${quotaWindow} window`, `resets in ${reset}`, ...bucketDetails].join(' · ')
  const status = reached ? 'Limit reached' : percentage >= 85 ? 'Near limit' : 'Available'

  return <div className={`usage-monitor usage-${tone}`} role="status" aria-label={`${title}. ${status}`} title={title}>
    <span className="usage-monitor-signal" aria-hidden="true" />
    <span className="usage-monitor-name">Usage</span>
    <span className="usage-monitor-window">{quotaWindow}</span>
    <span className="usage-monitor-track" aria-hidden="true"><span style={{ width: `${percentage}%` }} /></span>
    <strong>{percentage}%</strong>
    <span className="usage-monitor-reset">{reset}</span>
  </div>
}

function statusLabel(value: string): string {
  return runLabels[value] ?? value.split('_').join(' ')
}

function runTone(value: string): string {
  switch (value) {
    case 'completed':
      return 'success'
    case 'failed':
      return 'danger'
    case 'blocked':
      return 'warning'
    case 'running':
      return 'accent'
    default:
      return 'neutral'
  }
}

function readableLabel(value: string): string {
  return value
    .replace(/^_+/, '')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[_-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/^\w/, (character) => character.toUpperCase())
}

function parseJSONText(value: string, allowEmbedded = false): unknown | undefined {
  const trimmed = value.trim()
  const candidates = [trimmed]
  if (allowEmbedded && !trimmed.startsWith('{') && !trimmed.startsWith('[')) {
    const start = trimmed.indexOf('{')
    const end = trimmed.lastIndexOf('}')
    if (start >= 0 && end > start) candidates.push(trimmed.slice(start, end + 1))
  }
  for (const candidate of candidates) {
    try {
      return JSON.parse(candidate) as unknown
    } catch {
      // Keep normal prose and Markdown untouched.
    }
  }
  return undefined
}

function readableValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value).trim()
  if (Array.isArray(value)) {
    return value.map((item) => {
      const text = readableValue(item)
      return text ? `• ${text.replace(/\n/g, '\n  ')}` : ''
    }).filter(Boolean).join('\n')
  }
  if (typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>).map(([key, item]) => {
      const text = readableValue(item)
      return text ? `${readableLabel(key)}: ${text.replace(/\n/g, '\n  ')}` : ''
    }).filter(Boolean).join('\n')
  }
  return ''
}

function readableStructuredOutput(output: Record<string, unknown>): string {
  const primary = ['text', 'summary', 'message'].map((key) => output[key]).find((value): value is string => typeof value === 'string' && value.trim() !== '')
  if (primary) return readableUserText(primary).trim()

  const sections: string[] = []
  for (const key of ['result', 'decision', 'nextStep', 'reason']) {
    const text = readableValue(output[key])
    if (text) sections.push(`${readableLabel(key)}: ${text}`)
  }
  for (const key of ['blockers', 'changedPaths', 'files', 'artifacts', 'evidence']) {
    const text = readableValue(output[key])
    if (text) sections.push(`${readableLabel(key)}:\n${text}`)
  }
  if (sections.length > 0) return sections.join('\n\n').trim()

  if (typeof output.status === 'string' && output.status.trim() !== '') return `Status: ${statusLabel(output.status)}`
  if (typeof output._rawText === 'string' && output._rawText.trim() !== '') return readableUserText(output._rawText, true).trim()
  return readableValue(output).trim()
}

function readableUserText(value: string, allowEmbedded = false): string {
  const parsed = parseJSONText(value, allowEmbedded)
  if (parsed === undefined) return value
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return readableStructuredOutput(parsed as Record<string, unknown>)
  return readableValue(parsed)
}

function Icon({ glyph }: { glyph: string }) {
  const common = { viewBox: '0 0 24 24', width: 16, height: 16, fill: 'none', stroke: 'currentColor', strokeWidth: 1.7, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  const icon = glyph === 'P' ? <><path d="M6 20V4h7a4 4 0 0 1 0 8H6" /><path d="M10 16h8" /></>
    : glyph === '⌂' ? <><path d="m3 10 9-7 9 7" /><path d="M5 9.5V21h14V9.5" /><path d="M9 21v-6h6v6" /></>
      : glyph === '↗' ? <><path d="M5 19 19 5" /><path d="M9 5h10v10" /></>
        : glyph === 'A' ? <><circle cx="12" cy="8" r="3" /><path d="M5.5 20a6.5 6.5 0 0 1 13 0" /></>
          : glyph === '▶' ? <path d="m8 5 10 7-10 7V5Z" />
            : glyph === '>' ? <><path d="m7 5 6 7-6 7" /><path d="M15 19h3" /></>
              : glyph === 'H' ? <><path d="M6 5v14" /><path d="M18 5v14" /><path d="M6 12h12" /></>
                : glyph === '◷' ? <><circle cx="12" cy="12" r="8.5" /><path d="M12 7v5l3 2" /></>
                : glyph === 'M' ? <><path d="M7 8v8" /><path d="M17 8v8" /><path d="m7 12 5-5 5 5" /><path d="M4 19h16" /></>
                  : glyph === '✦' ? <><path d="m12 3 1.5 7.5L21 12l-7.5 1.5L12 21l-1.5-7.5L3 12l7.5-1.5L12 3Z" /><path d="m19 4 .4 1.6L21 6l-1.6.4L19 8l-.4-1.6L17 6l1.6-.4L19 4Z" /></>
                    : <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-1.7 1.7-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.1h-2.4v-.1a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L8 17l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.6-1H6.7v-2.4h.1a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L8 8.6l1.7-1.7.1.1a1.7 1.7 0 0 0 1.9.3 1.7 1.7 0 0 0 1-1.6v-.1h2.4v.1a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1 1.7 1.7-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.1V14h-.1a1.7 1.7 0 0 0-1.6 1Z" /></>
  return <span className="nav-glyph" aria-hidden="true"><svg {...common}>{icon}</svg></span>
}

function StatusPill({ value, tone = 'neutral' }: { value: string; tone?: string }) {
  return <span className={`status-pill ${tone}`}><span className="status-dot" />{value}</span>
}

type MCPStateMeta = {
  label: string
  tone: 'success' | 'warning' | 'danger' | 'neutral'
  action: 'oauth' | 'ready' | 'none'
  detail: string
}

function normalizeMCPValue(value?: string): string {
  return (value ?? '').replace(/([a-z])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').trim().toLowerCase()
}

function readableMCPValue(value?: string): string {
  const normalized = (value ?? '').replace(/([a-z])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').trim()
  if (!normalized) return 'Not reported'
  return normalized.charAt(0).toUpperCase() + normalized.slice(1)
}

function getMCPState(server: MCPServer, toolCount: number): MCPStateMeta {
  const status = normalizeMCPValue(server.status)
  const authStatus = normalizeMCPValue(server.authStatus)
  const requiresLogin = status === 'needs auth' || status === 'not logged in' || ['not logged in', 'needs authentication', 'authentication required', 'unauthorized'].includes(authStatus)

  if (status === 'ready' || status === 'connected') {
    return { label: 'Ready', tone: 'success', action: 'ready', detail: toolCount ? 'Connected and ready for workflows.' : 'Connected; no tools reported.' }
  }
  if (status === 'failed' || status === 'error' || status === 'unavailable') {
    return { label: 'Error', tone: 'danger', action: 'none', detail: 'Review the server error before running.' }
  }
  if (status === 'unsupported') {
    return { label: 'Unsupported', tone: 'neutral', action: 'none', detail: 'This server does not support OAuth here.' }
  }
  if (requiresLogin) {
    return { label: 'Needs sign-in', tone: 'warning', action: 'oauth', detail: 'Sign in to make this server available.' }
  }
  if (status === 'configured') {
    return { label: 'Configured', tone: 'warning', action: 'none', detail: 'Configuration loaded; no sign-in action reported.' }
  }
  return { label: readableMCPValue(server.status || 'Unknown'), tone: 'neutral', action: 'none', detail: 'Server status needs review.' }
}

function MCPCard({ server, onLogin }: { server: MCPServer; onLogin: (server: MCPServer) => void }) {
  const tools = server.tools ?? []
  const resources = server.resources?.length ?? 0
  const visibleTools = tools.slice(0, 6)
  const hiddenToolCount = Math.max(0, tools.length - visibleTools.length)
  const state = getMCPState(server, tools.length)

  return (
    <article className={`panel-card mcp-card mcp-tone-${state.tone}`}>
      <div className="mcp-card-header">
        <div className="mcp-card-identity">
          <span className="mcp-server-id">{server.id}</span>
          <h2>{server.name || server.id}</h2>
        </div>
        <StatusPill value={state.label} tone={state.tone} />
      </div>
      <div className="mcp-card-meta" aria-label={`${server.name || server.id} details`}>
        <span><span className="mcp-meta-label">Auth</span>{readableMCPValue(server.authStatus)}</span>
        <span><span className="mcp-meta-label">Tools</span>{tools.length}</span>
        {resources > 0 && <span><span className="mcp-meta-label">Resources</span>{resources}</span>}
      </div>
      <div className="mcp-tools-section">
        <div className="mcp-section-heading"><span>Available tools</span><span>{tools.length ? `${tools.length} total` : 'None reported'}</span></div>
        {tools.length > 0 ? <div className="tool-list" aria-label={`${tools.length} available tools`}>
          {visibleTools.map((tool) => <span key={tool.name} className="tool-chip" title={tool.description || tool.name}>{tool.name}</span>)}
          {hiddenToolCount > 0 && <span className="tool-chip tool-chip-more">+{hiddenToolCount} more</span>}
        </div> : <p className="mcp-empty-tools">No tools reported by this server.</p>}
      </div>
      {server.error && <p className="error-copy">{server.error}</p>}
      <div className="mcp-card-footer">
        <span className={`mcp-state-note mcp-state-note-${state.tone}`}>{state.detail}</span>
        {state.action === 'oauth' && <button className="link-button" onClick={() => onLogin(server)} aria-label={`Connect ${server.name || server.id} account`}>Connect account →</button>}
        {state.action === 'ready' && <span className="mcp-action-muted">No action needed</span>}
      </div>
    </article>
  )
}

function AgentAvatar({ agent, compact = false }: { agent: AgentProfile; compact?: boolean }) {
  const name = typeof agent.name === 'string' && agent.name.trim() ? agent.name : 'Agent'
  const initials = name.trim().split(/\s+/).map((part) => part[0]).join('').slice(0, 2).toUpperCase() || 'AG'
  const state = agent.visualState || 'idle'
  const spriteValue = /^sprite-(\d+)$/.exec(agent.spriteID ?? '')
  const spriteSeed = agent.id || agent.avatarID || agent.name
  const spriteIndex = spriteValue ? Number(spriteValue[1]) % 8 : [...spriteSeed].reduce((hash, character) => (hash * 31 + character.charCodeAt(0)) >>> 0, 7) % 8
  return (
    <div className={`agent-avatar ${compact ? 'compact' : ''} state-${state}`} aria-label={`${name}: ${stateLabels[state] ?? state}`}>
      <div className="avatar-aura" />
      <div className={`avatar-sprite sprite-${spriteIndex}`} role="img" aria-label={`${name} avatar`}><span className="avatar-fallback-initials">{initials}</span></div>
      <div className="avatar-status" aria-hidden="true"><span /></div>
    </div>
  )
}

const agentPresets = [
  { id: 'supervisor', label: 'Supervisor', detail: 'Coordinates decisions and routes work.', role: 'Operations supervisor', instructions: 'Coordinate context, split work into clear steps, and return a verifiable decision.', tools: ['files.read', 'git.diff'], room: 'strategy', avatar: 'supervisor' },
  { id: 'builder', label: 'Builder', detail: 'Changes code and validates the result.', role: 'Software implementer', instructions: 'Implement the task inside the allowed workspace. Make small changes, run tests, and report risks.', tools: ['files.read', 'files.write', 'shell.test', 'git.diff'], room: 'workshop', avatar: 'builder' },
  { id: 'researcher', label: 'Researcher', detail: 'Finds context and summarizes evidence.', role: 'Researcher', instructions: 'Collect relevant context, separate facts from assumptions, and return a concise synthesis with sources or next steps.', tools: ['files.read', 'web.search'], room: 'library', avatar: 'researcher' },
  { id: 'reviewer', label: 'Reviewer', detail: 'Looks for issues before delivery.', role: 'Quality reviewer', instructions: 'Review the result for bugs, security risks, and missing tests. Be specific and prioritize findings.', tools: ['files.read', 'git.diff', 'shell.test'], room: 'strategy', avatar: 'reviewer' },
] as const

function inferAgentPreset(agent: AgentProfile): typeof agentPresets[number]['id'] {
  const role = `${agent.role ?? ''} ${agent.name ?? ''}`.toLowerCase()
  const instructions = (agent.instructions ?? '').toLowerCase()
  const avatar = (agent.avatarID ?? '').toLowerCase()
  const text = `${role} ${instructions}`

  if (/research|researcher|analys|evidence|discover|investigat|market/.test(text)) return 'researcher'
  if (/review|reviewer|quality|qa\b|audit|security|test/.test(text)) return 'reviewer'
  if (/supervis|tech lead|team lead|architect|orchestrat|coordinate|planner/.test(text)) return 'supervisor'
  if (avatar === 'supervisor' || avatar === 'researcher' || avatar === 'reviewer' || avatar === 'builder') return avatar
  return 'builder'
}

const permissionOptions = [
  { id: 'files.read', label: 'Read files', detail: 'Inspect files inside the allowed workspace.' },
  { id: 'files.write', label: 'Write files', detail: 'Create or modify files inside the workspace.' },
  { id: 'shell.test', label: 'Run tests', detail: 'Run test and validation commands.' },
  { id: 'git.diff', label: 'Inspect Git diff', detail: 'Review changes tracked by Git.' },
  { id: 'web.search', label: 'Search the web', detail: 'Use web search when the integration is available.' },
] as const

const workflowNodeMeta: Record<string, { label: string; mark: string; detail: string }> = {
  agent: { label: 'Agent', mark: 'A', detail: 'Ask an agent to do the work.' },
  condition: { label: 'Condition', mark: 'IF', detail: 'Route by a declarative result.' },
  parallel: { label: 'Parallel', mark: '||', detail: 'Run branches at the same time.' },
  join: { label: 'Join', mark: '+', detail: 'Wait for parallel branches.' },
  loop: { label: 'Loop', mark: '↻', detail: 'Repeat with a hard iteration limit.' },
  approval: { label: 'Approval', mark: '!', detail: 'Pause until you approve an action.' },
  tool: { label: 'Tool', mark: 'T', detail: 'Call a configured tool.' },
  artifact: { label: 'Artifact', mark: '□', detail: 'Record an output file or result.' },
}

const workflowBlockTypes = Object.entries(workflowNodeMeta).map(([type, meta]) => ({ type, ...meta }))

function PermissionPicker({ value, onChange }: { value: string[]; onChange: (value: string[]) => void }) {
  const selectedPermissions = Array.isArray(value) ? value : []
  const knownPermissions = new Set<string>(permissionOptions.map((permission) => permission.id))
  const customPermissions = selectedPermissions.filter((permission) => !knownPermissions.has(permission))
  const toggle = (permissionID: string) => {
    const next = new Set(selectedPermissions)
    if (next.has(permissionID)) next.delete(permissionID)
    else next.add(permissionID)
    onChange([...next])
  }
  const renderOption = (permissionID: string, label: string, detail: string) => {
    const selected = selectedPermissions.includes(permissionID)
    return <label className={`permission-option ${selected ? 'selected' : ''}`} key={permissionID}><input type="checkbox" checked={selected} onChange={() => toggle(permissionID)} /><span className="permission-mark" aria-hidden="true">✓</span><span className="permission-copy"><strong>{label}</strong><small>{detail}</small></span></label>
  }
  return <div className="permission-picker"><div className="permission-list" role="group" aria-label="Agent tools and permissions">{permissionOptions.map((permission) => renderOption(permission.id, permission.label, permission.detail))}{customPermissions.map((permission) => renderOption(permission, permission, 'Custom permission preserved from this agent.'))}</div><span className="field-hint">{selectedPermissions.length} {selectedPermissions.length === 1 ? 'permission' : 'permissions'} selected</span></div>
}

function AgentEditor({
  agent,
  models,
  onChange,
  onSave,
  onDelete,
  busy,
}: {
  agent: AgentProfile
  models: ModelInfo[]
  onChange: (agent: AgentProfile) => void
  onSave: () => void
  onDelete?: () => void
  busy: boolean
}) {
  const [presetID, setPresetID] = useState(() => inferAgentPreset(agent))
  const selectedModel = models.find((model) => model.id === agent.modelID)
  const efforts = selectedModel?.supportedReasoningEfforts ?? []
  const workspaceRoots = Array.isArray(agent.workspaceRoots) ? agent.workspaceRoots : []
  const toolAllowlist = Array.isArray(agent.toolAllowlist) ? agent.toolAllowlist : []
  const update = <K extends keyof AgentProfile>(key: K, value: AgentProfile[K]) => onChange({ ...agent, [key]: value })
  const accessMode = agent.approvalProfile === 'autonomous' || agent.approvalProfile === 'trusted' ? 'complete' : 'approval'
  const applyPreset = (preset: typeof agentPresets[number]) => {
    setPresetID(preset.id)
    onChange({ ...agent, role: preset.role, instructions: preset.instructions, toolAllowlist: [...preset.tools], roomID: preset.room, avatarID: preset.avatar })
  }
  return (
    <div className="editor-stack">
      <div className="editor-heading"><div><span className="section-kicker">Agent</span><h2>{agent.id ? agent.name : 'New agent'}</h2></div><span className="mini-code">{agent.id ? agent.id.slice(0, 8) : 'draft'}</span></div>
      <div><span className="field-heading">Start with a preset</span><div className="preset-grid" role="radiogroup" aria-label="Agent presets">{agentPresets.map((preset) => <button type="button" key={preset.id} className={`preset-choice ${presetID === preset.id ? 'selected' : ''}`} onClick={() => applyPreset(preset)}><strong>{preset.label}</strong><span>{preset.detail}</span></button>)}</div></div>
      <div className="field-grid">
        <label className="field"><span>Name</span><input value={agent.name} onChange={(event) => update('name', event.target.value)} placeholder="e.g. Lara" /></label>
        <label className="field"><span>Role</span><input value={agent.role} onChange={(event) => update('role', event.target.value)} placeholder="e.g. Implementer" /></label>
      </div>
      <label className="field"><span>Model</span><select value={agent.modelID} onChange={(event) => update('modelID', event.target.value)}><option value="">Default available model</option>{models.map((model) => <option key={model.id} value={model.id}>{model.displayName}</option>)}</select></label>
      <div><span className="field-heading">Agent access</span><div className="access-picker" role="radiogroup" aria-label="Access level"><button type="button" className={accessMode === 'complete' ? 'active' : ''} onClick={() => update('approvalProfile', 'autonomous')}><strong>Full access</strong><span>Runs inside the workspace without intermediate approval.</span></button><button type="button" className={accessMode === 'approval' ? 'active' : ''} onClick={() => update('approvalProfile', 'on_request')}><strong>Request approval</strong><span>Pauses before commands, file changes, or external effects.</span></button></div></div>
      <details className="advanced-options"><summary>Advanced options</summary><div className="advanced-body"><label className="field"><span>Instructions</span><textarea rows={4} value={agent.instructions} onChange={(event) => update('instructions', event.target.value)} /></label><label className="field"><span>Allowed workspace</span><textarea rows={2} placeholder="One absolute path per line" value={workspaceRoots.join('\n')} onChange={(event) => update('workspaceRoots', event.target.value.split(/\r?\n/).map((value) => value.trim()).filter(Boolean))} /></label><div className="field"><span>Tools & permissions</span><PermissionPicker value={toolAllowlist} onChange={(value) => update('toolAllowlist', value)} /></div><div className="field-grid"><label className="field"><span>Effort</span><select value={agent.reasoningEffort} onChange={(event) => update('reasoningEffort', event.target.value)}><option value="">Automatic</option>{efforts.map((effort) => <option key={effort.reasoningEffort} value={effort.reasoningEffort}>{effort.reasoningEffort}</option>)}</select></label><label className="field compact-field"><span>Max turns</span><input type="number" min={1} value={agent.maxTurns} onChange={(event) => update('maxTurns', Number(event.target.value))} /></label></div></div></details>
      <div className="editor-actions"><button className="button primary" onClick={onSave} disabled={busy}>{busy ? 'Saving…' : 'Save agent'}</button>{onDelete && <button className="button danger-quiet" onClick={onDelete} disabled={busy}>Delete agent</button>}</div>
    </div>
  )
}

function ScheduleEditor({
  schedule,
  workflows,
  onChange,
  onSave,
  onDelete,
  busy,
}: {
  schedule: Schedule
  workflows: WorkflowDefinition[]
  onChange: (schedule: Schedule) => void
  onSave: () => void
  onDelete?: () => void
  busy: boolean
}) {
  const update = <K extends keyof Schedule>(key: K, value: Schedule[K]) => onChange({ ...schedule, [key]: value })
  return (
    <div className="editor-stack">
      <div className="editor-heading"><div><span className="section-kicker">Windows scheduler</span><h2>{schedule.id ? schedule.name : 'New schedule'}</h2></div><span className="mini-code">{schedule.id ? schedule.id.slice(0, 8) : 'draft'}</span></div>
      <label className="field"><span>Name</span><input value={schedule.name} onChange={(event) => update('name', event.target.value)} /></label>
      <label className="field"><span>Workflow</span><select value={schedule.workflowID} onChange={(event) => update('workflowID', event.target.value)}><option value="">Select a workflow</option>{workflows.map((workflow) => <option key={workflow.id} value={workflow.id}>{workflow.name}</option>)}</select></label>
      <div className="field-grid">
        <label className="field"><span>Supported cron</span><input value={schedule.cron} placeholder="*/30 * * * *" onChange={(event) => update('cron', event.target.value)} /></label>
        <label className="field"><span>Timezone</span><input value={schedule.timezone} placeholder="Local" onChange={(event) => update('timezone', event.target.value)} /></label>
      </div>
      <label className="toggle-field"><input type="checkbox" checked={schedule.enabled} onChange={(event) => update('enabled', event.target.checked)} /><span><strong>Schedule enabled</strong><small>The app will be awakened by Task Scheduler when needed.</small></span></label>
      <div className="schedule-format-note"><strong>Supported in this release</strong><span><code>*/N * * * *</code> for minute intervals or <code>M H * * *</code> for a daily run.</span></div>
      <div className="editor-actions"><button className="button primary" onClick={onSave} disabled={busy}>{busy ? 'Saving…' : 'Save schedule'}</button>{onDelete && <button className="button danger-quiet" onClick={onDelete} disabled={busy}>Delete</button>}</div>
    </div>
  )
}

function WorkflowCanvas({
  workflow,
  agents,
  onSave,
  onImport,
  onStart,
  onDelete,
  busy,
  onNotice,
}: {
  workflow: WorkflowDefinition
  agents: AgentProfile[]
  onSave: (workflow: WorkflowDefinition) => void
  onImport: (workflow: WorkflowDefinition) => void
  onStart: (workflow: WorkflowDefinition) => void
  onDelete?: () => void
  busy: boolean
  onNotice: (message: string) => void
}) {
  const canvasRef = useRef<HTMLDivElement | null>(null)
  const fileRef = useRef<HTMLInputElement | null>(null)
  const paletteRef = useRef<HTMLDivElement | null>(null)
  const draftRef = useRef(workflow)
  const validationSequence = useRef(0)
  const [selectedNodeID, setSelectedNodeID] = useState(workflow.entryNodeID || workflow.nodes[0]?.id || '')
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>({})
  const [draft, setDraft] = useState(workflow)
  const [connectingFrom, setConnectingFrom] = useState<string | null>(null)
  const [connectionTarget, setConnectionTarget] = useState<string | null>(null)
  const [connectionPreview, setConnectionPreview] = useState<{ x: number; y: number } | null>(null)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [history, setHistory] = useState<{ past: WorkflowDefinition[]; future: WorkflowDefinition[] }>({ past: [], future: [] })
  const [validation, setValidation] = useState<WorkflowValidation | null>(null)
  const [validationError, setValidationError] = useState('')
  const [validating, setValidating] = useState(false)
  const dragging = useRef<{ id: string; dx: number; dy: number } | null>(null)
  const connectionDrag = useRef<{ pointerId: number; from: string } | null>(null)
  const selectedNode = draft.nodes.find((node) => node.id === selectedNodeID)
  const selectedNodeMeta = workflowNodeMeta[selectedNode?.type ?? ''] ?? { label: selectedNode?.type ?? 'Step', mark: '•', detail: 'Configure this workflow step.' }
  const selectedAgent = selectedNode?.agentID ? agents.find((agent) => agent.id === selectedNode.agentID) : undefined
  const surfaceSize = useMemo(() => {
    const points = Object.values(positions)
    return {
      width: Math.max(900, ...points.map((point) => point.x + 260)),
      height: Math.max(530, ...points.map((point) => point.y + 132)),
    }
  }, [positions])

  useEffect(() => {
    draftRef.current = workflow
    setDraft(workflow)
    const next: Record<string, { x: number; y: number }> = {}
    workflow.nodes.forEach((node, index) => {
      next[node.id] = { x: 32 + (index % 3) * 228, y: 32 + Math.floor(index / 3) * 142 }
    })
    setPositions(next)
    setSelectedNodeID(workflow.entryNodeID || workflow.nodes[0]?.id || '')
    setHistory({ past: [], future: [] })
    setValidation(null)
    setValidationError('')
    setPaletteOpen(false)
  }, [workflow.id, workflow.version, workflow.nodes.length, workflow.edges.length, workflow.entryNodeID, workflow.updatedAt])

  useEffect(() => {
    if (!paletteOpen) return undefined
    const closePalette = (event: PointerEvent) => {
      if (!paletteRef.current?.contains(event.target as Node)) setPaletteOpen(false)
    }
    document.addEventListener('pointerdown', closePalette)
    return () => document.removeEventListener('pointerdown', closePalette)
  }, [paletteOpen])

  useEffect(() => {
    const timer = window.setTimeout(() => { void validateDraft() }, 450)
    return () => window.clearTimeout(timer)
  }, [draft])

  const commitDraft = (updater: (current: WorkflowDefinition) => WorkflowDefinition) => {
    const current = draftRef.current
    const next = updater(current)
    if (next === current) return
    draftRef.current = next
    setDraft(next)
    setHistory((currentHistory) => ({ past: [...currentHistory.past, current].slice(-40), future: [] }))
    setValidation(null)
    setValidationError('')
  }

  const undo = () => {
    const previous = history.past[history.past.length - 1]
    if (!previous) return
    const current = draftRef.current
    draftRef.current = previous
    setDraft(previous)
    setHistory({ past: history.past.slice(0, -1), future: [current, ...history.future].slice(0, 40) })
    setSelectedNodeID(previous.nodes.find((node) => node.id === selectedNodeID)?.id ?? previous.entryNodeID ?? previous.nodes[0]?.id ?? '')
    setValidation(null)
  }

  const redo = () => {
    const next = history.future[0]
    if (!next) return
    const current = draftRef.current
    draftRef.current = next
    setDraft(next)
    setHistory({ past: [...history.past, current].slice(-40), future: history.future.slice(1) })
    setSelectedNodeID(next.nodes.find((node) => node.id === selectedNodeID)?.id ?? next.entryNodeID ?? next.nodes[0]?.id ?? '')
    setValidation(null)
  }

  const validateDraft = async (): Promise<WorkflowValidation | null> => {
    const requestID = ++validationSequence.current
    setValidating(true)
    try {
      const result = await api.validateWorkflow(draftRef.current)
      if (requestID === validationSequence.current) {
        setValidation(result)
        setValidationError('')
      }
      return result
    } catch (error) {
      if (requestID === validationSequence.current) {
        setValidation(null)
        setValidationError(errorText(error))
      }
      return null
    } finally {
      if (requestID === validationSequence.current) setValidating(false)
    }
  }

  const point = (nodeID: string) => positions[nodeID] ?? { x: 32, y: 32 }
  const connectionPoint = (nodeID: string, side: 'input' | 'output') => { const value = point(nodeID); return { x: value.x + (side === 'output' ? 184 : 0), y: value.y + 42 } }
  const startDrag = (event: React.PointerEvent<HTMLDivElement>, nodeID: string) => {
    if (event.button !== 0 || connectingFrom) return
    const rect = canvasRef.current?.getBoundingClientRect()
    if (!rect) return
    const value = point(nodeID)
    dragging.current = { id: nodeID, dx: event.clientX - rect.left - value.x, dy: event.clientY - rect.top - value.y }
    event.currentTarget.setPointerCapture(event.pointerId)
    setSelectedNodeID(nodeID)
  }
  const moveDrag = (event: React.PointerEvent<HTMLDivElement>) => {
    const active = dragging.current
    const rect = canvasRef.current?.getBoundingClientRect()
    if (!active || !rect) return
    setPositions((current) => ({ ...current, [active.id]: { x: Math.max(12, event.clientX - rect.left - active.dx), y: Math.max(12, event.clientY - rect.top - active.dy) } }))
  }
  const stopDrag = () => { dragging.current = null }
  const surfacePoint = (clientX: number, clientY: number) => {
    const rect = canvasRef.current?.getBoundingClientRect()
    if (!rect) return null
    return {
      x: Math.max(0, Math.min(surfaceSize.width, clientX - rect.left)),
      y: Math.max(0, Math.min(surfaceSize.height, clientY - rect.top)),
    }
  }
  const inputAtPoint = (clientX: number, clientY: number) => {
    const element = document.elementFromPoint(clientX, clientY)
    const input = element?.closest('[data-node-input]') as HTMLElement | null
    return input?.dataset.nodeInput ?? null
  }
  const clearConnection = () => {
    const active = connectionDrag.current
    if (active && canvasRef.current?.hasPointerCapture(active.pointerId)) canvasRef.current.releasePointerCapture(active.pointerId)
    connectionDrag.current = null
    setConnectingFrom(null)
    setConnectionTarget(null)
    setConnectionPreview(null)
  }
  const finishConnection = (nodeID: string) => {
    if (!connectingFrom || connectingFrom === nodeID) return
    if (draftRef.current.edges.some((edge) => edge.from === connectingFrom && edge.to === nodeID)) {
      onNotice('This connection already exists.')
      clearConnection()
      return
    }
    commitDraft((current) => ({ ...current, edges: [...current.edges, { id: `edge-${Date.now().toString(36)}`, from: connectingFrom, to: nodeID }] }))
    clearConnection()
    onNotice('Connection created.')
  }
  const autoConnectWorkflow = () => {
    const current = draftRef.current
    if (current.nodes.length < 2 || current.edges.length > 0) return
    const edges = current.nodes.slice(0, -1).map((node, index) => ({
      id: `edge-auto-${Date.now().toString(36)}-${index + 1}`,
      from: node.id,
      to: current.nodes[index + 1].id,
    }))
    commitDraft((value) => ({ ...value, edges }))
    onNotice('Steps connected in their declared order. Review the route before running.')
  }
  const startConnectionDrag = (event: React.PointerEvent<HTMLButtonElement>, nodeID: string) => {
    if (event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    connectionDrag.current = { pointerId: event.pointerId, from: nodeID }
    setConnectingFrom(nodeID)
    setConnectionTarget(null)
    setConnectionPreview(connectionPoint(nodeID, 'output'))
    setSelectedNodeID(nodeID)
    canvasRef.current?.setPointerCapture(event.pointerId)
  }
  const startKeyboardConnection = (event: React.KeyboardEvent<HTMLButtonElement>, nodeID: string) => {
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    event.stopPropagation()
    setConnectingFrom(nodeID)
    setConnectionTarget(null)
    setConnectionPreview(null)
    setSelectedNodeID(nodeID)
    onNotice(`Connection started from ${draft.nodes.find((node) => node.id === nodeID)?.label ?? nodeID}. Select an input handle.`)
  }
  const moveConnection = (event: React.PointerEvent<HTMLDivElement>) => {
    const active = connectionDrag.current
    if (!active) return moveDrag(event)
    setConnectionPreview(surfacePoint(event.clientX, event.clientY))
    setConnectionTarget(inputAtPoint(event.clientX, event.clientY))
  }
  const endConnection = (event: React.PointerEvent<HTMLDivElement>) => {
    const active = connectionDrag.current
    if (!active) return stopDrag()
    const target = inputAtPoint(event.clientX, event.clientY)
    if (target && target !== active.from) finishConnection(target)
    else {
      clearConnection()
      onNotice(target === active.from ? 'A step cannot connect to itself.' : 'Connection canceled. Drop on an input handle.')
    }
  }
  const handleNodeClick = (nodeID: string) => {
    setSelectedNodeID(nodeID)
  }
  const removeEdge = (edgeID: string) => commitDraft((current) => ({ ...current, edges: current.edges.filter((edge) => edge.id !== edgeID) }))
  const updateEdgeCondition = (edgeID: string, condition: string) => commitDraft((current) => ({ ...current, edges: current.edges.map((edge) => edge.id === edgeID ? { ...edge, condition: condition.trim() || undefined } : edge) }))
  const exportJSON = async () => {
    const text = JSON.stringify(draftRef.current, null, 2)
    try {
      await navigator.clipboard?.writeText(text)
      const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `${draftRef.current.name.toLowerCase().replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '') || 'workflow'}.json`
      anchor.click()
      URL.revokeObjectURL(url)
      onNotice('JSON copied and exported for download.')
    } catch { onNotice('JSON ready; the clipboard is not available.') }
  }
  const importJSON = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      try { onImport(JSON.parse(String(reader.result)) as WorkflowDefinition) } catch { onNotice('The file does not contain valid workflow JSON.') }
    }
    reader.readAsText(file)
    event.target.value = ''
  }

  const updateNode = (changes: Partial<WorkflowNode>) => {
    if (!selectedNode) return
    commitDraft((current) => ({ ...current, nodes: current.nodes.map((node) => node.id === selectedNode.id ? { ...node, ...changes } : node) }))
  }
  const createNode = (type: string, id: string): WorkflowNode => {
    const meta = workflowNodeMeta[type] ?? { label: 'Step', mark: '•', detail: 'Configure this workflow step.' }
    const node: WorkflowNode = { id, type, label: type === 'agent' ? 'New agent step' : `New ${meta.label.toLowerCase()} step`, retry: { maxAttempts: 1, backoffSeconds: 0, idempotent: true } }
    if (type === 'agent') node.agentID = agents[0]?.id ?? ''
    if (type === 'condition') node.condition = 'always'
    if (type === 'loop') node.maxIterations = 3
    if (type === 'tool') node.toolName = ''
    if (type === 'artifact') node.artifactPath = ''
    return node
  }

  const addNode = (type: string) => {
    const current = draftRef.current
    const id = `node-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
    const source = selectedNode?.id ?? current.nodes[current.nodes.length - 1]?.id
    const node = createNode(type, id)
    const sourcePoint = source ? positions[source] : undefined
    let position = sourcePoint
      ? { x: sourcePoint.x + 260, y: sourcePoint.y }
      : { x: 32 + (current.nodes.length % 3) * 228, y: 32 + Math.floor(current.nodes.length / 3) * 142 }
    const occupied = Object.values(positions)
    while (occupied.some((item) => Math.abs(item.x - position.x) < 190 && Math.abs(item.y - position.y) < 120)) position = { x: position.x, y: position.y + 148 }
    commitDraft((value) => ({ ...value, nodes: [...value.nodes, node], edges: source ? [...value.edges, { id: `edge-${Date.now().toString(36)}`, from: source, to: id }] : value.edges, entryNodeID: value.entryNodeID || id }))
    setPositions((currentPositions) => ({ ...currentPositions, [id]: position }))
    setSelectedNodeID(id)
    setPaletteOpen(false)
    onNotice(`${workflowNodeMeta[type]?.label ?? 'Step'} added.`)
  }

  const duplicateNode = () => {
    if (!selectedNode) return
    const id = `node-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
    const copy: WorkflowNode = { ...selectedNode, id, label: `${selectedNode.label || 'Step'} copy`, config: selectedNode.config ? { ...selectedNode.config } : undefined }
    const sourcePoint = positions[selectedNode.id] ?? { x: 32, y: 32 }
    const position = { x: sourcePoint.x + 36, y: sourcePoint.y + 148 }
    commitDraft((value) => ({ ...value, nodes: [...value.nodes, copy] }))
    setPositions((currentPositions) => ({ ...currentPositions, [id]: position }))
    setSelectedNodeID(id)
    onNotice('Step duplicated. Connect it when ready.')
  }

  const removeSelectedNode = () => {
    if (!selectedNode) return
    const current = draftRef.current
    if (current.nodes.length <= 1) {
      onNotice('A workflow needs at least one step.')
      return
    }
    const nextNodes = current.nodes.filter((node) => node.id !== selectedNode.id)
    const nextEntry = current.entryNodeID === selectedNode.id ? nextNodes[0]?.id ?? '' : current.entryNodeID
    commitDraft((value) => ({ ...value, nodes: nextNodes, edges: value.edges.filter((edge) => edge.from !== selectedNode.id && edge.to !== selectedNode.id), entryNodeID: nextEntry }))
    setPositions((currentPositions) => {
      const next = { ...currentPositions }
      delete next[selectedNode.id]
      return next
    })
    setSelectedNodeID(nextEntry)
    onNotice('Step removed.')
  }

  const setEntryNode = () => {
    if (!selectedNode || selectedNode.id === draftRef.current.entryNodeID) return
    commitDraft((current) => ({ ...current, entryNodeID: selectedNode.id }))
    onNotice('Start step updated.')
  }

  const resetLayout = () => {
    const next: Record<string, { x: number; y: number }> = {}
    draftRef.current.nodes.forEach((node, index) => {
      next[node.id] = { x: 32 + (index % 3) * 228, y: 32 + Math.floor(index / 3) * 142 }
    })
    setPositions(next)
    onNotice('Canvas layout reset.')
  }

  const saveWorkflow = async () => {
    const result = await validateDraft()
    if (!result?.valid) {
      onNotice(result?.errors?.[0]?.message ?? 'Fix the validation errors before saving.')
      return
    }
    void onSave({ ...draftRef.current, updatedAt: new Date().toISOString() })
  }

  const startWorkflow = async () => {
    const result = await validateDraft()
    if (!result?.valid) {
      onNotice(result?.errors?.[0]?.message ?? 'Fix the validation errors before running.')
      return
    }
    onStart(draftRef.current)
  }

  const handleCanvasKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement
    const editing = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT'
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z' && !editing) {
      event.preventDefault()
      if (event.shiftKey) redo()
      else undo()
      return
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'y' && !editing) {
      event.preventDefault()
      redo()
      return
    }
    if (event.key === 'Escape' && connectingFrom) {
      event.preventDefault()
      clearConnection()
      onNotice('Connection canceled.')
      return
    }
    if (!editing && (event.key === 'Delete' || event.key === 'Backspace')) {
      event.preventDefault()
      removeSelectedNode()
    }
  }

  const validationIssue = validation?.errors?.[0] ?? validation?.warnings?.[0]
  const validationTone = validationError || (validation && !validation.valid) ? 'invalid' : validation?.warnings?.length ? 'warning' : validation?.valid ? 'valid' : 'pending'
  const validationTitle = validationError ? 'Validation unavailable' : validating ? 'Checking workflow…' : validation?.valid ? 'Workflow is ready' : validation ? 'Fix before running' : 'Workflow not checked yet'
  const validationDetail = validationError || validationIssue?.message || (validation?.warnings?.length ? `${validation.warnings.length} warning${validation.warnings.length === 1 ? '' : 's'} to review.` : 'Add blocks, connect them, and validate before running.')
  const workflowState = history.past.length ? 'Unsaved changes' : draft.createdAt ? 'Saved' : 'Unsaved'

  return (
    <section className="workbench-wrap" aria-label={`${draft.name} workflow editor`}>
      <div className="workbench-toolbar">
        <div className="workbench-title">
          <div className="workbench-title-line"><span className="workflow-ready-dot" aria-hidden="true" /><h2>{draft.name}</h2><span className={`workflow-state-badge ${workflowState === 'Saved' ? 'saved' : ''}`}>{workflowState}</span></div>
          <p className="workbench-help">Add a block, drag output to input, configure it on the right.</p>
        </div>
        <div className="workbench-summary" aria-label="Workflow summary">
          <span><strong>{draft.nodes.length}</strong><small>steps</small></span>
          <span><strong>{draft.edges.length}</strong><small>connections</small></span>
          <span><strong>{draft.nodes.filter((node) => node.type === 'agent').length}</strong><small>agents</small></span>
        </div>
        <div className="toolbar-actions">
          <div className="workflow-palette-wrap" ref={paletteRef}>
            <button type="button" className="button primary workflow-action" onClick={() => setPaletteOpen((open) => !open)} aria-expanded={paletteOpen} aria-haspopup="menu"><span className="button-symbol" aria-hidden="true">+</span>Add block</button>
            {paletteOpen && <div className="workflow-palette" role="menu" aria-label="Add workflow block"><div className="workflow-palette-heading"><strong>Choose a block</strong><span>Start with the next step</span></div>{workflowBlockTypes.map((block) => <button type="button" role="menuitem" key={block.type} className="workflow-palette-item" onClick={() => addNode(block.type)}><span className={`node-type-mark palette-mark type-${block.type}`}>{block.mark}</span><span><strong>{block.label}</strong><small>{block.detail}</small></span></button>)}</div>}
          </div>
          <div className="workflow-history-actions" aria-label="Edit history">
            <button type="button" className="button subtle workflow-icon-action" onClick={undo} disabled={!history.past.length} aria-label="Undo last change" title="Undo (Ctrl+Z)">↶</button>
            <button type="button" className="button subtle workflow-icon-action" onClick={redo} disabled={!history.future.length} aria-label="Redo last change" title="Redo (Ctrl+Y)">↷</button>
          </div>
          <button type="button" className="button subtle workflow-action" onClick={resetLayout}><span className="button-symbol" aria-hidden="true">⌗</span>Arrange</button>
          <button type="button" className="button subtle workflow-action" onClick={exportJSON}><span className="button-symbol" aria-hidden="true">↓</span>Export</button>
          <button type="button" className="button subtle workflow-action" onClick={() => fileRef.current?.click()}><span className="button-symbol" aria-hidden="true">↑</span>Import</button>
          <input ref={fileRef} type="file" accept="application/json,.json" hidden onChange={importJSON} />
          <button type="button" className="button subtle workflow-action" onClick={() => void saveWorkflow()} disabled={busy || validating}><span className="button-symbol" aria-hidden="true">✓</span>{busy ? 'Saving…' : 'Save workflow'}</button>
          {onDelete && <button type="button" className="button danger-quiet workflow-action" onClick={onDelete} disabled={busy}><span className="button-symbol" aria-hidden="true">×</span>Delete</button>}
          <button type="button" className="button primary workflow-action" onClick={() => void startWorkflow()} disabled={busy || validating || !draft.nodes.length}><span className="button-symbol" aria-hidden="true">▶</span>Run workflow</button>
        </div>
      </div>
      <div className={`workflow-validation ${validationTone}`} role="status" aria-live="polite"><span className="workflow-validation-mark" aria-hidden="true">{validationTone === 'valid' ? '✓' : validationTone === 'invalid' ? '!' : '·'}</span><div><strong>{validationTitle}</strong><span>{validationDetail}</span></div><div className="workflow-validation-actions">{draft.nodes.length > 1 && draft.edges.length === 0 && <button type="button" className="button subtle small workflow-repair-action" onClick={autoConnectWorkflow}>Connect in order</button>}<button type="button" className="link-button" onClick={() => void validateDraft()} disabled={validating}>{validating ? 'Checking…' : 'Validate'}</button></div></div>
      <div className="canvas-layout">
        <div className="graph-canvas" onPointerMove={moveConnection} onPointerUp={endConnection} onPointerCancel={() => { if (connectionDrag.current) clearConnection(); else stopDrag() }} onKeyDown={handleCanvasKeyDown} role="region" aria-label="Visual workflow canvas. Use Delete to remove the selected step." tabIndex={0}>
          <div className="canvas-toolbar">
            <div className={`canvas-mode ${connectingFrom ? 'active' : ''}`} role="status"><span className="canvas-mode-dot" aria-hidden="true" />{connectingFrom ? (connectionPreview ? 'Release to connect' : 'Choose an input handle') : 'Drag to connect'}</div>
            <span className="canvas-hint">Output <b>●</b> → input <b>●</b> · body moves · Delete removes</span>
          </div>
          <div className="graph-surface" ref={canvasRef} style={{ width: surfaceSize.width, height: surfaceSize.height }} onPointerDown={(event) => { if (event.target === event.currentTarget) setSelectedNodeID('') }}>
            <div className="canvas-ruler horizontal" /><div className="canvas-ruler vertical" />
            <svg className="graph-edges" viewBox={`0 0 ${surfaceSize.width} ${surfaceSize.height}`} preserveAspectRatio="none" aria-hidden="true"><defs><marker id="edge-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="currentColor" /></marker></defs>{draft.edges.map((edge) => { const from = connectionPoint(edge.from, 'output'); const to = connectionPoint(edge.to, 'input'); const bend = Math.max(48, Math.abs(to.x - from.x) * .45); const path = `M ${from.x} ${from.y} C ${from.x + bend} ${from.y}, ${to.x - bend} ${to.y}, ${to.x} ${to.y}`; return <path key={edge.id} d={path} markerEnd="url(#edge-arrow)" className={edge.condition ? 'conditional' : ''} /> })}{connectingFrom && connectionPreview && (() => { const from = connectionPoint(connectingFrom, 'output'); const bend = Math.max(48, Math.abs(connectionPreview.x - from.x) * .45); return <path className="connection-preview" d={`M ${from.x} ${from.y} C ${from.x + bend} ${from.y}, ${connectionPreview.x - bend} ${connectionPreview.y}, ${connectionPreview.x} ${connectionPreview.y}`} markerEnd="url(#edge-arrow)" /> })()}</svg>
            {draft.nodes.map((node, index) => {
              const value = point(node.id)
              const isSelected = node.id === selectedNodeID
              const meta = workflowNodeMeta[node.type] ?? { label: node.type, mark: '•', detail: 'Configure this workflow step.' }
              const agent = node.agentID ? agents.find((item) => item.id === node.agentID) : undefined
              const detail = node.type === 'agent' ? agent?.role ?? 'Assign an agent' : node.type === 'condition' ? node.condition || 'Declarative branch' : node.type === 'loop' ? `Up to ${node.maxIterations ?? 0} iterations` : node.type === 'tool' ? node.toolName || 'Configured tool' : node.type === 'artifact' ? node.artifactPath || 'Output artifact' : node.type === 'parallel' ? 'Branch fan-out' : node.type === 'join' ? 'Wait for branches' : node.type === 'approval' ? 'Pauses for a decision' : meta.label
              return <div key={node.id} className={`graph-node type-${node.type} ${isSelected ? 'selected' : ''} ${connectingFrom === node.id ? 'connecting-source' : ''} ${connectionTarget === node.id ? 'connection-target' : ''}`} style={{ left: value.x, top: value.y }} onPointerDown={(event) => startDrag(event, node.id)} onClick={() => handleNodeClick(node.id)} tabIndex={0} aria-label={`${meta.label} step ${index + 1}: ${node.label || meta.label}`} onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); handleNodeClick(node.id) } }}><button type="button" className={`node-handle input ${connectionTarget === node.id ? 'target' : ''}`} data-node-input={node.id} aria-label={`Input of ${node.label || node.id}`} onPointerDown={(event) => event.stopPropagation()} onClick={(event) => { event.stopPropagation(); if (!connectionDrag.current) finishConnection(node.id) }} /><div className="node-topline"><span className="node-type-pill"><span className="node-type-mark" aria-hidden="true">{meta.mark}</span>{meta.label}</span><span className="node-index">#{String(index + 1).padStart(2, '0')}</span></div><strong>{node.label || meta.label}</strong><small>{detail}</small><div className="node-footer"><span>{node.retry?.maxAttempts ?? 1} attempt{(node.retry?.maxAttempts ?? 1) === 1 ? '' : 's'}</span><span className="node-output-label">output ●</span></div><button type="button" className="node-handle output" aria-label={`Output of ${node.label || node.id}`} onPointerDown={(event) => startConnectionDrag(event, node.id)} onKeyDown={(event) => startKeyboardConnection(event, node.id)} onClick={(event) => event.stopPropagation()} /></div>
            })}
          </div>
          <div className="canvas-caption" aria-live="polite"><span className="legend-line" /><span>{connectingFrom ? (connectionPreview ? 'Release on an input handle' : 'Choose an input handle, or press Escape') : 'Drag from an output handle to an input handle.'}</span><span className="legend-dot" /><span>{draft.edges.length} connections</span></div>
        </div>
        <aside className="node-inspector" aria-label="Step inspector">
          <div className="inspector-heading"><div><span className="section-kicker">Step inspector</span><h3>{selectedNode?.label || 'Select a step'}</h3><p className="inspector-subtitle">{selectedNode ? selectedNodeMeta.detail : 'Select a block to configure it.'}</p></div><span className="mini-code">{selectedNodeMeta.label}</span></div>
          {selectedNode ? <>
            <div className="inspector-meta"><span className="inspector-chip">{selectedNode.id === draft.entryNodeID ? 'Start step' : 'Workflow step'}</span>{selectedAgent && <span className="inspector-chip">{selectedAgent.name}</span>}</div>
            <div className="inspector-actions"><button type="button" className="button subtle small" onClick={duplicateNode}>Duplicate</button>{selectedNode.id !== draft.entryNodeID && <button type="button" className="link-button" onClick={setEntryNode}>Set as start</button>}<button type="button" className="button danger-quiet small" onClick={removeSelectedNode}>Delete</button></div>
            <label className="field"><span>Name</span><input value={selectedNode.label} onChange={(event) => updateNode({ label: event.target.value })} /></label>
            <label className="field"><span>Type</span><select value={selectedNode.type} onChange={(event) => updateNode({ type: event.target.value })}>{workflowBlockTypes.map((block) => <option key={block.type} value={block.type}>{block.label}</option>)}</select></label>
            {selectedNode.type === 'agent' && <><label className="field"><span>Agent</span><select value={selectedNode.agentID ?? ''} onChange={(event) => updateNode({ agentID: event.target.value })}><option value="">Select an agent</option>{agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select>{agents.length === 0 && <small className="field-hint">Create an agent before running this step.</small>}</label><label className="field"><span>Task prompt</span><textarea rows={5} value={selectedNode.prompt ?? ''} placeholder="Describe the exact work and expected evidence for this step..." onChange={(event) => updateNode({ prompt: event.target.value })} /><small className="field-hint">This task is sent to the agent at runtime, together with the project brief.</small></label></>}
            {selectedNode.type === 'loop' && <label className="field"><span>Max iterations</span><input type="number" min={1} value={selectedNode.maxIterations ?? 1} onChange={(event) => updateNode({ maxIterations: Number(event.target.value) })} /></label>}
            {selectedNode.type === 'condition' && <label className="field"><span>Condition expression</span><input value={selectedNode.condition ?? ''} placeholder="truthy:review.approved" onChange={(event) => updateNode({ condition: event.target.value })} /></label>}
            {selectedNode.type === 'tool' && <label className="field"><span>Tool name</span><input value={selectedNode.toolName ?? ''} placeholder="server.tool" onChange={(event) => updateNode({ toolName: event.target.value })} /></label>}
            {selectedNode.type === 'artifact' && <label className="field"><span>Artifact path</span><input value={selectedNode.artifactPath ?? ''} placeholder="outputs/result.json" onChange={(event) => updateNode({ artifactPath: event.target.value })} /></label>}
            <details className="inspector-advanced"><summary>Retries & limits</summary><div className="inspector-advanced-body"><div className="field-grid"><label className="field"><span>Max attempts</span><input type="number" min={1} value={selectedNode.retry?.maxAttempts ?? 1} onChange={(event) => updateNode({ retry: { ...(selectedNode.retry ?? { backoffSeconds: 0, idempotent: true }), maxAttempts: Number(event.target.value) } })} /></label><label className="field"><span>Timeout (s)</span><input type="number" min={0} value={selectedNode.timeoutSeconds ?? 0} onChange={(event) => updateNode({ timeoutSeconds: Number(event.target.value) || undefined })} /></label></div><label className="field"><span>Backoff (s)</span><input type="number" min={0} value={selectedNode.retry?.backoffSeconds ?? 0} onChange={(event) => updateNode({ retry: { ...(selectedNode.retry ?? { maxAttempts: 1, idempotent: true }), backoffSeconds: Number(event.target.value) } })} /></label></div></details>
            <div className="inspector-list"><div><span>ID</span><strong>{selectedNode.id}</strong></div><div><span>Timeout</span><strong>{selectedNode.timeoutSeconds ? `${selectedNode.timeoutSeconds}s` : 'Default'}</strong></div><div><span>Retry</span><strong>{selectedNode.retry?.maxAttempts ?? 1} attempt(s)</strong></div></div>
            <div className="edge-list"><div className="field-heading connection-heading"><span>Connections</span><small>{draft.edges.filter((edge) => edge.from === selectedNode.id).length} out · {draft.edges.filter((edge) => edge.to === selectedNode.id).length} in</small></div>{draft.edges.filter((edge) => edge.from === selectedNode.id).map((edge) => <div className="edge-row" key={edge.id}><span>→ {draft.nodes.find((node) => node.id === edge.to)?.label ?? edge.to}</span><input className="edge-condition" value={edge.condition ?? ''} placeholder="always" aria-label={`Condition for ${edge.id}`} onChange={(event) => updateEdgeCondition(edge.id, event.target.value)} /><button type="button" onClick={() => removeEdge(edge.id)} aria-label="Remove connection">×</button></div>)}{draft.edges.filter((edge) => edge.to === selectedNode.id).map((edge) => <div className="edge-row incoming" key={`incoming-${edge.id}`}><span>← {draft.nodes.find((node) => node.id === edge.from)?.label ?? edge.from}</span><small>input</small></div>)}{draft.edges.filter((edge) => edge.from === selectedNode.id).length === 0 && draft.edges.filter((edge) => edge.to === selectedNode.id).length === 0 && <small>No connections yet. Drag from this block's output.</small>}</div>
            <div className="inspector-note"><span className="note-mark">i</span><span>Drag between handles to connect. Use Delete to remove a selected block.</span></div>
          </> : <p className="muted">Select a step on the canvas to configure it.</p>}
        </aside>
      </div>
      <div className="workbench-footer"><div className="workbench-footer-meta"><span>Version {draft.version}</span><span>{draft.nodes.length} steps</span><span>{draft.edges.length} connections</span><span className={`footer-validation ${validationTone}`}>{validation?.valid ? 'Ready to run' : validation?.errors?.length ? `${validation.errors.length} issue${validation.errors.length === 1 ? '' : 's'}` : 'Draft'}</span></div><button type="button" className="link-button" onClick={() => void saveWorkflow()} disabled={busy || validating}>Save workflow →</button></div>
    </section>
  )
}

function SystemPromptEditor({
  prompts,
  onSave,
  onReset,
  busy,
}: {
  prompts: SystemPrompt[]
  onSave: (promptID: string, template: string) => Promise<void>
  onReset: (promptID: string) => Promise<void>
  busy: boolean
}) {
  const [selectedID, setSelectedID] = useState('')
  const [draft, setDraft] = useState('')
  const selected = prompts.find((prompt) => prompt.id === selectedID) ?? prompts[0]
  const selectedVariables = Array.isArray(selected?.variables) ? selected.variables : []
  const isDirty = Boolean(selected && draft !== selected.template)

  useEffect(() => {
    if (!selectedID && prompts[0]) setSelectedID(prompts[0].id)
    if (selectedID && !prompts.some((prompt) => prompt.id === selectedID)) setSelectedID(prompts[0]?.id ?? '')
  }, [prompts, selectedID])

  useEffect(() => {
    setDraft(selected?.template ?? '')
  }, [selected?.id, selected?.template])

  const selectPrompt = (promptID: string) => {
    if (promptID === selected?.id) return
    if (isDirty && !window.confirm('Discard unsaved prompt changes?')) return
    setSelectedID(promptID)
  }

  const save = async () => {
    if (!selected || !isDirty) return
    await onSave(selected.id, draft)
  }

  const reset = async () => {
    if (!selected?.isCustomized) return
    if (!window.confirm(`Reset “${selected.name}” to its default prompt?`)) return
    await onReset(selected.id)
  }

  return (
    <section className="panel-card system-prompts-card" aria-labelledby="system-prompts-heading">
      <div className="system-prompts-heading">
        <div>
          <span className="section-kicker">Orchestrator configuration</span>
          <h2 id="system-prompts-heading">System prompts</h2>
          <p>View and edit the internal prompt blocks used to assemble every agent turn. Agent instructions and persistent memory are separate profile data.</p>
        </div>
        <span className="system-prompts-count">{prompts.length} prompt blocks</span>
      </div>
      {prompts.length === 0 ? <div className="empty-state system-prompts-empty"><strong>System prompts unavailable</strong><span>Reload the application to load the orchestrator defaults.</span></div> : <div className="system-prompts-layout">
        <nav className="system-prompt-list" aria-label="System prompt blocks">
          <div className="system-prompt-list-heading"><span>Prompt blocks</span><span>{prompts.filter((prompt) => prompt.isCustomized).length} customized</span></div>
          {prompts.map((prompt) => <button type="button" key={prompt.id} className={`system-prompt-item ${prompt.id === selected?.id ? 'selected' : ''}`} onClick={() => selectPrompt(prompt.id)}>
            <span className="system-prompt-item-copy"><strong>{prompt.name}</strong><small>{prompt.description}</small></span>
            <span className={`system-prompt-item-state ${prompt.isCustomized ? 'customized' : ''}`}>{prompt.isCustomized ? 'Customized' : 'Default'}</span>
          </button>)}
        </nav>
        {selected && <div className="system-prompt-editor">
          <div className="system-prompt-editor-heading">
            <div><span className="mini-code">{selected.id}</span><h3>{selected.name}</h3><p>{selected.description}</p></div>
            <StatusPill value={selected.isCustomized ? 'Customized' : 'Default'} tone={selected.isCustomized ? 'warning' : 'neutral'} />
          </div>
          <label className="field system-prompt-field"><span>Template</span><textarea value={draft} onChange={(event) => setDraft(event.target.value)} spellCheck={false} aria-label={`${selected.name} template`} rows={13} /></label>
          <div className="system-prompt-help"><span>Changes apply to new agent turns.</span>{selectedVariables.length > 0 && <span>Variables: {selectedVariables.map((variable) => <code key={variable}>{`{{${variable}}}`}</code>)}</span>}</div>
          <details className="system-prompt-default"><summary>View default template</summary><pre>{selected.defaultTemplate}</pre></details>
          <div className="system-prompt-actions"><button type="button" className="button subtle" onClick={() => void reset()} disabled={busy || !selected.isCustomized}>Reset to default</button><button type="button" className="button primary" onClick={() => void save()} disabled={busy || !isDirty}>{busy ? 'Saving…' : 'Save prompt'}</button></div>
        </div>}
      </div>}
    </section>
  )
}

function ProjectEditor({
  project,
  onChange,
  onSave,
  onOpen,
  onDelete,
  onChooseFolder,
  onLearnSystem,
  models,
  busy,
}: {
  project: Project
  onChange: (project: Project) => void
  onSave: () => void
  onOpen: () => void
  onDelete?: () => void
  onChooseFolder: () => Promise<string | undefined>
  onLearnSystem: (project: Project, modelID: string, reasoningEffort: string) => Promise<void>
  models: ModelInfo[]
  busy: boolean
}) {
  const [selectingFolder, setSelectingFolder] = useState(false)
  const [learnOpen, setLearnOpen] = useState(false)
  const [learning, setLearning] = useState(false)
  const [learnModelID, setLearnModelID] = useState('')
  const [learnEffort, setLearnEffort] = useState('')
  const selectedLearnModel = models.find((model) => model.id === learnModelID) ?? models.find((model) => model.isDefault) ?? models[0]
  const learnEfforts = selectedLearnModel?.supportedReasoningEfforts ?? []
  const update = <K extends keyof Project>(key: K, value: Project[K]) => onChange({ ...project, [key]: value })
  const chooseFolder = async () => {
    if (selectingFolder || busy) return
    setSelectingFolder(true)
    try {
      const folder = await onChooseFolder()
      if (!folder) return
      const alreadyAdded = project.folders.some((existing) => existing.toLowerCase() === folder.toLowerCase())
      if (!alreadyAdded) update('folders', [...project.folders, folder])
    } finally {
      setSelectingFolder(false)
    }
  }
  const removeFolder = (folder: string) => update('folders', project.folders.filter((item) => item !== folder))
  const makePrimary = (folder: string) => update('folders', [folder, ...project.folders.filter((item) => item !== folder)])
  const startLearning = async () => {
    if (!project.id || project.folders.length === 0 || learning || busy) return
    setLearning(true)
    try {
      await onLearnSystem(project, learnModelID, learnEffort)
      setLearnOpen(false)
    } catch {
      // The parent reports the backend error and keeps the confirmation open.
    } finally {
      setLearning(false)
    }
  }

  return <section className="panel-card project-editor-card">
    <div className="editor-heading"><div><span className="section-kicker">Project workspace</span><h2>{project.id ? project.name : 'New project'}</h2></div><span className="mini-code">{project.id ? project.id.slice(0, 8) : 'draft'}</span></div>
    <p className="editor-intro">A project is the context boundary for runs. Add multiple folders when one workflow spans repositories or services.</p>
    <div className="field-grid">
      <label className="field"><span>Name</span><input value={project.name} onChange={(event) => update('name', event.target.value)} placeholder="e.g. Centurion" /></label>
      <label className="field"><span>Description</span><input value={project.description ?? ''} onChange={(event) => update('description', event.target.value)} placeholder="What belongs here?" /></label>
    </div>
    <div className="field project-folders-field"><span className="field-heading">Project folders</span><span className="field-hint">Agents use these folders as their workspace boundary. The first folder is the primary root, and system documentation is written to its docs/ folder.</span>
      <div className="project-folder-list">{project.folders.map((folder, index) => <div className={`project-folder-row ${index === 0 ? 'primary' : ''}`} key={folder}><span className="folder-mark" aria-hidden="true">/</span><div className="project-folder-copy"><code title={folder}>{folder}</code>{index === 0 && <span className="folder-primary-badge">Primary folder</span>}</div><div className="project-folder-row-actions">{index > 0 && <button type="button" className="folder-primary-button" onClick={() => makePrimary(folder)} disabled={busy || learning}>Set as primary</button>}<button type="button" className="icon-button" onClick={() => removeFolder(folder)} aria-label={`Remove ${folder}`} disabled={busy || learning}>×</button></div></div>)}{project.folders.length === 0 && <div className="empty-small"><strong>No folders yet</strong><span>Choose at least one local folder for this project.</span></div>}</div>
      <div className="project-folder-add"><div className="project-folder-picker-copy"><span className="folder-picker-icon" aria-hidden="true">+</span><span><strong>Add a workspace folder</strong><small>Choose a folder from Windows Explorer.</small></span></div><button type="button" className="button subtle project-folder-picker-button" onClick={() => void chooseFolder()} disabled={busy || selectingFolder} aria-busy={selectingFolder}>{selectingFolder ? 'Opening…' : 'Choose folder'}</button></div>
    </div>
    {learnOpen && <div className="learn-confirmation" role="dialog" aria-modal="false" aria-labelledby="learn-system-title" aria-describedby="learn-system-description" aria-busy={learning}><div className="learn-confirmation-heading"><div><span className="section-kicker">Codex documentation</span><h3 id="learn-system-title">Learn this system</h3></div><button type="button" className="link-button" onClick={() => setLearnOpen(false)} disabled={learning}>Close</button></div><p id="learn-system-description">Centurion will inspect every configured folder and create a factual documentation set in the primary project folder.</p><div className="learn-token-warning"><strong>Token-intensive operation</strong><span>This can consume a lot of tokens on large repositories. Dependencies, generated output, binaries, caches, and secrets are skipped to keep the pass focused.</span></div><div className="field-grid"><label className="field"><span>Model</span><select value={learnModelID} onChange={(event) => { setLearnModelID(event.target.value); setLearnEffort('') }} disabled={learning || busy}><option value="">Account default</option>{models.map((model) => <option key={model.id} value={model.id}>{model.displayName}</option>)}</select></label><label className="field"><span>Thinking effort</span><select value={learnEffort} onChange={(event) => setLearnEffort(event.target.value)} disabled={learning || busy || learnEfforts.length === 0}><option value="">Automatic, economy first</option>{learnEfforts.map((effort) => <option key={effort.reasoningEffort} value={effort.reasoningEffort}>{effort.reasoningEffort}</option>)}</select></label></div><div className="learn-output-note"><span>Output</span><code>docs/centurion-*.md</code><small>Only the generated documentation files inside the primary folder are requested.</small></div><div className="learn-confirmation-actions"><button type="button" className="button subtle" onClick={() => setLearnOpen(false)} disabled={learning}>Cancel</button><button type="button" className="button primary" onClick={() => void startLearning()} disabled={learning || busy || !project.folders.length}>{learning ? 'Documenting…' : 'Start documentation'}</button></div></div>}
    <div className="project-editor-actions"><button type="button" className="button subtle" onClick={onOpen} disabled={!project.id}>Open project</button><div className="project-editor-actions-right">{project.id && <button type="button" className="button subtle" onClick={() => setLearnOpen(true)} disabled={busy || !project.folders.length}>Learn this system</button>}{onDelete && <button type="button" className="button danger-quiet" onClick={onDelete} disabled={busy}>Delete</button>}<button type="button" className="button primary" onClick={onSave} disabled={busy || !project.name.trim() || project.folders.length === 0}>{busy ? 'Saving…' : 'Save project'}</button></div></div>
  </section>
}

function TerminalView({ project, onNotice, onHistory }: { project?: Project; onNotice: (message: string) => void; onHistory: () => void }) {
  const [command, setCommand] = useState('')
  const [workingDir, setWorkingDir] = useState(project?.folders[0] ?? '')
  const [result, setResult] = useState<TerminalResult | null>(null)
  const [running, setRunning] = useState(false)

  useEffect(() => {
    setWorkingDir(project?.folders[0] ?? '')
    setResult(null)
  }, [project?.id])

  const run = async () => {
    if (!project || !command.trim()) return
    setRunning(true)
    try {
      const next = await api.runTerminalCommand(project.id, command, workingDir)
      setResult(next)
      onHistory()
    } catch (error) {
      onNotice(errorText(error))
    } finally {
      setRunning(false)
    }
  }

  if (!project) return <div className="empty-state panel-card"><strong>Create a project first</strong><span>The terminal is scoped to the active project folders.</span></div>
  return <div className="view-stack terminal-page">
    <div className="view-heading"><div><span className="section-kicker">Fast access</span><h1>Terminal</h1><p>Run a local command in the active project without leaving Centurion.</p></div><StatusPill value={`${project.folders.length} folders`} tone="neutral" /></div>
    <section className="panel-card terminal-card">
      <div className="terminal-toolbar"><label className="field terminal-directory"><span>Working directory</span><select value={workingDir} onChange={(event) => setWorkingDir(event.target.value)}>{project.folders.map((folder) => <option key={folder} value={folder}>{folder}</option>)}</select></label><div className="terminal-scope"><span className="eyebrow">Project context</span><strong>{project.name}</strong><small>Starts here and uses your local account.</small></div></div>
      <label className="field terminal-command-field"><span>Command</span><div className="terminal-command-row"><span className="terminal-prompt" aria-hidden="true">$</span><input value={command} onChange={(event) => setCommand(event.target.value)} onKeyDown={(event) => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') void run() }} placeholder="git status" spellCheck={false} /><button type="button" className="button primary" onClick={() => void run()} disabled={running || !command.trim()}>{running ? 'Running…' : 'Run'}</button></div><span className="field-hint">Press Ctrl+Enter to run. It is user-invoked, capped at 120 seconds, and saved in local history.</span></label>
      <div className="terminal-output" aria-live="polite"><div className="terminal-output-topline"><span>Output</span>{result && <span className={result.exitCode === 0 ? 'terminal-success' : 'terminal-failure'}>{result.exitCode === 0 ? 'Success' : `Exit ${result.exitCode}`} · {result.durationMs}ms</span>}</div>{result ? <pre>{result.output || '(no output)'}</pre> : <div className="terminal-empty"><span className="terminal-caret">▌</span><span>Run a command to see its output here.</span></div>}{result?.truncated && <small className="terminal-truncated">Output was truncated at 256 KB.</small>}</div>
    </section>
    <div className="terminal-folder-strip"><span className="eyebrow">Available folders</span>{project.folders.map((folder) => <button type="button" key={folder} className={`folder-chip ${workingDir === folder ? 'selected' : ''}`} onClick={() => setWorkingDir(folder)}>{folder}</button>)}</div>
  </div>
}

function HistoryView({ entries, kind, onKindChange }: { entries: HistoryEntry[]; kind: string; onKindChange: (kind: string) => void }) {
  const filters = [{ value: '', label: 'All activity' }, { value: 'planning', label: 'Planning' }, { value: 'prompt', label: 'Prompts' }, { value: 'builder_prompt', label: 'Builder' }, { value: 'conversation', label: 'Conversations' }, { value: 'system_learning', label: 'Documentation' }, { value: 'terminal', label: 'Terminal' }, { value: 'project', label: 'Projects' }]
  return <div className="view-stack history-page">
    <div className="view-heading"><div><span className="section-kicker">Local record</span><h1>History</h1><p>Prompts, agent responses, terminal commands, and project activity for the active project.</p></div><div className="history-filter" role="tablist" aria-label="History filters">{filters.map((filter) => <button type="button" key={filter.value} className={kind === filter.value ? 'active' : ''} onClick={() => onKindChange(filter.value)}>{filter.label}</button>)}</div></div>
    <section className="history-list">{entries.map((entry) => <article className={`panel-card history-entry history-kind-${entry.kind}`} key={entry.id}><div className="history-entry-topline"><span className="history-kind">{entry.kind}</span><time>{formatTime(entry.createdAt)}</time></div><h2>{entry.title}</h2>{entry.content && <div className="history-entry-content">{readableUserText(entry.content)}</div>}</article>)}{entries.length === 0 && <div className="empty-state panel-card"><strong>No history yet</strong><span>Activity will appear here as the project runs agents or terminal commands.</span></div>}</section>
  </div>
}

type BuilderMode = 'planning' | 'building'
type BuilderRequestMeta = { prompt: string; mode: BuilderMode; modelID?: string; reasoningEffort?: string; subagentApprovalProfile?: string; durationMs?: number }
type BuilderMessage = { id: number; role: 'user' | 'assistant' | 'system'; text: string; request?: BuilderRequestMeta; scope?: BuilderMode }

const builderFallbackActivities: Record<BuilderMode, string[]> = {
  planning: ['Reviewing the request', 'Checking assumptions', 'Organizing the next questions'],
  building: ['Reading the agreed scope', 'Mapping agent responsibilities', 'Drafting the workflow'],
}

type BuilderSessionSnapshot = {
  version: 3
  projectID: string
  mode: BuilderMode
  prompt: string
  planningThreadID: string
  builderThreadID: string
  plannerAgentID: string
  messages: BuilderMessage[]
  proposal: BuilderProposal | null
  selectedModelID: string
  selectedEffort: string
  subagentApprovalProfile: string
  confirming: boolean
  planningHistoryCutoff: string
}

type PersistedBuilderSession = Partial<Omit<BuilderSessionSnapshot, 'version'>> & { version?: number }

const builderSessionStoragePrefix = 'centurion.builder-session.v3'
const previousBuilderSessionStoragePrefix = 'centurion.builder-session.v2'
const legacyBuilderSessionStoragePrefix = 'centurion.builder-session.v1'

function builderSessionStorageKey(projectID: string): string {
  return `${builderSessionStoragePrefix}.${encodeURIComponent(projectID)}`
}

function previousBuilderSessionStorageKey(projectID: string): string {
  return `${previousBuilderSessionStoragePrefix}.${encodeURIComponent(projectID)}`
}

function legacyBuilderSessionStorageKey(projectID: string): string {
  return `${legacyBuilderSessionStoragePrefix}.${encodeURIComponent(projectID)}`
}

function parseBuilderMessage(value: unknown): BuilderMessage | null {
  if (!value || typeof value !== 'object') return null
  const candidate = value as Partial<BuilderMessage>
  if (typeof candidate.id !== 'number'
    || !Number.isFinite(candidate.id)
    || (candidate.role !== 'user' && candidate.role !== 'assistant' && candidate.role !== 'system')
    || typeof candidate.text !== 'string') return null
  const request = candidate.request && typeof candidate.request === 'object' ? candidate.request as BuilderRequestMeta : undefined
  const scope = candidate.scope === 'planning' || candidate.scope === 'building' ? candidate.scope : request?.mode
  return { id: candidate.id, role: candidate.role, text: candidate.text, request, scope }
}

function inferBuilderMessageScopes(messages: BuilderMessage[]): BuilderMessage[] {
  let currentScope: BuilderMode = 'planning'
  return messages.map((message, index) => {
    const explicitScope = message.scope ?? message.request?.mode
    if (explicitScope === 'planning' || explicitScope === 'building') {
      currentScope = explicitScope
      return { ...message, scope: explicitScope }
    }
    const nextScopedMessage = messages.slice(index + 1).find((candidate) => candidate.scope === 'planning' || candidate.scope === 'building' || candidate.request?.mode === 'planning' || candidate.request?.mode === 'building')
    const inferredScope = nextScopedMessage?.scope ?? nextScopedMessage?.request?.mode ?? currentScope
    return { ...message, scope: inferredScope }
  })
}

function readBuilderSession(projectID: string): BuilderSessionSnapshot | null {
  try {
    let raw = window.localStorage.getItem(builderSessionStorageKey(projectID))
    let migrated = false
    let sourceVersion = 3
    if (!raw) {
      raw = window.localStorage.getItem(previousBuilderSessionStorageKey(projectID))
      migrated = Boolean(raw)
      sourceVersion = 2
    }
    if (!raw) {
      raw = window.localStorage.getItem(legacyBuilderSessionStorageKey(projectID))
      migrated = Boolean(raw)
      sourceVersion = 1
    }
    if (!raw) return null
    const parsed = JSON.parse(raw) as PersistedBuilderSession
    const parsedVersion = parsed.version ?? sourceVersion
    if ((parsedVersion !== 1 && parsedVersion !== 2 && parsedVersion !== 3) || parsed.projectID !== projectID || !Array.isArray(parsed.messages)) return null
    const isCurrent = !migrated && parsedVersion === 3
    const canPreserveSelection = parsedVersion >= 2
    return {
      version: 3,
      projectID,
      mode: parsed.mode === 'building' ? 'building' : 'planning',
      prompt: typeof parsed.prompt === 'string' ? parsed.prompt : '',
      // v1 and v2 proposals may contain a model chosen by the Builder itself.
      // Do not reuse their threads or proposals; a fresh request is required
      // after the application model selection becomes authoritative.
      planningThreadID: isCurrent && typeof parsed.planningThreadID === 'string' ? parsed.planningThreadID : '',
      builderThreadID: isCurrent && typeof parsed.builderThreadID === 'string' ? parsed.builderThreadID : '',
      plannerAgentID: typeof parsed.plannerAgentID === 'string' ? parsed.plannerAgentID : '',
      messages: inferBuilderMessageScopes(parsed.messages.map(parseBuilderMessage).filter((message): message is BuilderMessage => message !== null)).slice(-80),
      proposal: isCurrent && parsed.proposal && typeof parsed.proposal === 'object' ? parsed.proposal as BuilderProposal : null,
      selectedModelID: canPreserveSelection && typeof parsed.selectedModelID === 'string' ? parsed.selectedModelID : '',
      selectedEffort: canPreserveSelection && typeof parsed.selectedEffort === 'string' ? parsed.selectedEffort : '',
      subagentApprovalProfile: parsed.subagentApprovalProfile === 'autonomous' ? 'autonomous' : 'on_request',
      confirming: isCurrent && parsed.confirming === true,
      planningHistoryCutoff: typeof parsed.planningHistoryCutoff === 'string' ? parsed.planningHistoryCutoff : '',
    }
  } catch {
    return null
  }
}

function persistBuilderSession(snapshot: BuilderSessionSnapshot): void {
  if (!snapshot.projectID) return
  const key = builderSessionStorageKey(snapshot.projectID)
  const isEmpty = snapshot.messages.length === 0
    && !snapshot.prompt.trim()
    && !snapshot.planningThreadID
    && !snapshot.builderThreadID
    && !snapshot.proposal
    && !snapshot.selectedModelID
    && !snapshot.selectedEffort
    && snapshot.subagentApprovalProfile === 'on_request'
    && !snapshot.planningHistoryCutoff
  try {
    if (isEmpty) {
      window.localStorage.removeItem(key)
      return
    }
    window.localStorage.setItem(key, JSON.stringify({ ...snapshot, messages: snapshot.messages.slice(-80) }))
  } catch {
    try {
      window.localStorage.setItem(key, JSON.stringify({ ...snapshot, messages: snapshot.messages.slice(-24), proposal: null }))
    } catch {
      // Local persistence is best effort; the backend still records the exchange in history.
    }
  }
}

function planningHistoryMessages(entries: HistoryEntry[], cutoff = ''): { messages: BuilderMessage[]; threadID: string } {
  const messages: BuilderMessage[] = []
  let nextID = 1
  let threadID = ''
  const cutoffTime = cutoff ? Date.parse(cutoff) : Number.NaN
  const sorted = [...entries].sort((left, right) => Date.parse(left.createdAt) - Date.parse(right.createdAt))
  for (const entry of sorted) {
    if (Number.isFinite(cutoffTime) && Date.parse(entry.createdAt) <= cutoffTime) continue
    const role = entry.kind === 'planning_prompt' ? 'user' : entry.kind === 'planning_response' ? 'assistant' : undefined
    if (!role || !entry.content) continue
    const metadata = entry.metadata ?? {}
    const metadataThreadID = typeof metadata.threadID === 'string' ? metadata.threadID : ''
    if (metadataThreadID) threadID = metadataThreadID
    const modelID = typeof metadata.modelID === 'string' ? metadata.modelID : undefined
    const reasoningEffort = typeof metadata.reasoningEffort === 'string' ? metadata.reasoningEffort : undefined
    messages.push({
      id: nextID++,
      role,
      text: entry.content,
      scope: 'planning',
      request: role === 'user' ? { prompt: entry.content, mode: 'planning', modelID, reasoningEffort } : { prompt: '', mode: 'planning', modelID, reasoningEffort },
    })
  }
  return { messages, threadID }
}

type CodexBlock =
  | { kind: 'paragraph'; lines: string[] }
  | { kind: 'heading'; level: 1 | 2 | 3; text: string }
  | { kind: 'quote'; lines: string[] }
  | { kind: 'list'; ordered: boolean; start?: number; items: string[] }
  | { kind: 'code'; language: string; code: string }

const fencedCodeStart = /^ {0,3}```\s*([\w.+-]*)\s*$/
const fencedCodeEnd = /^ {0,3}```\s*$/
const unorderedListItem = /^\s*[-*+]\s+(.+)$/
const orderedListItem = /^\s*(\d+)[.)]\s+(.+)$/

function isCodexBlockStart(line: string): boolean {
  return fencedCodeStart.test(line)
    || /^(#{1,3})\s+/.test(line)
    || /^\s*>\s?/.test(line)
    || unorderedListItem.test(line)
    || orderedListItem.test(line)
}

function parseCodexBlocks(source: string): CodexBlock[] {
  const lines = source.replace(/\r\n?/g, '\n').split('\n')
  const blocks: CodexBlock[] = []
  let index = 0

  while (index < lines.length) {
    const line = lines[index]
    if (!line.trim()) {
      index += 1
      continue
    }

    const fence = line.match(fencedCodeStart)
    if (fence) {
      index += 1
      const code: string[] = []
      while (index < lines.length && !fencedCodeEnd.test(lines[index])) {
        code.push(lines[index])
        index += 1
      }
      if (index < lines.length) index += 1
      blocks.push({ kind: 'code', language: fence[1] || 'text', code: code.join('\n') })
      continue
    }

    const quote = line.match(/^\s*>\s?(.*)$/)
    if (quote) {
      const quoteLines = [quote[1]]
      index += 1
      while (index < lines.length) {
        const nextQuote = lines[index].match(/^\s*>\s?(.*)$/)
        if (!nextQuote) break
        quoteLines.push(nextQuote[1])
        index += 1
      }
      blocks.push({ kind: 'quote', lines: quoteLines })
      continue
    }

    const heading = line.match(/^(#{1,3})\s+(.+)$/)
    if (heading) {
      blocks.push({ kind: 'heading', level: heading[1].length as 1 | 2 | 3, text: heading[2] })
      index += 1
      continue
    }

    const unordered = line.match(unorderedListItem)
    const ordered = line.match(orderedListItem)
    if (unordered || ordered) {
      const isOrdered = Boolean(ordered)
      const items: string[] = []
      const start = ordered ? Number(ordered[1]) : undefined

      while (index < lines.length) {
        const current = lines[index]
        const item = isOrdered ? current.match(orderedListItem) : current.match(unorderedListItem)
        if (!item) break
        items.push(isOrdered ? item[2] : item[1])
        index += 1

        while (index < lines.length && lines[index].trim() && !isCodexBlockStart(lines[index])) {
          items[items.length - 1] += `\n${lines[index].trim()}`
          index += 1
        }
      }

      blocks.push({ kind: 'list', ordered: isOrdered, start, items })
      continue
    }

    const paragraph = [line]
    index += 1
    while (index < lines.length && lines[index].trim() && !isCodexBlockStart(lines[index])) {
      paragraph.push(lines[index])
      index += 1
    }
    blocks.push({ kind: 'paragraph', lines: paragraph })
  }

  return blocks
}

function renderCodexInline(value: string, keyPrefix: string): ReactNode[] {
  const nodes: ReactNode[] = []
  const pattern = /(\[[^\]\n]+\]\(https?:\/\/[^\s)]+\)|`[^`\n]+`|\*\*[^*\n]+\*\*|__[^_\n]+__|\*[^*\n]+\*|_[^_\n]+_)/g
  let cursor = 0
  let match: RegExpExecArray | null
  let tokenIndex = 0

  while ((match = pattern.exec(value)) !== null) {
    if (match.index > cursor) nodes.push(value.slice(cursor, match.index))
    const token = match[0]
    const key = `${keyPrefix}-${tokenIndex}`
    const link = token.match(/^\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)$/)
    if (link) {
      nodes.push(<a className="codex-inline-link" href={link[2]} target="_blank" rel="noreferrer" key={key}>{renderCodexInline(link[1], `${key}-link`)}</a>)
    } else if (token.startsWith('`')) {
      nodes.push(<code className="codex-inline-code" key={key}>{token.slice(1, -1)}</code>)
    } else if (token.startsWith('**') || token.startsWith('__')) {
      nodes.push(<strong key={key}>{token.slice(2, -2)}</strong>)
    } else {
      nodes.push(<em key={key}>{token.slice(1, -1)}</em>)
    }
    cursor = match.index + token.length
    tokenIndex += 1
  }

  if (cursor < value.length) nodes.push(value.slice(cursor))
  return nodes.length > 0 ? nodes : [value]
}

function renderCodexLines(lines: string[], keyPrefix: string): ReactNode[] {
  return lines.flatMap((line, index) => [
    ...renderCodexInline(line, `${keyPrefix}-${index}`),
    ...(index < lines.length - 1 ? [<br key={`${keyPrefix}-break-${index}`} />] : []),
  ])
}

function CodexResponse({ text, messageID, copiedCodeID, onCopyCode }: { text: string; messageID: number; copiedCodeID: string; onCopyCode: (code: string, codeID: string) => void }) {
  const blocks = parseCodexBlocks(text)
  return <div className="codex-richtext">
    {blocks.map((block, index) => {
      const key = `codex-block-${index}`
      if (block.kind === 'code') {
        const codeID = `${messageID}-${key}`
        return <div className="codex-code-block" key={key}>
          <div className="codex-code-toolbar"><span>{block.language}</span><button type="button" className="codex-copy-button" onClick={() => onCopyCode(block.code, codeID)} aria-label={`Copy ${block.language} code`}>{copiedCodeID === codeID ? 'Copied' : 'Copy'}</button></div>
          <pre><code>{block.code}</code></pre>
        </div>
      }
      if (block.kind === 'heading') {
        const Heading = `h${block.level}` as 'h1' | 'h2' | 'h3'
        return <Heading className="codex-response-heading" key={key}>{renderCodexInline(block.text, key)}</Heading>
      }
      if (block.kind === 'list') {
        const items = block.items.map((item, itemIndex) => <li key={`${key}-item-${itemIndex}`}>{renderCodexLines(item.split('\n'), `${key}-item-${itemIndex}`)}</li>)
        return block.ordered
          ? <ol className="codex-response-list" start={block.start} key={key}>{items}</ol>
          : <ul className="codex-response-list" key={key}>{items}</ul>
      }
      if (block.kind === 'quote') {
        return <blockquote className="codex-response-quote" key={key}>{renderCodexLines(block.lines, key)}</blockquote>
      }
      return <p className="codex-response-paragraph" key={key}>{renderCodexLines(block.lines, key)}</p>
    })}
  </div>
}

function planningBrief(messages: BuilderMessage[], project: Project): string {
  const planningMessages = messages.filter((message) => (message.scope === 'planning' || message.request?.mode === 'planning') && message.role !== 'system')
  const source = planningMessages.length > 0 ? planningMessages : messages.filter((message) => message.role !== 'system')
  const firstBrief = source.find((message) => message.role === 'user')
  const recent = source.slice(-14)
  const selected = firstBrief
    ? [firstBrief, ...recent.filter((message) => message.id !== firstBrief.id)]
    : recent
  const formatMessage = (message: BuilderMessage): string => {
    const role = message.role === 'user' ? 'User' : message.role === 'assistant' ? 'Planning lead' : 'Centurion'
    return `${role}:\n${message.text}`
  }
  const originalBrief = firstBrief ? `Original user brief:\n${formatMessage(firstBrief)}` : ''
  const recentTranscript = selected
    .filter((message) => message.id !== firstBrief?.id)
    .map(formatMessage)
    .join('\n\n')
  // This crosses into a brand-new Builder thread, so it is not covered by the
  // Planning thread's retained context. Keep the original goal plus recent
  // decisions, but avoid paying to resend an entire long planning transcript.
  const maxTranscriptBytes = 16 * 1024
  const originalBudget = 4 * 1024
  const boundedOriginal = truncateUtf8(originalBrief, originalBudget)
  const separator = boundedOriginal ? '\n\n[Older planning details omitted for token efficiency]\n\n' : ''
  const remainingBytes = Math.max(0, maxTranscriptBytes - new TextEncoder().encode(boundedOriginal + separator).length)
  const boundedRecent = truncateUtf8End(recentTranscript, remainingBytes)
  const boundedTranscript = [boundedOriginal, boundedRecent].filter(Boolean).join(separator)
  return `Prepare a Centurion execution proposal for the active project "${project.name}" from the approved planning conversation below. Preserve the original user outcome, agreed constraints, acceptance criteria, agent responsibilities, approval boundaries, and handoffs. Ignore stale error messages, discarded proposals, and UI diagnostics as requirements. Create only the agents and minimal connected workflow needed to execute the plan. If the scope is not ready or a safe implementation cannot be inferred, report a blocking note instead of claiming that code or configuration is ready. This is the handoff from planning to the configuration builder. Return a reviewable proposal draft; do not claim that anything was saved.\n\n<approved_planning_conversation>\n${boundedTranscript}\n</approved_planning_conversation>`
}

function truncateUtf8(value: string, maxBytes: number): string {
  if (maxBytes <= 0 || value === '') return ''
  const encoder = new TextEncoder()
  if (encoder.encode(value).length <= maxBytes) return value
  let low = 0
  let high = value.length
  while (low < high) {
    const middle = Math.ceil((low + high) / 2)
    if (encoder.encode(value.slice(0, middle)).length <= maxBytes) low = middle
    else high = middle - 1
  }
  return value.slice(0, low)
}

function truncateUtf8End(value: string, maxBytes: number): string {
  if (maxBytes <= 0 || value === '') return ''
  const encoder = new TextEncoder()
  if (encoder.encode(value).length <= maxBytes) return value
  let low = 0
  let high = value.length
  while (low < high) {
    const middle = Math.ceil((low + high) / 2)
    if (encoder.encode(value.slice(value.length - middle)).length <= maxBytes) low = middle
    else high = middle - 1
  }
  return value.slice(value.length - low)
}

function BuilderView({
  project,
  agents,
  models,
  auth,
  onApply,
  onOpenWorkflow,
  onStartWorkflow,
  onNotice,
}: {
  project?: Project
  agents: AgentProfile[]
  models: ModelInfo[]
  auth: AuthState
  onApply: (proposal: BuilderProposal) => Promise<BuilderApplyResult>
  onOpenWorkflow: () => void
  onStartWorkflow: (workflow: WorkflowDefinition) => Promise<void>
  onNotice: (message: string) => void
}) {
  const [mode, setMode] = useState<BuilderMode>('planning')
  const [prompt, setPrompt] = useState('')
  const [planningThreadID, setPlanningThreadID] = useState('')
  const [builderThreadID, setBuilderThreadID] = useState('')
  const [plannerAgentID, setPlannerAgentID] = useState('')
  const [messages, setMessages] = useState<BuilderMessage[]>([])
  const [proposal, setProposal] = useState<BuilderProposal | null>(null)
  const [selectedModelID, setSelectedModelID] = useState('')
  const [selectedEffort, setSelectedEffort] = useState('')
  const [subagentApprovalProfile, setSubagentApprovalProfile] = useState('on_request')
  const [busy, setBusy] = useState(false)
  const [liveActivity, setLiveActivity] = useState<BuilderActivityEvent | null>(null)
  const [remoteBuilderStatus, setRemoteBuilderStatus] = useState<BuilderStatus | null>(null)
  const [builderStatusLoading, setBuilderStatusLoading] = useState(true)
  const [confirming, setConfirming] = useState(false)
  const [planningHistoryCutoff, setPlanningHistoryCutoff] = useState('')
  const [clearTarget, setClearTarget] = useState<BuilderMode | null>(null)
  const [expandedMessages, setExpandedMessages] = useState<Set<number>>(new Set())
  const [copiedID, setCopiedID] = useState('')
  const [showJumpToLatest, setShowJumpToLatest] = useState(false)
  const messageID = useRef(0)
  const plannerSelectionInitialized = useRef(false)
  const hydratedSessionProjectID = useRef('')
  const sessionStateRef = useRef<BuilderSessionSnapshot | null>(null)
  const messagesScrollRef = useRef<HTMLDivElement>(null)
  const followTranscript = useRef(true)
  const activeRequestMode = useRef<BuilderMode>('planning')
  const activityHeartbeat = useRef(0)
  const messagesRef = useRef<BuilderMessage[]>([])
  const restoredBuilderRequestID = useRef('')
  const localOperation = useRef<'builder' | 'apply' | ''>('')

  messagesRef.current = messages

  sessionStateRef.current = project?.id ? {
    version: 3,
    projectID: project.id,
    mode,
    prompt,
    planningThreadID,
    builderThreadID,
    plannerAgentID,
    messages,
    proposal,
    selectedModelID,
    selectedEffort,
    subagentApprovalProfile,
    confirming,
    planningHistoryCutoff,
  } : null

  const selectedModel = selectedModelID ? models.find((model) => model.id === selectedModelID) : undefined
  const selectedPlanner = agents.find((agent) => agent.id === plannerAgentID)
  const efforts = selectedModel?.supportedReasoningEfforts ?? []
  const examples = mode === 'planning'
    ? [
      'I need a delivery team for a multi-project platform. Help me define the milestones, risks, and agent handoffs first.',
      'Think through how a tech lead, researcher, backend, and frontend agent should collaborate while keeping sensitive actions behind approval.',
      'Help me turn this idea into a small first release with clear acceptance criteria and a workflow we can expand later.',
    ]
    : [
      'Create the agents and workflow from the agreed plan, keeping permissions minimal and approvals explicit.',
      'Create a tech lead, backend, and frontend team for this project, then connect them in a delivery workflow.',
      'Build a workflow that runs documentation and implementation in parallel, then joins for a review.',
    ]
  const hasAssistantReply = messages.some((message) => message.role === 'assistant')

  useEffect(() => {
    if (models.length === 0 || !selectedModelID || selectedModel) return
    // A model can disappear between sessions. Reset to the account-level
    // Codex configuration instead of allowing a stale explicit ID to be sent.
    setSelectedModelID('')
    setSelectedEffort('')
  }, [models, selectedModelID, selectedModel])

  useEffect(() => {
    const cleanup = Events.On('builder.activity', (event) => {
      const payload = eventValue<BuilderActivityEvent>(event)
      if (!payload || typeof payload !== 'object' || (payload.scope !== 'planning' && payload.scope !== 'building')) return
      if (!busy || payload.scope !== activeRequestMode.current) return
      activityHeartbeat.current = Date.now()
      setLiveActivity(payload)
    })
    return cleanup
  }, [busy])

  useEffect(() => {
    if (!busy) {
      setLiveActivity(null)
      return
    }
    let nextIndex = 0
    const interval = window.setInterval(() => {
      if (Date.now() - activityHeartbeat.current < 5000) return
      const requestMode = activeRequestMode.current
      const activity = builderFallbackActivities[requestMode][nextIndex % builderFallbackActivities[requestMode].length]
      nextIndex += 1
      setLiveActivity({
        schemaVersion: 1,
        timestamp: new Date().toISOString(),
        sequence: Date.now(),
        source: 'centurion',
        scope: requestMode,
        state: 'working',
        activity,
        detail: 'Waiting for the next Codex update.',
      })
    }, 3500)
    return () => window.clearInterval(interval)
  }, [busy])

  useEffect(() => {
    if (plannerSelectionInitialized.current || agents.length === 0) return
    if (plannerAgentID) {
      plannerSelectionInitialized.current = true
      return
    }
    const preferred = agents.find((agent) => /lead|supervisor|architect|planner/i.test(`${agent.name} ${agent.role}`)) ?? agents[0]
    setPlannerAgentID(preferred.id)
    plannerSelectionInitialized.current = true
  }, [agents, plannerAgentID])

  useEffect(() => {
    const projectID = project?.id
    if (!projectID) {
      hydratedSessionProjectID.current = ''
      return
    }

    const snapshot = readBuilderSession(projectID)
    hydratedSessionProjectID.current = projectID
    plannerSelectionInitialized.current = Boolean(snapshot?.plannerAgentID)
    if (snapshot) {
      setMode(snapshot.mode)
      setPrompt(snapshot.prompt)
      setPlanningThreadID(snapshot.planningThreadID)
      setBuilderThreadID(snapshot.builderThreadID)
      setPlannerAgentID(snapshot.plannerAgentID)
      setMessages(snapshot.messages)
      setProposal(snapshot.proposal)
      setSelectedModelID(snapshot.selectedModelID)
      setSelectedEffort(snapshot.selectedEffort)
      setSubagentApprovalProfile(snapshot.subagentApprovalProfile)
      setConfirming(snapshot.confirming)
      setPlanningHistoryCutoff(snapshot.planningHistoryCutoff)
      messageID.current = snapshot.messages.reduce((highest, message) => Math.max(highest, message.id), 0)
    } else {
      setMode('planning')
      setPrompt('')
      setPlanningThreadID('')
      setBuilderThreadID('')
      setPlannerAgentID('')
      setMessages([])
      setProposal(null)
      setSelectedModelID('')
      setSelectedEffort('')
      setSubagentApprovalProfile('on_request')
      setConfirming(false)
      setPlanningHistoryCutoff('')
      messageID.current = 0
    }
    setClearTarget(null)
    setExpandedMessages(new Set())
    setCopiedID('')
    followTranscript.current = true
    setShowJumpToLatest(false)

    void api.listHistory({ projectID, kind: 'planning', limit: 200 }).then((entries) => {
      if (hydratedSessionProjectID.current !== projectID) return
      const activeCutoff = sessionStateRef.current?.projectID === projectID
        ? sessionStateRef.current.planningHistoryCutoff
        : snapshot?.planningHistoryCutoff ?? ''
      const historySession = planningHistoryMessages(entries, activeCutoff)
      if (historySession.messages.length === 0) return
      setMessages((current) => {
        const merged = [...current]
        for (const historyMessage of historySession.messages) {
          if (merged.some((message) => message.role === historyMessage.role && message.text === historyMessage.text)) continue
          const nextID = Math.max(messageID.current, ...merged.map((message) => message.id), 0) + 1
          messageID.current = nextID
          merged.push({ ...historyMessage, id: nextID })
        }
        return merged.slice(-80)
      })
      // History is still useful for restoring the transcript, but it must not
      // silently resurrect an old thread whose model may differ from the
      // current account configuration. Only an explicitly persisted session
      // thread is safe to resume.
      if (snapshot?.planningThreadID && historySession.threadID) setPlanningThreadID((current) => current || historySession.threadID)
    }).catch(() => undefined)
  }, [project?.id])

  useEffect(() => {
    const projectID = project?.id
    if (!projectID) return
    const timeout = window.setTimeout(() => {
      if (hydratedSessionProjectID.current !== projectID) return
      const snapshot = sessionStateRef.current
      if (snapshot?.projectID === projectID) persistBuilderSession(snapshot)
    }, 180)
    return () => window.clearTimeout(timeout)
  }, [project?.id, mode, prompt, planningThreadID, builderThreadID, plannerAgentID, messages, proposal, selectedModelID, selectedEffort, subagentApprovalProfile, confirming, planningHistoryCutoff])

  useEffect(() => {
    const container = messagesScrollRef.current
    if (!container || !followTranscript.current) return
    const frame = window.requestAnimationFrame(() => {
      container.scrollTop = container.scrollHeight
      setShowJumpToLatest(false)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [messages.length, busy, expandedMessages])

  const appendMessage = (role: BuilderMessage['role'], text: string, request?: BuilderRequestMeta, scope: BuilderMode = request?.mode ?? mode) => {
    messageID.current += 1
    setMessages((current) => [...current, { id: messageID.current, role, text, request, scope }])
  }

  useEffect(() => {
    const projectID = project?.id
    if (!projectID) {
      setRemoteBuilderStatus(null)
      setBuilderStatusLoading(false)
      return
    }

    let mounted = true
    setBuilderStatusLoading(true)

    const syncBuilderStatus = async () => {
      try {
        const status = await api.getBuilderStatus()
        if (!mounted) return
        setRemoteBuilderStatus(status)
        setBuilderStatusLoading(false)

        // A single AppService can serve more than one project. Do not move a
        // proposal into the wrong project when the user switches projects.
        if (status.projectID && status.projectID !== projectID) return

        const scope: BuilderMode = status.scope === 'building' ? 'building' : 'planning'
        activeRequestMode.current = scope
        if (status.state === 'working') {
          localOperation.current = 'builder'
          activityHeartbeat.current = Date.now()
          setBusy(true)
          setLiveActivity({
            schemaVersion: status.schemaVersion,
            timestamp: status.timestamp,
            sequence: status.sequence,
            source: status.source,
            scope,
            threadID: status.threadID,
            turnID: status.turnID,
            state: 'working',
            activity: status.activity || 'Working on the request',
            detail: status.detail || 'The Codex turn is active.',
          })
          return
        }

        if (status.state === 'completed') {
          if (localOperation.current !== 'apply') setBusy(false)
          if (!status.requestID || restoredBuilderRequestID.current === status.requestID) return
          restoredBuilderRequestID.current = status.requestID

          if (scope === 'building' && status.builderResponse) {
            const response = status.builderResponse
            setMode('building')
            setBuilderThreadID(response.threadID)
            setPlanningThreadID('')
            setProposal(response.proposal)
            setConfirming(false)
            const text = response.reply.trim() || response.proposal.summary.trim()
            if (text && !messagesRef.current.some((message) => message.role === 'assistant' && message.scope === 'building' && message.text === text)) {
              appendMessage('assistant', text, { prompt: 'Recovered Builder response', mode: 'building' }, 'building')
            }
          } else if (scope === 'planning' && status.planningResponse) {
            const response = status.planningResponse
            setMode('planning')
            setPlanningThreadID(response.threadID)
            const text = response.reply.trim()
            if (text && !messagesRef.current.some((message) => message.role === 'assistant' && message.scope === 'planning' && message.text === text)) {
              appendMessage('assistant', text, { prompt: 'Recovered planning response', mode: 'planning' }, 'planning')
            }
          }
          return
        }

        if (status.state === 'error' && localOperation.current !== 'apply') {
          setBusy(false)
          setLiveActivity({
            schemaVersion: status.schemaVersion,
            timestamp: status.timestamp,
            sequence: status.sequence,
            source: status.source,
            scope,
            threadID: status.threadID,
            turnID: status.turnID,
            state: 'error',
            activity: status.activity || 'Request stopped',
            detail: status.error || status.detail || 'Codex could not complete this request.',
          })
        }
      } catch {
        if (mounted) setBuilderStatusLoading(false)
      }
    }

    void syncBuilderStatus()
    const interval = window.setInterval(() => void syncBuilderStatus(), 1200)
    return () => {
      mounted = false
      window.clearInterval(interval)
    }
  }, [project?.id])

  const handleMessagesScroll = () => {
    const container = messagesScrollRef.current
    if (!container) return
    const atBottom = container.scrollHeight - container.clientHeight - container.scrollTop <= 42
    followTranscript.current = atBottom
    setShowJumpToLatest(!atBottom)
  }

  const jumpToLatest = () => {
    const container = messagesScrollRef.current
    if (!container) return
    followTranscript.current = true
    setShowJumpToLatest(false)
    container.scrollTo({ top: container.scrollHeight, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
  }

  const copyValue = async (value: string, copyID: string) => {
    const copied = await copyToClipboard(value)
    if (!copied) {
      onNotice('Could not copy this response to the clipboard.')
      return
    }
    setCopiedID(copyID)
    window.setTimeout(() => setCopiedID((current) => current === copyID ? '' : current), 1600)
  }

  const toggleExpanded = (id: number) => {
    setExpandedMessages((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const submit = async (rawValue: string, options: { recordUser?: boolean; requestMode?: BuilderMode } = {}) => {
    const value = rawValue.trim()
    const requestMode = options.requestMode ?? mode
    if (!value || busy) return
    if (auth.status !== 'logged_in') {
      onNotice('Codex is not authenticated. Sign in through the Codex CLI; Centurion does not manage account login.')
      return
    }
    setPrompt('')
    setProposal(null)
    setConfirming(false)
    setRemoteBuilderStatus(null)
    localOperation.current = 'builder'
    activeRequestMode.current = requestMode
    activityHeartbeat.current = Date.now()
    setLiveActivity({
      schemaVersion: 1,
      timestamp: new Date().toISOString(),
      sequence: Date.now(),
      source: 'centurion',
      scope: requestMode,
      state: 'working',
      activity: requestMode === 'planning' ? 'Starting the planning turn' : 'Preparing the build',
      detail: 'Connecting the request to Codex.',
    })
    setBusy(true)
    const startedAt = performance.now()
    try {
      const userRequest: BuilderRequestMeta = {
        prompt: value,
        mode: requestMode,
        modelID: selectedModelID || undefined,
        reasoningEffort: selectedEffort || undefined,
        subagentApprovalProfile,
      }
      const requestMeta = (durationMs: number): BuilderRequestMeta => ({
        prompt: value,
        mode: requestMode,
        modelID: selectedModelID || undefined,
        reasoningEffort: selectedEffort || undefined,
        subagentApprovalProfile,
        durationMs,
      })
      if (options.recordUser !== false) appendMessage('user', value, userRequest, requestMode)
      if (requestMode === 'planning') {
        const response = await api.planWithCodex({
          prompt: value,
          projectID: project?.id,
          threadID: planningThreadID || undefined,
          plannerAgentID: plannerAgentID || undefined,
          modelID: selectedModelID || undefined,
          reasoningEffort: selectedEffort || undefined,
        })
        setPlanningThreadID(response.threadID)
        appendMessage('assistant', response.reply || 'The planning lead did not return a message.', requestMeta(Math.round(performance.now() - startedAt)))
      } else {
        const response = await api.generateBuilderProposal({
          prompt: value,
          projectID: project?.id,
          threadID: builderThreadID || undefined,
          modelID: selectedModelID || undefined,
          reasoningEffort: selectedEffort || undefined,
          subagentApprovalProfile,
        })
        setBuilderThreadID(response.threadID)
        setProposal(response.proposal)
        appendMessage('assistant', response.reply || response.proposal.summary, requestMeta(Math.round(performance.now() - startedAt)))
      }
    } catch (error) {
      appendMessage('system', errorText(error))
      onNotice(errorText(error))
    } finally {
      localOperation.current = ''
      setBusy(false)
    }
  }

  const send = async () => {
    await submit(prompt)
  }

  const regenerateResponse = async (message: BuilderMessage) => {
    if (!message.request) return
    setMode(message.request.mode)
    await submit(message.request.prompt, { recordUser: false, requestMode: message.request.mode })
  }

  const continueResponse = async (message: BuilderMessage) => {
    if (!message.request) return
    setMode(message.request.mode)
    await submit('Continue the previous response from where it stopped. Do not repeat content already provided; finish the remaining useful details.', { requestMode: message.request.mode })
  }

  const prepareBuild = async () => {
    if (!project || !hasAssistantReply || busy) return
    if (auth.status !== 'logged_in') {
      onNotice('Codex is not authenticated. Sign in through the Codex CLI; Centurion does not manage account login.')
      return
    }
    setBusy(true)
    setRemoteBuilderStatus(null)
    localOperation.current = 'builder'
    activeRequestMode.current = 'building'
    activityHeartbeat.current = Date.now()
    setLiveActivity({
      schemaVersion: 1,
      timestamp: new Date().toISOString(),
      sequence: Date.now(),
      source: 'centurion',
      scope: 'building',
      state: 'working',
      activity: 'Preparing the build',
      detail: 'Transferring the agreed scope to the configuration builder.',
    })
    const handoffPrompt = planningBrief(messages, project)
    const startedAt = performance.now()
    try {
      const response = await api.generateBuilderProposal({
        prompt: handoffPrompt,
        projectID: project.id,
        modelID: selectedModelID || undefined,
        reasoningEffort: selectedEffort || undefined,
        subagentApprovalProfile,
        handoff: true,
      })
      setBuilderThreadID(response.threadID)
      // The builder now owns the execution handoff. Keep the planning transcript
      // for review, but stop reusing the Planner thread and its full context.
      setPlanningThreadID('')
      setProposal(response.proposal)
      setConfirming(false)
      setMode('building')
      appendMessage('assistant', 'The agreed plan is now a reviewable execution proposal. Check the agents, access levels, models, and workflow before applying it.', { prompt: handoffPrompt, mode: 'building', modelID: selectedModelID || undefined, reasoningEffort: selectedEffort || undefined, durationMs: Math.round(performance.now() - startedAt) })
    } catch (error) {
      appendMessage('system', errorText(error))
      onNotice(errorText(error))
    } finally {
      localOperation.current = ''
      setBusy(false)
    }
  }

  const apply = async (runAfterApply = false) => {
    if (!proposal || busy) return
    activeRequestMode.current = 'building'
    localOperation.current = 'apply'
    activityHeartbeat.current = Date.now()
    setLiveActivity({
      schemaVersion: 1,
      timestamp: new Date().toISOString(),
      sequence: Date.now(),
      source: 'centurion',
      scope: 'building',
      state: 'working',
      activity: 'Applying the proposal',
      detail: 'Validating agents, permissions, and workflow boundaries.',
    })
    setBusy(true)
    try {
      const result = await onApply(proposal)
      await api.clearBuilderStatus().catch(() => undefined)
      setRemoteBuilderStatus(null)
      restoredBuilderRequestID.current = ''
      appendMessage('assistant', `Applied ${result.agents.length} agent profile${result.agents.length === 1 ? '' : 's'}${result.workflow ? ' and created the workflow.' : '.'}`)
      // The proposal has been committed; a future builder request should start
      // with a fresh, compact context instead of carrying the old draft thread.
      setBuilderThreadID('')
      setProposal(null)
      setConfirming(false)
      if (result.workflow && runAfterApply) {
        await onStartWorkflow(result.workflow)
      } else if (result.workflow) {
        onOpenWorkflow()
      }
    } catch (error) {
      onNotice(errorText(error))
    } finally {
      localOperation.current = ''
      setBusy(false)
    }
  }

  const discard = () => {
    void api.clearBuilderStatus().catch(() => undefined)
    setRemoteBuilderStatus(null)
    restoredBuilderRequestID.current = ''
    setProposal(null)
    setBuilderThreadID('')
    setConfirming(false)
    setMode('planning')
    appendMessage('system', 'Proposal discarded. Nothing was changed. You can continue refining the plan.')
  }

  const hasPlanningSession = Boolean(planningThreadID || messages.some((message) => message.scope === 'planning' || message.request?.mode === 'planning'))
  const hasBuilderSession = Boolean(builderThreadID || proposal || messages.some((message) => message.scope === 'building' || message.request?.mode === 'building'))

  const clearSession = (target: BuilderMode) => {
    if (busy) return
    void api.clearBuilderStatus().catch(() => undefined)
    setRemoteBuilderStatus(null)
    restoredBuilderRequestID.current = ''
    const cutoff = new Date().toISOString()
    setClearTarget(null)
    setPrompt('')
    setConfirming(false)
    setExpandedMessages(new Set())
    setCopiedID('')
    followTranscript.current = true
    setShowJumpToLatest(false)
    if (target === 'planning') {
      setMode('planning')
      setPlanningThreadID('')
      setBuilderThreadID('')
      setMessages([])
      setProposal(null)
      setPlanningHistoryCutoff(cutoff)
      onNotice('Plan session cleared. The dependent build draft was cleared too. History remains available.')
      return
    }
    setMode('planning')
    setBuilderThreadID('')
    setProposal(null)
    setMessages((current) => current.filter((message) => message.scope !== 'building' && message.request?.mode !== 'building'))
    onNotice('Build session cleared. The planning conversation was kept.')
  }

  const newConversation = () => {
    void api.clearBuilderStatus().catch(() => undefined)
    setRemoteBuilderStatus(null)
    restoredBuilderRequestID.current = ''
    setMode('planning')
    setPlanningThreadID('')
    setBuilderThreadID('')
    setMessages([])
    setProposal(null)
    setConfirming(false)
    setPrompt('')
    setPlanningHistoryCutoff(new Date().toISOString())
    setClearTarget(null)
    setExpandedMessages(new Set())
    setCopiedID('')
    followTranscript.current = true
    setShowJumpToLatest(false)
  }

  const workflow = proposal?.workflow ?? null
  const draftAgentByID = new Map((proposal?.agents ?? []).map((agent) => [agent.temporaryID, agent]))
  const lastAssistantMessageID = [...messages].reverse().find((message) => message.role === 'assistant')?.id
  const activeActivity = liveActivity?.scope === activeRequestMode.current ? liveActivity : null
  const activeActivityMode = activeRequestMode.current
  const fallbackActivity = builderFallbackActivities[activeActivityMode][0]
  const activityLabel = activeActivity?.activity ?? fallbackActivity
  const activityDetail = activeActivity?.detail ?? 'The Codex turn is active.'
  const remoteStatusForProject = remoteBuilderStatus && (!remoteBuilderStatus.projectID || !project?.id || remoteBuilderStatus.projectID === project.id)
    ? remoteBuilderStatus
    : null
  const busyButtonLabel = activeActivity?.activity === 'Applying the proposal'
    ? 'Applying…'
    : activeActivityMode === 'planning' ? 'Planning…' : 'Drafting…'
  const responsePreview = (text: string) => {
    const compact = text.replace(/```[\s\S]*?```/g, '[Code block]').replace(/\s+/g, ' ').trim()
    return compact.length > 520 ? `${compact.slice(0, 520).trimEnd()}…` : compact
  }
  const changeModelSelection = (nextModelID: string) => {
    const changed = nextModelID !== selectedModelID
    const hadThread = Boolean(planningThreadID || builderThreadID)
    const hadProposal = Boolean(proposal)
    if (changed && hadThread) {
      // A Codex thread can retain the model it was created with. Starting a
      // fresh thread is the only safe way to make the new selection—or the
      // account config—authoritative for the next turn.
      setPlanningThreadID('')
      setBuilderThreadID('')
    }
    if (changed && hadProposal) {
      setProposal(null)
      setConfirming(false)
    }
    if (changed && (hadThread || hadProposal)) onNotice(hadProposal
      ? 'Model changed. The previous draft was cleared; generate it again under the new selection.'
      : 'Model changed. The next Codex request will start a new thread.')
    setSelectedModelID(nextModelID)
    setSelectedEffort('')
  }
  const changeEffortSelection = (nextEffort: string) => {
    const changed = nextEffort !== selectedEffort
    const hadThread = Boolean(planningThreadID || builderThreadID)
    const hadProposal = Boolean(proposal)
    if (changed && hadThread) {
      setPlanningThreadID('')
      setBuilderThreadID('')
    }
    if (changed && hadProposal) {
      setProposal(null)
      setConfirming(false)
    }
    if (changed && (hadThread || hadProposal)) onNotice(hadProposal
      ? 'Reasoning effort changed. The previous draft was cleared; generate it again under the new selection.'
      : 'Reasoning effort changed. The next Codex request will start a new thread.')
    setSelectedEffort(nextEffort)
  }
  return (
    <div className="view-stack builder-page">
      <div className="view-heading builder-heading">
        <div><span className="section-kicker">Configuration assistant</span><h1>Build with Codex</h1><p>Talk through the outcome with a planning lead, then turn the agreed scope into agents and a visual workflow.</p></div>
        <div className="builder-heading-meta"><span className={`builder-connection ${auth.status === 'logged_in' ? 'online' : ''}`}><span />{auth.status === 'logged_in' ? 'Codex ready' : 'Codex offline'}</span>{remoteStatusForProject?.state === 'working' && <span className="builder-request-state working"><span />Builder running · {remoteStatusForProject.scope === 'building' ? 'build' : 'plan'}</span>}{remoteStatusForProject?.state === 'error' && <span className="builder-request-state error"><span />Builder stopped</span>}{project && <span className="builder-project-name">{project.name}</span>}<div className="builder-mode-switch" role="tablist" aria-label="Builder mode"><button type="button" role="tab" aria-selected={mode === 'planning'} className={mode === 'planning' ? 'active' : ''} onClick={() => setMode('planning')}><span className="builder-mode-dot" />Plan</button><button type="button" role="tab" aria-selected={mode === 'building'} className={mode === 'building' ? 'active' : ''} onClick={() => setMode('building')}><span className="builder-mode-dot" />Build</button></div></div>
      </div>
      {!project ? <div className="empty-state panel-card"><strong>Choose a project first</strong><span>The planning room uses project folders as the safe workspace boundary.</span></div> : <div className="builder-layout">
        <section className="panel-card builder-chat" aria-label={mode === 'planning' ? 'Codex planning room' : 'Codex builder chat'}>
          <div className="builder-chat-header"><div><span className="eyebrow">{mode === 'planning' ? 'Planning room' : 'Execution handoff'}</span><h2>{mode === 'planning' ? 'Shape the plan together' : 'Review the execution proposal'}</h2></div><div className="builder-chat-header-actions"><span className="builder-chat-scope">{mode === 'planning' ? 'Read-only' : 'Draft only'}</span>{clearTarget ? <div className="builder-clear-confirm" role="alertdialog" aria-label={`Clear ${clearTarget === 'planning' ? 'plan' : 'build'} session`}><span>Clear {clearTarget === 'planning' ? 'plan' : 'build'} session?</span><button type="button" className="button danger-quiet small" onClick={() => clearSession(clearTarget)} disabled={busy}>Clear</button><button type="button" className="button subtle small" onClick={() => setClearTarget(null)} disabled={busy}>Cancel</button></div> : <div className="builder-session-actions" aria-label="Session actions"><button type="button" className="link-button builder-clear-button" onClick={() => setClearTarget('planning')} disabled={busy || !hasPlanningSession}>Clear plan</button><button type="button" className="link-button builder-clear-button" onClick={() => setClearTarget('building')} disabled={busy || !hasBuilderSession}>Clear build</button></div>}<button type="button" className="link-button" onClick={newConversation} disabled={busy}>New chat</button></div></div>
          {messages.length === 0 ? <div className="builder-welcome"><div className="builder-welcome-mark">{mode === 'planning' ? '◎' : '✦'}</div><div><strong>{mode === 'planning' ? 'Start with the outcome and constraints.' : 'Turn the agreement into a build.'}</strong><p>{mode === 'planning' ? 'Explain what you want to accomplish. The planning lead will ask focused questions, surface risks, and add ideas before anything is delegated.' : 'The builder will translate the agreed plan into reviewable agent profiles and a bounded visual workflow.'}</p></div><div className="builder-example-list">{examples.map((example) => <button type="button" key={example} onClick={() => setPrompt(example)}>{example}<span>Use example →</span></button>)}</div></div> : <div className="builder-transcript">
            <div className="builder-messages" ref={messagesScrollRef} onScroll={handleMessagesScroll} aria-live="polite">
              {messages.map((message) => {
                const displayText = message.role === 'user' ? message.text : readableUserText(message.text)
                const isLongResponse = message.role === 'assistant' && (displayText.length > 2400 || displayText.split('\n').length > 36)
				const isCollapsed = isLongResponse && !expandedMessages.has(message.id)
				const model = message.request?.modelID ? models.find((item) => item.id === message.request?.modelID) : undefined
				const modelLabel = message.request?.modelID ? (model?.displayName ?? message.request.modelID) : 'Account default'
				const label = message.role === 'user' ? 'You' : message.role === 'assistant' ? (message.request?.mode === 'planning' ? 'Planning lead' : 'Codex') : 'Centurion'
                return <article className={`builder-message builder-message-${message.role}`} key={message.id}>
                  <div className="builder-message-topline">
                    <span className="builder-message-label">{label}</span>
                    <div className="builder-message-tools">
					  {message.request && <span className="builder-message-meta">{modelLabel} · {message.request.reasoningEffort ?? 'automatic'}{message.request.durationMs ? ` · ${message.request.durationMs}ms` : ''}</span>}
                      <button type="button" className="builder-message-copy" onClick={() => void copyValue(displayText, `message-${message.id}`)} aria-label={`Copy ${label.toLowerCase()} response`}>{copiedID === `message-${message.id}` ? 'Copied' : 'Copy'}</button>
                    </div>
                  </div>
                  <div className={`builder-message-body ${isCollapsed ? 'collapsed' : ''}`}>
                    {isCollapsed ? <p className="codex-response-preview">{responsePreview(displayText)}</p> : <CodexResponse text={displayText} messageID={message.id} copiedCodeID={copiedID} onCopyCode={(code, codeID) => void copyValue(code, codeID)} />}
                    {isLongResponse && <button type="button" className="builder-expand-button" onClick={() => toggleExpanded(message.id)}>{isCollapsed ? 'Show full response' : 'Collapse response'}</button>}
                  </div>
                  {message.role === 'assistant' && message.request && message.id === lastAssistantMessageID && <div className="builder-reply-actions"><button type="button" className="builder-reply-action" onClick={() => void regenerateResponse(message)} disabled={busy}>Regenerate</button><button type="button" className="builder-reply-action" onClick={() => void continueResponse(message)} disabled={busy}>Continue</button></div>}
                </article>
              })}
              {busy && <div className={`builder-thinking builder-thinking-${activeActivity?.state === 'error' ? 'error' : 'working'}`} role="status" aria-live="polite"><span className="builder-thinking-indicator" aria-hidden="true"><span className="builder-thinking-dot" /></span><span className="builder-thinking-copy"><strong>{activityLabel}</strong><small>{activityDetail}</small></span><span className="builder-thinking-tag">Live activity</span></div>}
            </div>
            {showJumpToLatest && <button type="button" className="builder-jump-latest" onClick={jumpToLatest}><span aria-hidden="true">↓</span>New response</button>}
          </div>}
          {remoteStatusForProject?.state === 'working' && <div className="builder-persisted-status" role="status" aria-live="polite"><span className="builder-persisted-status-indicator" /><div><strong>{remoteStatusForProject.activity || 'Builder request in progress'}</strong><small>{remoteStatusForProject.detail || 'This request is still running in the background. You can stay on this screen or navigate away and return.'}</small></div><span className="builder-persisted-status-tag">Reconnected</span></div>}{remoteStatusForProject?.state === 'error' && <div className="builder-persisted-status error" role="alert"><span className="builder-persisted-status-indicator" /><div><strong>{remoteStatusForProject.activity || 'Builder request stopped'}</strong><small>{remoteStatusForProject.error || remoteStatusForProject.detail || 'No proposal was produced.'}</small></div></div>}<div className="builder-composer"><div className="builder-composer-label"><span>{mode === 'planning' ? 'Planning brief' : 'Build request'}</span><span>Ctrl+Enter</span></div><textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} onKeyDown={(event) => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') { event.preventDefault(); void send() } }} placeholder={mode === 'planning' ? 'Explain the outcome, constraints, and open questions...' : 'Describe the agents and workflow to create...'} rows={3} aria-label={mode === 'planning' ? 'Explain the project scope to the planning lead' : 'Describe the agents and workflow to create'} /><div className="builder-composer-footer"><span>{mode === 'planning' ? 'Read-only conversation · no tools or file changes.' : 'No changes are made until you apply a proposal.'}</span><div className="builder-composer-actions">{mode === 'planning' && hasAssistantReply && <button type="button" className="button subtle" onClick={() => void prepareBuild()} disabled={busy || builderStatusLoading}>Build from plan</button>}<button type="button" className="button primary" onClick={() => void send()} disabled={busy || !prompt.trim() || builderStatusLoading}>{busy ? busyButtonLabel : mode === 'planning' ? 'Discuss' : 'Ask Codex'}</button></div></div></div>
        </section>
        <aside className="builder-side">
           <section className="panel-card builder-controls"><div className="card-topline"><span className="eyebrow">{mode === 'planning' ? 'Planning lead' : 'Builder model'}</span><span className="mini-code">{mode === 'planning' ? 'read-only' : 'config-aware'}</span></div>{mode === 'planning' && <label className="field"><span>Agent</span><select value={plannerAgentID} onChange={(event) => setPlannerAgentID(event.target.value)} disabled={busy || Boolean(planningThreadID)}><option value="">Use Centurion planning lead</option>{agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name} · {agent.role}</option>)}</select><span className="field-hint">{selectedPlanner ? `${selectedPlanner.name}'s role and instructions guide the conversation. Workspace and tools stay disabled.` : 'No agent profile is available yet, so Centurion uses a read-only planning lead.'}</span></label>}<label className="field"><span>Model</span><select value={selectedModelID} onChange={(event) => changeModelSelection(event.target.value)} disabled={busy || builderStatusLoading}><option value="">Account default</option>{models.map((model) => <option key={model.id} value={model.id}>{model.displayName}</option>)}</select></label><label className="field"><span>Reasoning effort</span><select value={selectedEffort} onChange={(event) => changeEffortSelection(event.target.value)} disabled={busy || builderStatusLoading}><option value="">Automatic</option>{efforts.map((effort) => <option key={effort.reasoningEffort} value={effort.reasoningEffort}>{effort.reasoningEffort}</option>)}</select></label><div className="field"><span>Subagent Permissions</span><div className="access-picker" role="radiogroup" aria-label="Subagent permissions"><button type="button" className={subagentApprovalProfile === 'autonomous' ? 'active' : ''} onClick={() => setSubagentApprovalProfile('autonomous')} disabled={busy || builderStatusLoading}><strong>Full access</strong><span>Run generated agents without intermediate approval.</span></button><button type="button" className={subagentApprovalProfile !== 'autonomous' ? 'active' : ''} onClick={() => setSubagentApprovalProfile('on_request')} disabled={busy || builderStatusLoading}><strong>Request approval</strong><span>Pause before sensitive commands, files, or external effects.</span></button></div><span className="field-hint">Applied to every new subagent in the proposal. Workspace boundaries still apply.</span></div><p className="field-hint">Account default leaves model and automatic effort to your Codex config. Changing a selection starts a new Codex thread.</p></section>
          <section className="panel-card builder-proposal"><div className="card-topline"><div><span className="eyebrow">{mode === 'planning' && !proposal ? 'Execution handoff' : 'Proposal preview'}</span><h2>{proposal ? 'Ready to review' : mode === 'planning' ? 'Waiting for agreement' : 'Nothing drafted yet'}</h2></div>{proposal && <span className="builder-draft-badge">Draft</span>}</div>{proposal ? <><p className="builder-proposal-summary">{proposal.summary}</p>{proposal.notes && proposal.notes.length > 0 && <div className="builder-notes"><span className="eyebrow">Notes</span>{proposal.notes.map((note) => <p key={note}>{note}</p>)}</div>}<div className="builder-agent-preview"><div className="builder-preview-heading"><span>Agents</span><strong>{proposal.agents.length}</strong></div>{proposal.agents.map((agent) => { const agentModel = models.find((model) => model.id === agent.modelID); return <div className="builder-agent-row" key={agent.temporaryID}><div className="builder-agent-avatar">{agent.name.slice(0, 1).toUpperCase()}</div><div><strong>{agent.name}</strong><small>{agent.role} · {agentModel?.displayName ?? agent.modelID ?? 'Builder model'}{agent.reasoningEffort ? ` · ${agent.reasoningEffort}` : ''}</small></div><span className={`builder-access ${agent.approvalProfile === 'autonomous' ? 'complete' : ''}`}>{agent.approvalProfile === 'autonomous' ? 'Full access' : 'Approval'}</span></div>})}</div>{workflow && <div className="builder-workflow-preview"><div className="builder-preview-heading"><span>Workflow</span><strong>{workflow.nodes.length} nodes · {workflow.edges.length} connections</strong></div><div className="builder-route">{workflow.nodes.slice(0, 8).map((node, index) => <span key={node.id}><span className={`builder-node-type builder-node-${node.type}`}>{node.type === 'agent' ? (draftAgentByID.get(node.agentID ?? '')?.name ?? 'Agent') : node.type}</span>{index < Math.min(workflow.nodes.length, 8) - 1 && <b>→</b>}</span>)}</div></div>}<div className="builder-proposal-actions">{!confirming ? <><button type="button" className="button primary full" onClick={() => setConfirming(true)} disabled={busy}>Review & apply</button><button type="button" className="button subtle full" onClick={discard} disabled={busy}>Discard</button></> : <div className="builder-confirmation"><strong>This will create {proposal.agents.length} agent profile{proposal.agents.length === 1 ? '' : 's'}{workflow ? ' and save one workflow' : ''}.</strong><span>Centurion will validate the workspace, permissions, graph, and loop limits before committing.</span><div><button type="button" className="button subtle" onClick={() => void apply()} disabled={busy}>Apply only</button>{workflow && <button type="button" className="button primary" onClick={() => void apply(true)} disabled={busy}>{busy ? 'Starting…' : 'Apply & run'}</button>}<button type="button" className="button subtle" onClick={() => setConfirming(false)} disabled={busy}>Back</button></div></div>}</div></> : <div className="builder-proposal-empty"><span>{mode === 'planning' ? 'PLAN' : '01'}</span><p>{mode === 'planning' ? 'After the planning lead and you agree on the scope, use Build from plan to generate the agent and workflow proposal here.' : 'Your draft will appear here with agent roles, access levels, models, and the connected workflow.'}</p></div>}</section>
        </aside>
      </div>}
    </div>
  )
}

function WindowFrame({ auth }: { auth: AuthState }) {
  const [maximized, setMaximized] = useState(false)

  useEffect(() => {
    let mounted = true
    void WailsWindow.IsMaximised().then((value) => {
      if (mounted) setMaximized(value)
    }).catch(() => undefined)
    const cleanups = [
      Events.On('windows:WindowMaximise', () => setMaximized(true)),
      Events.On('windows:WindowUnMaximise', () => setMaximized(false)),
      Events.On('windows:WindowRestore', () => {
        void WailsWindow.IsMaximised().then((value) => {
          if (mounted) setMaximized(value)
        }).catch(() => undefined)
      }),
    ]
    return () => {
      mounted = false
      cleanups.forEach((cleanup) => cleanup())
    }
  }, [])

  return (
    <header className="window-frame" aria-label="Window frame">
      <div className="window-frame-caption">
        <img className="window-frame-mark" src="/centurion-icon.png" alt="" aria-hidden="true" />
        <span className="window-frame-product">CENTURION</span>
        <span className="window-frame-divider" aria-hidden="true">/</span>
        <span className="window-frame-title">Agent Command Center</span>
        <UsageMonitor auth={auth} />
      </div>
      <div className="window-frame-controls" aria-label="Window controls">
        <button className="window-control" type="button" aria-label="Minimize window" title="Minimize" onClick={() => void WailsWindow.Minimise()}>
          <span className="window-glyph minimize" aria-hidden="true" />
        </button>
        <button className="window-control" type="button" aria-label={maximized ? 'Restore window' : 'Maximize window'} title={maximized ? 'Restore' : 'Maximize'} onClick={() => void WailsWindow.ToggleMaximise()}>
          <span className={`window-glyph ${maximized ? 'restore' : 'maximize'}`} aria-hidden="true" />
        </button>
        <button className="window-control close" type="button" aria-label="Close window" title="Close" onClick={() => void WailsWindow.Close()}>
          <span className="window-glyph close" aria-hidden="true" />
        </button>
      </div>
    </header>
  )
}

function App() {
  const [activeView, setActiveView] = useState<View>('projects')
  const [auth, setAuth] = useState<AuthState>({ status: 'checking', updatedAt: '' })
  const [runtimeStatus, setRuntimeStatus] = useState<RuntimeStatus>({ connected: false, codexCommand: 'codex', activeSessions: 0, maxSessions: 0, activeRuns: 0, authStatus: 'checking', updatedAt: '' })
  const [projects, setProjects] = useState<Project[]>([])
  const [activeProjectID, setActiveProjectID] = useState('')
  const [draftProject, setDraftProject] = useState<Project>(defaultProject)
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [auditEntries, setAuditEntries] = useState<AuditEntry[]>([])
  const [historyKind, setHistoryKind] = useState('')
  const [agents, setAgents] = useState<AgentProfile[]>([])
  const [models, setModels] = useState<ModelInfo[]>([])
  const [systemPrompts, setSystemPrompts] = useState<SystemPrompt[]>([])
  const [workflows, setWorkflows] = useState<WorkflowDefinition[]>([])
  const [selectedWorkflowID, setSelectedWorkflowID] = useState('')
  const [runs, setRuns] = useState<Run[]>([])
  const [events, setEvents] = useState<RunEvent[]>([])
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [mcpServers, setMCPServers] = useState<MCPServer[]>([])
  const [selectedAgentID, setSelectedAgentID] = useState('')
  const [officeAgentID, setOfficeAgentID] = useState('')
  const [selectedRunID, setSelectedRunID] = useState('')
  const [runSteps, setRunSteps] = useState<RunStep[]>([])
  const [draftAgent, setDraftAgent] = useState<AgentProfile>(defaultAgent)
  const [draftSchedule, setDraftSchedule] = useState<Schedule>({ id: '', name: 'New routine', workflowID: '', cron: '*/30 * * * *', timezone: 'Local', enabled: true, createdAt: '', updatedAt: '' })
  const [approvals, setApprovals] = useState<Record<string, ApprovalRequest>>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')

  const selectedWorkflow = workflows.find((workflow) => workflow.id === selectedWorkflowID) ?? workflows[0]
  const selectedRun = runs.find((run) => run.id === selectedRunID)
  const activeProject = projects.find((project) => project.id === activeProjectID) ?? projects[0]
  const activeEvents = useMemo(() => events.filter((event) => !selectedRunID || event.runID === selectedRunID).slice(-200), [events, selectedRunID])
  const workingAgents = agents.filter((agent) => agent.visualState !== 'idle')
  const latestRun = runs[0]
  const officeAgent = agents.find((agent) => agent.id === officeAgentID) ?? agents[0]
  const officeRun = selectedRun ?? latestRun
  const officeWorkflow = officeRun ? workflows.find((workflow) => workflow.id === officeRun.workflowID) : selectedWorkflow
  const officeAgentNodeIDs = useMemo(() => new Set((officeWorkflow?.nodes ?? []).filter((node) => node.agentID === officeAgent?.id).map((node) => node.id)), [officeWorkflow, officeAgent?.id])
  const officeAgentSteps = useMemo(() => runSteps.filter((step) => officeAgentNodeIDs.has(step.nodeID)), [runSteps, officeAgentNodeIDs])
  const officeAgentStep = officeAgentSteps[officeAgentSteps.length - 1]
  const officeAgentEvents = useMemo(() => activeEvents.filter((event) => event.agentID === officeAgent?.id).slice(-8).reverse(), [activeEvents, officeAgent?.id])

  const announce = (message: string) => {
    setNotice(message)
    window.setTimeout(() => setNotice((current) => current === message ? '' : current), 3800)
  }

  const refreshRuns = () => { void api.listRuns(activeProjectID).then((nextRuns) => { setRuns(nextRuns); setSelectedRunID((current) => nextRuns.some((run) => run.id === current) ? current : nextRuns[0]?.id ?? '') }).catch((error) => announce(errorText(error))) }
  const refreshHistory = (projectID = activeProjectID, kind = historyKind) => { if (!projectID) return; void api.listHistory({ projectID, kind, limit: 100 }).then(setHistory).catch((error) => announce(errorText(error))) }
  const refreshProjectData = async (projectID: string) => {
    if (!projectID) return
    const [agentsResult, workflowsResult, runsResult, schedulesResult, historyResult, auditResult] = await Promise.allSettled([
      api.listAgents(),
      api.listWorkflows(),
      api.listRuns(projectID),
      api.listSchedules(),
      api.listHistory({ projectID, limit: 100 }),
      api.listAudit({ projectID, limit: 100 }),
    ])
    if (agentsResult.status === 'fulfilled') {
      const nextAgents = agentsResult.value.map(normalizeAgent)
      setAgents(nextAgents)
      setSelectedAgentID(nextAgents[0]?.id ?? '')
      setOfficeAgentID((current) => nextAgents.some((agent) => agent.id === current) ? current : nextAgents[0]?.id ?? '')
    }
    if (workflowsResult.status === 'fulfilled') {
      setWorkflows(workflowsResult.value)
      setSelectedWorkflowID(workflowsResult.value[0]?.id ?? '')
    }
    if (runsResult.status === 'fulfilled') {
      setRuns(runsResult.value)
      setSelectedRunID(runsResult.value[0]?.id ?? '')
    }
    if (schedulesResult.status === 'fulfilled') {
      setSchedules(schedulesResult.value)
      setDraftSchedule(schedulesResult.value[0] ?? { id: '', name: 'New routine', workflowID: '', cron: '*/30 * * * *', timezone: 'Local', enabled: true, createdAt: '', updatedAt: '' })
    }
    if (historyResult.status === 'fulfilled') setHistory(historyResult.value)
    if (auditResult.status === 'fulfilled') setAuditEntries(auditResult.value)
    setEvents([])
  }

  useEffect(() => {
    let mounted = true
    const load = async () => {
      setLoading(true)
      const [authResult, runtimeResult, projectsResult, activeProjectResult, agentsResult, promptsResult, workflowsResult, runsResult, schedulesResult] = await Promise.allSettled([api.getAuthState(), api.getRuntimeStatus(), api.listProjects(), api.getActiveProject(), api.listAgents(), api.listSystemPrompts(), api.listWorkflows(), api.listRuns(), api.listSchedules()])
      if (!mounted) return
      if (authResult.status === 'fulfilled') setAuth(authResult.value)
      if (runtimeResult.status === 'fulfilled') setRuntimeStatus(runtimeResult.value)
      if (projectsResult.status === 'fulfilled') setProjects(projectsResult.value)
      if (activeProjectResult.status === 'fulfilled') {
        setActiveProjectID(activeProjectResult.value.id)
        setDraftProject(activeProjectResult.value)
        setActiveView('office')
      }
      if (agentsResult.status === 'fulfilled') {
        const nextAgents = agentsResult.value.map(normalizeAgent)
        setAgents(nextAgents)
        setSelectedAgentID(nextAgents[0]?.id ?? '')
        setOfficeAgentID(nextAgents[0]?.id ?? '')
      }
      if (promptsResult.status === 'fulfilled') setSystemPrompts(promptsResult.value)
      if (workflowsResult.status === 'fulfilled') { setWorkflows(workflowsResult.value); setSelectedWorkflowID(workflowsResult.value[0]?.id ?? '') }
      if (runsResult.status === 'fulfilled') { setRuns(runsResult.value); setSelectedRunID(runsResult.value[0]?.id ?? '') }
      if (schedulesResult.status === 'fulfilled') { setSchedules(schedulesResult.value); if (schedulesResult.value[0]) setDraftSchedule(schedulesResult.value[0]) }
      setLoading(false)
      if (activeProjectResult.status === 'fulfilled') {
        const projectID = activeProjectResult.value.id
        void api.listHistory({ projectID, limit: 100 }).then(setHistory).catch(() => undefined)
        void api.listAudit({ projectID, limit: 100 }).then(setAuditEntries).catch(() => undefined)
      }
      void Promise.allSettled([api.listModels(), api.listMCPServers()]).then(([modelsResult, mcpResult]) => {
        if (!mounted) return
        if (modelsResult.status === 'fulfilled') setModels(modelsResult.value)
        if (mcpResult.status === 'fulfilled') setMCPServers(mcpResult.value)
      })
    }
    void load()
    const cleanups: (() => void)[] = []
    const listen = (name: string, callback: (value: unknown) => void) => {
      const result = (Events as unknown as { On: (event: string, handler: (value: unknown) => void) => unknown }).On(name, callback)
      if (typeof result === 'function') cleanups.push(result as () => void)
    }
    listen('auth.updated', (value) => setAuth(eventValue<AuthState>(value)))
    listen('models.updated', (value) => { const payload = eventValue<{ models?: ModelInfo[] } | ModelInfo[]>(value); setModels(Array.isArray(payload) ? payload : payload.models ?? []) })
    listen('mcp.status', (value) => { const payload = eventValue<{ servers?: MCPServer[] } | MCPServer[]>(value); setMCPServers(Array.isArray(payload) ? payload : payload.servers ?? []) })
    listen('run.created', (value) => {
      const run = eventValue<Run>(value)
      setRuns((current) => mergeRunSnapshot(current, run))
      setSelectedRunID(run.id)
      // The creation event is intentionally lightweight. Reconcile it with
      // SQLite immediately so a late queued snapshot cannot overwrite a run
      // that already moved to running or completed.
      void api.getRun(run.id).then((latest) => setRuns((current) => mergeRunSnapshot(current, latest))).catch(() => undefined)
    })
    listen('run.updated', () => refreshRuns())
    listen('run.event', (value) => { const event = eventValue<RunEvent>(value); setEvents((current) => [...current.filter((item) => !(item.runID === event.runID && item.sequence === event.sequence)), event].slice(-500)) })
    listen('office.agent.state', (value) => { const state = eventValue<{ agentID: string; state: string }>(value); setAgents((current) => current.map((agent) => agent.id === state.agentID ? { ...agent, visualState: state.state } : agent)) })
    listen('approval.requested', (value) => { const approval = eventValue<ApprovalRequest>(value); setApprovals((current) => ({ ...current, [approval.id]: approval })) })
    listen('approval.resolved', (value) => { const decision = eventValue<{ id: string }>(value); setApprovals((current) => { const next = { ...current }; delete next[decision.id]; return next }) })
    listen('scheduler.updated', () => { void api.listSchedules().then(setSchedules).catch(() => undefined) })
    const interval = window.setInterval(() => {
      refreshRuns()
      void api.getRuntimeStatus().then(setRuntimeStatus).catch(() => undefined)
    }, 7000)
    return () => { mounted = false; window.clearInterval(interval); cleanups.forEach((cleanup) => cleanup()) }
  }, [])

  useEffect(() => {
    if (!selectedRunID) {
      setEvents([])
      setRunSteps([])
      return
    }
    const refreshRunDetails = () => {
      void api.getRunEvents(selectedRunID, 0).then(setEvents).catch(() => undefined)
      void api.getRunSteps(selectedRunID).then(setRunSteps).catch(() => undefined)
    }
    refreshRunDetails()
    const interval = window.setInterval(refreshRunDetails, 2500)
    return () => window.clearInterval(interval)
  }, [selectedRunID])

  useEffect(() => {
    if (activeView === 'history' && activeProjectID) refreshHistory(activeProjectID, historyKind)
  }, [activeView, activeProjectID, historyKind])

  useEffect(() => {
    if (activeProjectID) refreshRuns()
  }, [activeProjectID])

  const saveAgent = async () => {
    setBusy(true)
    try {
      const payload = normalizeAgent(draftAgent)
      const saved = normalizeAgent(payload.id ? await api.updateAgent(payload) : await api.createAgent(payload))
      setAgents((current) => [...current.filter((agent) => agent.id !== saved.id), saved].sort((left, right) => left.name.localeCompare(right.name)))
      setSelectedAgentID(saved.id)
      setOfficeAgentID(saved.id)
      setDraftAgent(saved)
      announce('Agent profile saved.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const createAgent = () => { setDraftAgent({ ...defaultAgent, name: 'New agent' }); setSelectedAgentID(''); setActiveView('agents') }

  const createProject = () => { setDraftProject({ ...defaultProject, name: 'New project' }); setActiveView('projects') }

  const exportProjectSnapshot = async () => {
    if (!activeProjectID || busy) return
    setBusy(true)
    try {
      const path = await api.exportProjectSnapshot(activeProjectID)
      announce(`Project snapshot exported to ${path}`)
    } catch (error) {
      announce(errorText(error))
    } finally {
      setBusy(false)
    }
  }

  const createWorkflow = () => {
    const nodeID = `node-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
    const workflowID = `workflow-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`
    const firstAgent = agents[0]
    const hasAgent = Boolean(firstAgent)
    const workflow: WorkflowDefinition = {
      id: workflowID,
      name: 'New workflow',
      version: 1,
      description: '',
      entryNodeID: nodeID,
      nodes: [{ id: nodeID, type: hasAgent ? 'agent' : 'approval', label: hasAgent ? 'Start with an agent' : 'Start here', agentID: firstAgent?.id, retry: { maxAttempts: 1, backoffSeconds: 0, idempotent: true } }],
      edges: [],
      globalLimits: { maxDurationSeconds: 3600, maxParallel: 2, maxTurns: 24, maxPromptTokens: 12000 },
      errorPolicy: 'stop',
      createdAt: '',
      updatedAt: '',
    }
    setWorkflows((current) => [workflow, ...current])
    setSelectedWorkflowID(workflow.id)
    setActiveView('workflows')
    announce('New workflow created. Add blocks and save it.')
  }

  const deleteWorkflow = async (workflow = selectedWorkflow) => {
    if (!workflow) return
    if (!window.confirm(`Delete workflow “${workflow.name}”? This cannot be undone.`)) return
    setBusy(true)
    try {
      if (workflow.createdAt) await api.deleteWorkflow(workflow.id)
      const nextWorkflows = workflow.createdAt ? await api.listWorkflows() : workflows.filter((item) => item.id !== workflow.id)
      setWorkflows(nextWorkflows)
      setSelectedWorkflowID(nextWorkflows[0]?.id ?? '')
      announce('Workflow deleted.')
    } catch (error) {
      announce(errorText(error))
    } finally {
      setBusy(false)
    }
  }

  const applyBuilderProposal = async (proposal: BuilderProposal): Promise<BuilderApplyResult> => {
    setBusy(true)
    try {
      const result = await api.applyBuilderProposal({ projectID: activeProjectID || undefined, proposal })
      setAgents((current) => [...current, ...result.agents.map(normalizeAgent)].sort((left, right) => left.name.localeCompare(right.name)))
      if (result.workflow) {
        setWorkflows((current) => [result.workflow as WorkflowDefinition, ...current.filter((item) => item.id !== result.workflow?.id)])
        setSelectedWorkflowID(result.workflow.id)
      }
      announce('Codex proposal applied to the project.')
      return result
    } finally {
      setBusy(false)
    }
  }

  const openProject = async (projectID: string) => {
    if (!projectID) return
    setBusy(true)
    try {
      const opened = await api.setActiveProject(projectID)
      setActiveProjectID(opened.id)
      setDraftProject(opened)
      setProjects((current) => [opened, ...current.filter((project) => project.id !== opened.id)])
      await refreshProjectData(opened.id)
      setActiveView('office')
      announce(`Opened ${opened.name}.`)
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const saveProject = async () => {
    setBusy(true)
    try {
      const saved = draftProject.id ? await api.updateProject(draftProject) : await api.createProject(draftProject)
      const opened = await api.setActiveProject(saved.id)
      setProjects((current) => [opened, ...current.filter((project) => project.id !== opened.id)])
      setActiveProjectID(opened.id)
      setDraftProject(opened)
      await refreshProjectData(opened.id)
      setActiveView('office')
      announce('Project saved and opened.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const chooseProjectFolder = async () => {
    try {
      const folder = await api.selectProjectFolder()
      return folder || undefined
    } catch (error) {
      announce(errorText(error))
      return undefined
    }
  }

  const learnProjectSystem = async (project: Project, modelID: string, reasoningEffort: string) => {
    if (!project.id) return
    setBusy(true)
    try {
      const saved = await api.updateProject(project)
      setDraftProject(saved)
      setProjects((current) => [saved, ...current.filter((item) => item.id !== saved.id)])
      const result = await api.learnProjectSystem({ projectID: saved.id, modelID: modelID || undefined, reasoningEffort: reasoningEffort || undefined })
      const missing = result.missingFiles && result.missingFiles.length > 0 ? ` Missing: ${result.missingFiles.join(', ')}.` : ''
      announce(result.status === 'completed' ? `System documentation saved in ${result.docsPath}.` : `System documentation is partial in ${result.docsPath}.${missing}`)
    } catch (error) {
      announce(errorText(error))
      throw error
    } finally {
      setBusy(false)
    }
  }

  const deleteProject = async () => {
    if (!draftProject.id) return
    if (!window.confirm(`Delete project “${draftProject.name}”?`)) return
    setBusy(true)
    try {
      await api.deleteProject(draftProject.id)
      const nextProjects = await api.listProjects()
      const nextActive = await api.getActiveProject()
      setProjects(nextProjects)
      setActiveProjectID(nextActive.id)
      setDraftProject(nextActive)
      await refreshProjectData(nextActive.id)
      announce('Project deleted.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const editProject = (project: Project) => { setDraftProject(project); setActiveView('projects') }

  const createSchedule = () => { setDraftSchedule({ id: '', name: 'New routine', workflowID: selectedWorkflow?.id ?? workflows[0]?.id ?? '', cron: '*/30 * * * *', timezone: 'Local', enabled: true, createdAt: '', updatedAt: '' }); setActiveView('schedules') }

  const editSchedule = (schedule: Schedule) => { setDraftSchedule(schedule); setActiveView('schedules') }

  const saveSchedule = async () => {
    setBusy(true)
    try {
      const saved = draftSchedule.id ? await api.updateSchedule(draftSchedule) : await api.createSchedule(draftSchedule)
      setSchedules((current) => [...current.filter((schedule) => schedule.id !== saved.id), saved].sort((left, right) => left.name.localeCompare(right.name)))
      setDraftSchedule(saved)
      announce('Schedule saved and registered with Windows.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const deleteSchedule = async () => {
    if (!draftSchedule.id) return
    setBusy(true)
    try { await api.deleteSchedule(draftSchedule.id); setSchedules((current) => current.filter((schedule) => schedule.id !== draftSchedule.id)); setDraftSchedule({ id: '', name: 'New routine', workflowID: selectedWorkflow?.id ?? '', cron: '*/30 * * * *', timezone: 'Local', enabled: true, createdAt: '', updatedAt: '' }); announce('Schedule deleted.') } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const editAgent = (agent: AgentProfile) => {
    const normalized = normalizeAgent(agent)
    setSelectedAgentID(normalized.id)
    setDraftAgent(normalized)
  }

  const deleteAgent = async () => {
    if (!draftAgent.id) return
    setBusy(true)
    try { await api.deleteAgent(draftAgent.id); setAgents((current) => current.filter((agent) => agent.id !== draftAgent.id)); setDraftAgent({ ...defaultAgent }); announce('Agent deleted.') } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const startRun = async (workflowOverride?: WorkflowDefinition) => {
    let runnable = workflowOverride ?? selectedWorkflow
    if (!runnable) return
    setBusy(true)
    try {
      if (workflowOverride) {
        const saved = await api.saveWorkflow({ ...workflowOverride, updatedAt: new Date().toISOString() })
        runnable = saved
        setWorkflows((current) => [...current.filter((item) => item.id !== saved.id), saved])
        setSelectedWorkflowID(runnable.id)
      }
      const run = await api.startRun(runnable.id, { goal: 'Run the selected workflow', requestedAt: new Date().toISOString() })
      setRuns((current) => [run, ...current.filter((item) => item.id !== run.id)])
      setSelectedRunID(run.id)
      setActiveView('runs')
      announce('Run created; supervisor is now monitoring it.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const resolveApproval = async (approval: ApprovalRequest, decision: string) => {
    try { await api.resolveApproval({ id: approval.id, decision }); setApprovals((current) => { const next = { ...current }; delete next[approval.id]; return next }); announce(decision === 'approve' || decision === 'accept' ? 'Action approved.' : 'Action declined.') } catch (error) { announce(errorText(error)) }
  }

  const saveSystemPrompt = async (promptID: string, template: string) => {
    setBusy(true)
    try {
      const saved = await api.updateSystemPrompt(promptID, template)
      setSystemPrompts((current) => current.map((prompt) => prompt.id === saved.id ? saved : prompt))
      announce('System prompt saved.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const resetSystemPrompt = async (promptID: string) => {
    setBusy(true)
    try {
      const reset = await api.resetSystemPrompt(promptID)
      setSystemPrompts((current) => current.map((prompt) => prompt.id === reset.id ? reset : prompt))
      announce('System prompt restored to default.')
    } catch (error) { announce(errorText(error)) } finally { setBusy(false) }
  }

  const renderProjects = () => (
    <div className="view-stack projects-page">
      <div className="view-heading"><div><span className="section-kicker">Workspace context</span><h1>Projects</h1><p>Open Centurion into a project and keep every run, folder, prompt, and terminal command in one local context.</p></div><div className="heading-actions"><button className="button subtle" onClick={() => void exportProjectSnapshot()} disabled={!activeProjectID || busy}>Export snapshot</button><button className="button primary" onClick={createProject}>New project</button></div></div>
      <div className="projects-layout"><section className="project-list panel-card"><div className="project-list-heading"><span className="eyebrow">Your projects</span><span>{projects.length}</span></div>{projects.map((project) => <button type="button" className={`project-list-item ${project.id === activeProjectID ? 'active' : ''} ${project.id === draftProject.id ? 'selected' : ''}`} key={project.id} onClick={() => editProject(project)}><span className="project-list-mark">{project.name.slice(0, 1).toUpperCase()}</span><span className="project-list-copy"><strong>{project.name}</strong><small>{project.folders.length} folder{project.folders.length === 1 ? '' : 's'} · {project.id === activeProjectID ? 'Open now' : 'Local project'}</small></span><span className="project-list-arrow">→</span></button>)}{projects.length === 0 && <div className="empty-state"><strong>No project yet</strong><span>Create one to define the workspace context.</span></div>}</section><ProjectEditor project={draftProject} models={models} onChange={setDraftProject} onSave={saveProject} onOpen={() => void openProject(draftProject.id)} onDelete={draftProject.id ? deleteProject : undefined} onChooseFolder={chooseProjectFolder} onLearnSystem={learnProjectSystem} busy={busy} /></div>
    </div>
  )

  const renderTerminal = () => <TerminalView project={activeProject} onNotice={announce} onHistory={() => refreshHistory()} />

  const renderHistory = () => <HistoryView entries={history} kind={historyKind} onKindChange={setHistoryKind} />

  const renderBuilder = () => <BuilderView project={activeProject} agents={agents} models={models} auth={auth} onApply={applyBuilderProposal} onOpenWorkflow={() => setActiveView('workflows')} onStartWorkflow={startRun} onNotice={announce} />

  const renderOffice = () => (
    <div className="view-stack">
      <div className="view-heading"><div><span className="section-kicker">{activeProject?.name ?? 'No project selected'}</span><h1>Office</h1><p>See who is working, what needs approval, and which workflow is running in this project.</p></div><div className="heading-actions"><button className="button subtle" onClick={() => setActiveView('projects')}>Project settings</button><button className="button subtle" onClick={() => setActiveView('workflows')}>Open workflow</button><button className="button primary" onClick={() => void startRun()} disabled={!selectedWorkflow || busy}>Start run</button></div></div>
      <div className="office-grid">
        <section className="office-stage" aria-label="2D virtual office">
          <div className="stage-topbar"><div><span className="section-kicker">Now</span><strong>{workingAgents.length ? `${workingAgents.length} active agents` : 'System idle'}</strong></div><span className="stage-clock">{formatTime(new Date().toISOString())} local</span></div>
          <div className="office-rooms">
            {(['strategy', 'workshop', 'library'] as const).map((room) => { const roomAgents = agents.filter((agent) => agent.roomID === room); const roomLabel = room === 'strategy' ? 'Strategy room' : room === 'workshop' ? 'Workshop' : 'Library'; return <div className={`office-room room-${room}`} key={room}><div className="room-label"><span>{roomLabel}</span><small>{roomAgents.length.toString().padStart(2, '0')} occupants</small></div><div className="room-floor" />{roomAgents.map((agent) => <button className={`office-agent ${agent.id === officeAgent?.id ? 'selected' : ''}`} key={agent.id} aria-pressed={agent.id === officeAgent?.id} onClick={() => setOfficeAgentID(agent.id)}><AgentAvatar agent={agent} /><span className="office-agent-name">{agent.name}</span><small>{stateLabels[agent.visualState] ?? 'Idle'}</small></button>)}</div> })}
          </div>
          <div className="stage-legend"><span><i className="legend-state working" /> working</span><span><i className="legend-state waiting" /> approval</span><span><i className="legend-state idle" /> idle</span><span className="reduced-note">Text labels accompany every state</span></div>
        </section>
        <aside className="office-inspector">
          <div className="panel-card account-card"><div className="card-topline"><span className="eyebrow">Codex connection</span><StatusPill value={auth.status === 'logged_in' ? 'Online' : auth.status === 'offline' ? 'Offline' : 'Checking'} tone={auth.status === 'logged_in' ? 'success' : auth.status === 'offline' ? 'warning' : 'neutral'} /></div><h2>{auth.plan ? `${auth.plan} connected` : 'Codex-managed session'}</h2><p>{auth.email ?? 'Centurion uses the Codex CLI session already configured on this computer. It does not create or store a separate account login.'}</p>{auth.rateLimit && <div className="quota-line"><span>Current window</span><strong>{auth.rateLimit.usedPercent}% used</strong></div>}<span className="field-hint">If Codex is offline or unauthenticated, sign in from the Codex CLI and reload Centurion.</span></div>
          <AgentRunInspector agent={officeAgent} run={officeRun} workflow={officeWorkflow} step={officeAgentStep} events={officeAgentEvents} onOpenProfile={() => { if (officeAgent) { editAgent(officeAgent); setActiveView('agents') } }} onOpenRuns={() => setActiveView('runs')} />
          <div className="panel-card"><div className="card-topline"><span className="eyebrow">Current run</span><span className="mini-code">{latestRun ? latestRun.id.slice(0, 8) : '—'}</span></div>{latestRun ? <><div className="run-summary"><StatusPill value={statusLabel(latestRun.status)} tone={runTone(latestRun.status)} /><span>{formatTime(latestRun.updatedAt)}</span></div><h3>{workflows.find((workflow) => workflow.id === latestRun.workflowID)?.name ?? 'Workflow'}</h3><p className="muted">{latestRun.error ?? (latestRun.currentNodeID ? `Current node: ${latestRun.currentNodeID}` : 'No step selected.')}</p><button className="link-button" onClick={() => { setSelectedRunID(latestRun.id); setActiveView('runs') }}>Open timeline →</button></> : <div className="empty-small"><strong>No recent runs</strong><span>Start a workflow to bring the office to life.</span></div>}</div>
          <div className="panel-card compact-card"><div className="card-topline"><span className="eyebrow">Team</span><button className="link-button" onClick={() => setActiveView('agents')}>Manage</button></div><div className="agent-strip">{agents.slice(0, 5).map((agent) => <button key={agent.id} title={`Inspect ${agent.name}`} className={agent.id === officeAgent?.id ? 'selected' : ''} onClick={() => setOfficeAgentID(agent.id)}><AgentAvatar agent={agent} compact /></button>)}</div><span className="muted">{agents.length} profiles stored locally</span></div>
        </aside>
      </div>
      <Timeline events={activeEvents} onOpenRuns={() => setActiveView('runs')} />
    </div>
  )

  const renderAgents = () => (
    <div className="view-stack"><div className="view-heading"><div><span className="section-kicker">Team</span><h1>Agents</h1><p>Choose a preset, model, and access level. Adjust the role when needed.</p></div><div className="heading-actions"><button className="button subtle" onClick={() => setActiveView('builder')}>✦ Build with Codex</button><button className="button primary" onClick={createAgent}>New agent</button></div></div><div className="agents-layout"><section className="agent-list panel-card">{agents.map((agent) => <button className={`agent-list-item ${agent.id === selectedAgentID ? 'selected' : ''}`} key={agent.id} onClick={() => editAgent(agent)}><AgentAvatar agent={agent} compact /><span><strong>{agent.name}</strong><small>{agent.role}</small></span><span className="list-state">{agent.approvalProfile === 'autonomous' || agent.approvalProfile === 'trusted' ? 'Full access' : 'Approval required'}</span></button>)}{agents.length === 0 && <div className="empty-state"><strong>No agents</strong><span>Create the first profile to build your workflow.</span></div>}</section><section className="panel-card agent-editor-card"><AgentEditor key={draftAgent.id || 'draft'} agent={draftAgent} models={models} onChange={setDraftAgent} onSave={saveAgent} onDelete={draftAgent.id ? deleteAgent : undefined} busy={busy} /></section></div></div>
  )

  const renderWorkflows = () => (
    <div className="view-stack workflow-page"><div className="view-heading workflow-view-heading"><div><span className="section-kicker">Workflow builder</span><h1>Build a workflow</h1><p>Connect agents, decisions, and tools into a run you can inspect before it starts.</p></div><div className="workflow-heading-side"><button type="button" className="button subtle" onClick={() => setActiveView('builder')}>✦ Build with Codex</button><button type="button" className="button primary workflow-new-button" onClick={createWorkflow}>+ New workflow</button><label className="workflow-select"><span>Active workflow</span><select value={selectedWorkflow?.id ?? ''} onChange={(event) => setSelectedWorkflowID(event.target.value)}>{workflows.map((workflow) => <option key={workflow.id} value={workflow.id}>{workflow.name}</option>)}</select></label><span className="workflow-page-note"><span className="workflow-status-dot" aria-hidden="true" />Local workflow</span></div></div><section className="workflow-quickstart" aria-label="Workflow quick start"><div className="workflow-quickstart-copy"><span className="section-kicker">Fast path</span><strong>Build the route, then run it.</strong></div><ol className="workflow-quickstart-steps"><li><span>1</span><div><strong>Add blocks</strong><small>Agents, logic, tools</small></div></li><li><span>2</span><div><strong>Connect</strong><small>Output → input</small></div></li><li><span>3</span><div><strong>Configure</strong><small>Use the inspector</small></div></li><li><span>4</span><div><strong>Validate & run</strong><small>Fix issues first</small></div></li></ol></section>{selectedWorkflow ? <WorkflowCanvas workflow={selectedWorkflow} agents={agents} onSave={async (workflow) => { setBusy(true); try { const saved = await api.saveWorkflow(workflow); setWorkflows((current) => [...current.filter((item) => item.id !== saved.id), saved]); setSelectedWorkflowID(saved.id); announce('Workflow saved.') } catch (error) { announce(errorText(error)) } finally { setBusy(false) } }} onImport={async (workflow) => { try { const validation = await api.validateWorkflow(workflow); if (!validation.valid) { announce(validation.errors?.[0]?.message ?? 'Invalid workflow.'); return } const saved = await api.saveWorkflow(workflow); setWorkflows((current) => [...current.filter((item) => item.id !== saved.id), saved]); setSelectedWorkflowID(saved.id); announce('Workflow imported.') } catch (error) { announce(errorText(error)) } }} onStart={startRun} onDelete={() => void deleteWorkflow(selectedWorkflow)} busy={busy} onNotice={announce} /> : <div className="empty-state panel-card workflow-empty-state"><strong>No workflows yet</strong><span>Create your first workflow to connect agents visually.</span><button type="button" className="button primary" onClick={createWorkflow}>Create workflow</button><button type="button" className="button subtle" onClick={() => setActiveView('builder')}>Build it with Codex</button></div>}</div>
  )

  const renderRuns = () => (
    <div className="view-stack"><div className="view-heading"><div><span className="section-kicker">History</span><h1>Runs</h1><p>Open a run to inspect steps, approvals, errors, and results.</p></div><button className="button primary" onClick={() => void startRun()} disabled={!selectedWorkflow || busy}>Run workflow</button></div><div className="runs-layout"><section className="run-list panel-card">{runs.map((run) => <button className={`run-list-item ${run.id === selectedRunID ? 'selected' : ''}`} key={run.id} onClick={() => setSelectedRunID(run.id)}><div><strong>{workflows.find((workflow) => workflow.id === run.workflowID)?.name ?? 'Workflow'}</strong><small>{run.id.slice(0, 12)} · {formatTime(run.startedAt)}</small></div><StatusPill value={statusLabel(run.status)} tone={runTone(run.status)} /></button>)}{runs.length === 0 && <div className="empty-state"><strong>No runs</strong><span>The first run will appear here.</span></div>}</section><section className="run-detail panel-card">{selectedRun ? <><div className="card-topline"><span className="eyebrow">Selected run</span><span className="mini-code">{selectedRun.id}</span></div><div className="run-detail-heading"><div><h2>{workflows.find((workflow) => workflow.id === selectedRun.workflowID)?.name ?? 'Workflow'}</h2><p className="muted">Started at {formatTime(selectedRun.startedAt)} · updated at {formatTime(selectedRun.updatedAt)}</p></div><StatusPill value={statusLabel(selectedRun.status)} tone={runTone(selectedRun.status)} /></div><div className="run-actions">{selectedRun.status === 'running' && <button className="button subtle" onClick={() => void api.pauseRun(selectedRun.id).then(() => announce('Pause requested.')).catch((error) => announce(errorText(error)))}>Pause</button>}{['paused', 'interrupted'].includes(selectedRun.status) && <button className="button primary" onClick={() => void api.resumeRun(selectedRun.id).then(() => announce('Run resumed.')).catch((error) => announce(errorText(error)))}>Resume</button>}{['running', 'paused', 'queued'].includes(selectedRun.status) && <button className="button danger-quiet" onClick={() => void api.cancelRun(selectedRun.id).then(() => announce('Cancellation requested.')).catch((error) => announce(errorText(error)))}>Cancel</button>}{['blocked', 'failed'].includes(selectedRun.status) && selectedRun.currentNodeID && <button className="button subtle" onClick={() => void api.retryStep(selectedRun.id, selectedRun.currentNodeID || '').then(() => announce('Blocked step retry started.')).catch((error) => announce(errorText(error)))}>Retry current step</button>}</div><div className="run-usage-summary" aria-label="Run token usage"><div><span>Prompt budget</span><strong>{(selectedRun.promptTokensUsed ?? 0).toLocaleString()} / {(selectedRun.promptTokenBudget ?? 0).toLocaleString()} est. tokens</strong></div><div><span>Output</span><strong>{(selectedRun.outputBytes ?? 0).toLocaleString()} bytes</strong></div></div><Timeline events={activeEvents} agents={agents} detailed onOpenRuns={() => undefined} /></> : <div className="empty-state"><strong>Select a run</strong><span>The complete history is available here.</span></div>}</section></div></div>
  )

  const renderSchedules = () => (
    <div className="view-stack"><div className="view-heading"><div><span className="eyebrow">Recurring operation</span><h1>Local schedules</h1><p>Wake Centurion at defined times and run workflows without turning your plan into unlimited access.</p></div><button className="button primary" onClick={createSchedule}>New schedule</button></div><div className="schedules-layout"><section className="schedule-list panel-card">{schedules.map((schedule) => <button className={`schedule-item ${schedule.id === draftSchedule.id ? 'selected' : ''}`} key={schedule.id} onClick={() => editSchedule(schedule)}><div><strong>{schedule.name}</strong><small>{workflows.find((workflow) => workflow.id === schedule.workflowID)?.name ?? 'Workflow'}</small></div><span className={`schedule-state ${schedule.enabled ? 'enabled' : ''}`}>{schedule.enabled ? 'Active' : 'Paused'}</span></button>)}{schedules.length === 0 && <div className="empty-state"><strong>No schedules</strong><span>Create a routine to register the first local task.</span></div>}</section><section className="panel-card schedule-editor-card"><ScheduleEditor schedule={draftSchedule} workflows={workflows} onChange={setDraftSchedule} onSave={saveSchedule} onDelete={draftSchedule.id ? deleteSchedule : undefined} busy={busy} /></section></div><div className="schedule-note panel-card"><span className="note-mark">i</span><span>Headless mode uses <code>Centurion.exe --scheduled-run &lt;id&gt;</code>. If the interface is already open, a named pipe forwards the trigger to the main instance.</span></div></div>
  )

  const renderMCP = () => (
    <div className="view-stack">
      <div className="view-heading">
        <div><span className="eyebrow">External integrations</span><h1>MCP & tools</h1><p>Codex remains the executor; this view shows servers, tools, and authentication.</p></div>
        <button className="button subtle" onClick={() => void api.reloadMCPServers().then(() => announce('MCP configuration reloaded.')).catch((error) => announce(errorText(error)))}>Reload configuration</button>
      </div>
      <div className="mcp-grid">
        {mcpServers.map((server) => <MCPCard key={server.id} server={server} onLogin={(currentServer) => void api.loginMCPServer(currentServer.id).then((url) => { if (url) window.open(url, '_blank', 'noopener,noreferrer'); announce('MCP OAuth flow opened.') }).catch((error) => announce(errorText(error)))} />)}
        {mcpServers.length === 0 && <div className="empty-state panel-card"><strong>No MCP servers discovered</strong><span>Add servers in Codex configuration and reload this view.</span></div>}
      </div>
    </div>
  )

  const renderSettings = () => (
    <div className="view-stack">
      <div className="view-heading"><div><span className="eyebrow">Local runtime</span><h1>Settings</h1><p>Managed account, usage limits, and workspace security principles.</p></div></div>
      <div className="settings-grid">
        <section className="panel-card settings-hero"><span className="eyebrow">Codex runtime</span><h2>{auth.email ?? 'Managed by Codex CLI'}</h2><p>{auth.status === 'logged_in' ? `${auth.plan ?? 'Active plan'} is available through the local Codex App Server.` : 'The MVP has no separate Centurion login. Authentication, tokens, and refresh state remain managed by Codex.'}</p><div className="settings-actions"><span className="settings-inline-note">Run <code>codex login</code> outside the app when the Codex session needs attention.</span></div></section>
        <section className="panel-card policy-card"><span className="eyebrow">Default guardrails</span><div className="policy-row"><strong>Workspaces</strong><span>Only roots configured per agent</span></div><div className="policy-row"><strong>Network</strong><span>Disabled by default for coding agents</span></div><div className="policy-row"><strong>Approvals</strong><span>Commands, files, and permissions follow risk</span></div><div className="policy-row"><strong>Usage</strong><span>Automatic runs stop when Codex reports a limit</span></div></section>
        <section className="panel-card policy-card settings-catalog"><span className="eyebrow">Available catalog</span><h2>{models.length || '—'} models</h2><p className="muted">Discovered dynamically through <code>model/list</code>. Availability varies by account; IDs are not hardcoded.</p><div className="model-tags">{models.slice(0, 6).map((model) => <span key={model.id} className="tool-chip">{model.displayName}</span>)}</div></section>
        <section className="panel-card policy-card"><span className="eyebrow">Local session manager</span><h2>{runtimeStatus.activeSessions}/{runtimeStatus.maxSessions || '—'} logical sessions</h2><p className="muted">All agents share one Codex App Server process and use isolated threads. This prevents one process per agent.</p><div className="policy-row"><strong>Process</strong><span>{runtimeStatus.connected ? `${runtimeStatus.codexCommand} · PID ${runtimeStatus.appServerPID || '—'}` : 'Codex App Server offline'}</span></div><div className="policy-row"><strong>Active runs</strong><span>{runtimeStatus.activeRuns}</span></div><div className="policy-row"><strong>Prompt guard</strong><span>Each workflow has a persisted estimated-token budget.</span></div></section>
        <section className="panel-card policy-card audit-card"><div className="card-topline"><span className="eyebrow">Security audit</span><span className="mini-code">{auditEntries.length} recent entries</span></div>{auditEntries.length === 0 ? <p className="muted">Approvals, terminal commands, project changes, and exports will appear here.</p> : <div className="audit-list">{auditEntries.slice(0, 8).map((entry) => <div className="audit-row" key={entry.id}><span className="timeline-dot info" /><div><strong>{entry.kind}</strong><small>{entry.detail || entry.target || 'Recorded action'} · {formatTime(entry.createdAt)}</small></div><span>{entry.decision || entry.actor}</span></div>)}</div>}</section>
        <SystemPromptEditor prompts={systemPrompts} onSave={saveSystemPrompt} onReset={resetSystemPrompt} busy={busy} />
      </div>
    </div>
  )

  const content = activeView === 'projects' ? renderProjects() : activeView === 'office' ? renderOffice() : activeView === 'agents' ? renderAgents() : activeView === 'workflows' ? renderWorkflows() : activeView === 'builder' ? renderBuilder() : activeView === 'runs' ? renderRuns() : activeView === 'terminal' ? renderTerminal() : activeView === 'history' ? renderHistory() : activeView === 'schedules' ? renderSchedules() : activeView === 'mcp' ? renderMCP() : renderSettings()
  const approvalList = Object.values(approvals)

  return (
    <div className="app-window">
      <WindowFrame auth={auth} />
      <div className="app-shell">
      <aside className="side-rail"><div className="brand-lockup"><img className="brand-mark" src="/centurion-icon.png" alt="" aria-hidden="true" /><div><strong>CENTURION</strong><span>agent command center</span></div></div><nav className="main-nav" aria-label="Main navigation">{navItems.map((item) => <button key={item.id} title={item.label} aria-label={item.label} className={activeView === item.id ? 'active' : ''} onClick={() => setActiveView(item.id)}><Icon glyph={item.short} /><span>{item.label}</span></button>)}</nav><div className="rail-footer"><div className={`connection-mark ${auth.status === 'logged_in' ? 'online' : ''}`} /><span>{auth.status === 'logged_in' ? 'Codex connected' : 'Codex waiting'}</span><small>local-first · v0.1</small></div></aside>
      <main className="main-area"><header className="topbar"><div className="breadcrumb"><span>Centurion</span><b>/</b><strong>{navItems.find((item) => item.id === activeView)?.label}</strong></div><label className="project-switcher"><span>Project</span><select value={activeProjectID} onChange={(event) => void openProject(event.target.value)} disabled={!projects.length}><option value="">Choose a project</option>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label><div className="topbar-right"><span className="system-time">{new Date().toLocaleDateString('en-US', { day: '2-digit', month: 'short' })}</span><button className="quick-add" aria-label="Create new agent" onClick={createAgent}><span className="quick-add-icon" aria-hidden="true">+</span><span className="quick-add-label">New agent</span></button></div></header>{loading || auth.status === 'checking' ? <div className="loading-view"><div className="loading-orbit" /><span>{loading ? 'Opening your project…' : 'Waiting for the Codex session…'}</span></div> : <div className="content-scroll">{approvalList.length > 0 && <section className="approval-tray" aria-live="assertive"><div className="approval-tray-title"><span className="alert-mark">!</span><div><strong>{approvalList.length} approval{approvalList.length === 1 ? '' : 's'} waiting for you</strong><span>Execution is paused until you make an explicit decision.</span></div></div><div className="approval-actions">{approvalList.slice(0, 2).map((approval) => <div className="approval-item" key={approval.id}><span>{approval.title}</span><div><button className="button primary small" onClick={() => void resolveApproval(approval, 'approve')}>Approve</button><button className="button danger-quiet small" onClick={() => void resolveApproval(approval, 'decline')}>Decline</button></div></div>)}</div></section>}{content}</div>}{notice && <div className="toast" role="status">{notice}</div>}</main>
      </div>
    </div>
  )
}

function Timeline({ events, agents = [], detailed = false, onOpenRuns }: { events: RunEvent[]; agents?: AgentProfile[]; detailed?: boolean; onOpenRuns: () => void }) {
  const agentNames = new Map(agents.map((agent) => [agent.id, agent.name]))
  const visibleEvents = (detailed ? events.slice(-200) : events.slice(-7)).reverse()
  return <section className={`timeline-panel panel-card ${detailed ? 'timeline-panel-detailed' : ''}`}><div className="card-topline"><div><span className="eyebrow">Timeline</span><h2>{detailed ? 'Agent activity & run log' : 'Operational events'}</h2></div>{detailed ? <span className="timeline-count">{events.length} event{events.length === 1 ? '' : 's'}</span> : <button className="link-button" onClick={onOpenRuns}>view history →</button>}</div>{events.length === 0 ? <div className="timeline-empty"><span className="empty-line" /><span>Waiting for supervisor events.</span></div> : <div className="timeline-list">{visibleEvents.map((event) => { const agentName = event.agentID ? (agentNames.get(event.agentID) ?? event.agentID.slice(0, 12)) : 'Supervisor'; const detail = typeof event.data?.detail === 'string' ? event.data.detail : event.type === 'agent.output' && typeof event.data?.outputBytes === 'number' ? `${event.data.outputBytes.toLocaleString()} bytes returned` : ''; return <div className={`timeline-row ${detailed ? 'timeline-row-detailed' : ''}`} key={`${event.runID}-${event.sequence}`}><span className={`timeline-dot ${event.level}`} /><span className="timeline-time">{formatTime(event.timestamp)}</span>{detailed && <span className="timeline-agent">{agentName}</span>}{detailed && <span className="timeline-node">{event.nodeID ? `Node ${event.nodeID}` : 'Run'}</span>}<span className="timeline-type">{event.type}</span><span className="timeline-message">{event.message}{detail && <small className="timeline-detail">{detail}</small>}</span><span className="timeline-source">{event.source}</span></div>})}</div>}</section>
}

function AgentRunInspector({
  agent,
  run,
  workflow,
  step,
  events,
  onOpenProfile,
  onOpenRuns,
}: {
  agent?: AgentProfile
  run?: Run
  workflow?: WorkflowDefinition
  step?: RunStep
  events: RunEvent[]
  onOpenProfile: () => void
  onOpenRuns: () => void
}) {
  if (!agent) {
    return <div className="panel-card agent-run-inspector empty-small"><strong>Select an agent</strong><span>Click a person in the office to inspect its execution.</span></div>
  }
  const node = workflow?.nodes.find((candidate) => candidate.id === step?.nodeID)
  const currentStatus = step?.status ?? agent.visualState ?? 'idle'
  const tone = ['completed', 'success'].includes(currentStatus) ? 'success' : ['failed', 'error'].includes(currentStatus) ? 'danger' : ['waiting_approval', 'blocked'].includes(currentStatus) ? 'warning' : currentStatus === 'idle' ? 'neutral' : 'accent'
  let outputText = ''
  if (step?.output && Object.keys(step.output).length > 0) outputText = readableStructuredOutput(step.output)
  if (outputText.length > 1400) outputText = `${outputText.slice(0, 1400)}\n…`
  const latestEvent = events[0]
  return <section className="panel-card agent-run-inspector" aria-label={`Execution inspector for ${agent.name}`}>
    <div className="agent-inspector-heading"><AgentAvatar agent={agent} /><div><span className="eyebrow">Selected agent</span><h2>{agent.name}</h2><p>{agent.role}</p></div></div>
    <div className="agent-inspector-status"><StatusPill value={step ? statusLabel(currentStatus) : (stateLabels[currentStatus] ?? currentStatus)} tone={tone} /><span>{run ? `Run ${run.id.slice(0, 8)}` : 'No run selected'}</span></div>
    <div className="agent-inspector-grid">
      <div><span>Stage</span><strong>{node?.label || node?.id || 'Not started'}</strong></div>
      <div><span>Attempt</span><strong>{step ? `${step.attempt || 1}` : '—'}</strong></div>
      <div><span>Thread</span><code>{step?.threadID ? step.threadID.slice(0, 14) : '—'}</code></div>
      <div><span>Turn</span><code>{step?.turnID ? step.turnID.slice(0, 14) : '—'}</code></div>
    </div>
    {latestEvent && <div className="agent-inspector-event"><span>Latest activity</span><strong>{latestEvent.message || latestEvent.type}</strong><small>{formatTime(latestEvent.timestamp)} · {latestEvent.type}</small></div>}
    {events.length > 0 && <details className="agent-inspector-log"><summary>Agent activity · {events.length} events</summary><div>{events.map((event) => <div className="agent-inspector-log-row" key={`${event.runID}-${event.sequence}`}><span>{formatTime(event.timestamp)}</span><strong>{event.type}</strong><small>{event.message}</small></div>)}</div></details>}
    {step?.error && <div className="agent-inspector-error">{step.error}</div>}
    {outputText && <details className="agent-inspector-output" open><summary>Latest result</summary><div className="agent-inspector-readable-output">{outputText}</div></details>}
    <div className="agent-inspector-actions"><button className="button subtle small" onClick={onOpenProfile}>Open profile</button><button className="link-button" onClick={onOpenRuns}>Full run timeline →</button></div>
  </section>
}

export default App
