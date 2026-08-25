package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/RuanFernandes/centurion/internal/model"
	projectmanifest "github.com/RuanFernandes/centurion/internal/project"
	"github.com/RuanFernandes/centurion/internal/security"
	"github.com/google/uuid"
)

type Store struct {
	db       *sql.DB
	configMu sync.Mutex
}

const (
	systemPromptOverridesKey = "orchestrator.system_prompts"
	activeProjectSettingKey  = "workspace.active_project_id"
)

type storedSystemPromptOverride struct {
	Template  string `json:"template"`
	UpdatedAt string `json:"updatedAt"`
}

type RunStep struct {
	RunID       string
	NodeID      string
	Status      string
	Attempt     int
	ThreadID    string
	TurnID      string
	Output      map[string]any
	Error       string
	StartedAt   string
	CompletedAt string
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	store := &Store{db: db}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.seedDefaults(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func DefaultPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve local app data directory: %w", err)
	}
	return filepath.Join(configDir, "Centurion", "centurion.db"), nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) configure() error {
	statements := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite: %s: %w", statement, err)
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			role TEXT NOT NULL,
			instructions TEXT NOT NULL DEFAULT '',
			model_id TEXT NOT NULL DEFAULT '',
			reasoning_effort TEXT NOT NULL DEFAULT '',
			workspace_roots_json TEXT NOT NULL DEFAULT '[]',
			tool_allowlist_json TEXT NOT NULL DEFAULT '[]',
			approval_profile TEXT NOT NULL DEFAULT 'on_request',
			room_id TEXT NOT NULL DEFAULT 'main',
			avatar_id TEXT NOT NULL DEFAULT 'operator',
			visual_state TEXT NOT NULL DEFAULT 'idle',
			max_duration_seconds INTEGER NOT NULL DEFAULT 1800,
			max_turns INTEGER NOT NULL DEFAULT 12,
			max_attempts INTEGER NOT NULL DEFAULT 2,
			memory_summary TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			folders_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_opened_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS workflows (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			version INTEGER NOT NULL,
			definition_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			workflow_id TEXT NOT NULL,
			status TEXT NOT NULL,
			input_json TEXT NOT NULL DEFAULT '{}',
			output_json TEXT NOT NULL DEFAULT '{}',
			current_node_id TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			completed_at TEXT,
			error TEXT NOT NULL DEFAULT '',
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			paused INTEGER NOT NULL DEFAULT 0,
			prompt_tokens_used INTEGER NOT NULL DEFAULT 0,
			prompt_token_budget INTEGER NOT NULL DEFAULT 0,
			output_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS run_steps (
			run_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			status TEXT NOT NULL,
			attempt INTEGER NOT NULL DEFAULT 0,
			thread_id TEXT NOT NULL DEFAULT '',
			turn_id TEXT NOT NULL DEFAULT '',
			output_json TEXT NOT NULL DEFAULT '{}',
			error TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '',
			completed_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (run_id, node_id),
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS run_events (
			run_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			level TEXT NOT NULL,
			node_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL,
			data_json TEXT NOT NULL DEFAULT '{}',
			timestamp TEXT NOT NULL,
			schema_version INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (run_id, sequence),
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			path TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			byte_size INTEGER NOT NULL DEFAULT 0,
			media_type TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS schedules (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			workflow_id TEXT NOT NULL,
			cron TEXT NOT NULL,
			timezone TEXT NOT NULL DEFAULT 'Local',
			enabled INTEGER NOT NULL DEFAULT 1,
			next_run_at TEXT NOT NULL DEFAULT '',
			last_run_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS history (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			actor TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			decision TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_last_opened ON projects(last_opened_at DESC, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_history_project_created ON history(project_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_project_created ON audit_log(project_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_run_created ON audit_log(run_id, created_at DESC)`,
	}
	for index, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration statement %d: %w", index+1, err)
		}
	}
	if err := ensureColumn(ctx, s.db, "runs", "project_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate runs project scope: %w", err)
	}
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "agents", name: "project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "workflows", name: "project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "runs", name: "prompt_tokens_used", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "runs", name: "prompt_token_budget", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "runs", name: "output_bytes", definition: "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(ctx, s.db, column.table, column.name, column.definition); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", column.table, column.name, err)
		}
	}
	return nil
}

func (s *Store) seedDefaults(ctx context.Context) error {
	var projectCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&projectCount); err != nil {
		return err
	}
	firstInstall := projectCount == 0
	var project model.Project
	if firstInstall {
		var err error
		project, err = initialProject()
		if err != nil {
			return err
		}
		if err := s.SaveProject(ctx, project); err != nil {
			return err
		}
		if err := s.setSettingString(ctx, activeProjectSettingKey, project.ID); err != nil {
			return err
		}
	} else {
		var err error
		project, err = s.GetActiveProject(ctx)
		if err != nil {
			return fmt.Errorf("load active project for startup: %w", err)
		}
	}

	if firstInstall {
		exists, err := projectConfigExists(project)
		if err != nil {
			return err
		}
		if !exists {
			if err := s.seedDefaultCatalog(ctx, project); err != nil {
				return err
			}
		}
	}
	if err := s.syncAllProjectConfigs(ctx); err != nil {
		return err
	}
	if err := s.migrateLegacyLanguage(ctx); err != nil {
		return err
	}
	return s.syncAllProjectConfigs(ctx)
}

func initialProject() (model.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return model.Project{}, fmt.Errorf("resolve initial project folder: %w", err)
	}
	manifestPath := filepath.Join(cwd, projectmanifest.DirectoryName, projectmanifest.ManifestName)
	if _, statErr := os.Stat(manifestPath); statErr == nil {
		manifest, readErr := projectmanifest.ReadManifest(manifestPath)
		if readErr != nil {
			return model.Project{}, fmt.Errorf("read existing project manifest: %w", readErr)
		}
		return model.Project{ID: manifest.ProjectID, Name: manifest.Name, Description: manifest.Description, Folders: manifest.Folders, CreatedAt: now(), UpdatedAt: manifest.UpdatedAt, LastOpenedAt: now()}, nil
	} else if !os.IsNotExist(statErr) {
		return model.Project{}, fmt.Errorf("inspect initial project manifest: %w", statErr)
	}
	projectName := filepath.Base(cwd)
	if projectName == "." || projectName == string(filepath.Separator) || projectName == "" {
		projectName = "Local project"
	}
	current := now()
	return model.Project{ID: "project-local", Name: projectName, Folders: []string{cwd}, CreatedAt: current, UpdatedAt: current, LastOpenedAt: current}, nil
}

func (s *Store) seedDefaultCatalog(ctx context.Context, project model.Project) error {
	nowValue := now()
	roots := append([]string(nil), project.Folders...)
	agents := []model.AgentProfile{
		{
			ID: "agent-architect", ProjectID: project.ID, Name: "Mara", Role: "Systems architect",
			Instructions:   "Define a small, safe, and verifiable approach. Return decisions and risks in an objective format.",
			WorkspaceRoots: roots, ToolAllowlist: []string{"files.read", "git.diff"}, ApprovalProfile: "on_request",
			RoomID: "strategy", AvatarID: "architect", VisualState: model.AgentStateIdle, MaxDurationSeconds: 1200, MaxTurns: 8, MaxAttempts: 2,
			CreatedAt: nowValue, UpdatedAt: nowValue,
		},
		{
			ID: "agent-builder", ProjectID: project.ID, Name: "Nico", Role: "Software implementer",
			Instructions:   "Implement only inside the allowed workspace. Validate changes with tests and report any blocker.",
			WorkspaceRoots: roots, ToolAllowlist: []string{"files.read", "files.write", "shell.test", "git.diff"}, ApprovalProfile: "on_request",
			RoomID: "workshop", AvatarID: "builder", VisualState: model.AgentStateIdle, MaxDurationSeconds: 1800, MaxTurns: 12, MaxAttempts: 2,
			CreatedAt: nowValue, UpdatedAt: nowValue,
		},
	}
	for _, agent := range agents {
		if err := s.SaveAgent(ctx, agent); err != nil {
			return err
		}
	}

	workflow := model.WorkflowDefinition{
		ID: "workflow-studio-brief", ProjectID: project.ID, Name: "Product brief", Version: 1, EntryNodeID: "brief",
		Description:  "Supervisor example with parallel execution, a condition, a bounded loop, and approval.",
		ErrorPolicy:  "stop",
		GlobalLimits: model.WorkflowLimits{MaxDurationSeconds: 3600, MaxParallel: 2, MaxTurns: 24, MaxPromptTokens: 12000},
		Nodes: []model.WorkflowNode{
			{ID: "brief", Type: "agent", Label: "Define direction", AgentID: "agent-architect", TimeoutSeconds: 600, Retry: model.RetryPolicy{MaxAttempts: 2, BackoffSeconds: 2, Idempotent: true}},
			{ID: "parallel", Type: "parallel", Label: "Open workstreams", Retry: model.RetryPolicy{MaxAttempts: 1}},
			{ID: "build", Type: "agent", Label: "Build", AgentID: "agent-builder", TimeoutSeconds: 900, Retry: model.RetryPolicy{MaxAttempts: 2, BackoffSeconds: 4, Idempotent: false}},
			{ID: "review", Type: "agent", Label: "Review", AgentID: "agent-architect", TimeoutSeconds: 600, Retry: model.RetryPolicy{MaxAttempts: 2, BackoffSeconds: 3, Idempotent: true}},
			{ID: "join", Type: "join", Label: "Consolidate", Retry: model.RetryPolicy{MaxAttempts: 1}},
			{ID: "quality", Type: "condition", Label: "Quality approved", Condition: "truthy:review.approved", Retry: model.RetryPolicy{MaxAttempts: 1}},
			{ID: "loop", Type: "loop", Label: "Iterate corrections", MaxIterations: 2, Config: map[string]any{"onExhausted": "approval"}, Retry: model.RetryPolicy{MaxAttempts: 1}},
			{ID: "approval", Type: "approval", Label: "Approve publication", Retry: model.RetryPolicy{MaxAttempts: 1}},
			{ID: "artifact", Type: "artifact", Label: "Record result", ArtifactPath: "", Retry: model.RetryPolicy{MaxAttempts: 1}},
		},
		Edges: []model.WorkflowEdge{
			{ID: "e1", From: "brief", To: "parallel"},
			{ID: "e2", From: "parallel", To: "build"},
			{ID: "e3", From: "parallel", To: "review"},
			{ID: "e4", From: "build", To: "join"},
			{ID: "e5", From: "review", To: "join"},
			{ID: "e6", From: "join", To: "quality"},
			{ID: "e7", From: "quality", To: "loop", Condition: "equals:review.approved:false"},
			{ID: "e8", From: "quality", To: "approval", Condition: "equals:review.approved:true"},
			{ID: "e9", From: "loop", To: "quality"},
			{ID: "e10", From: "approval", To: "artifact"},
		},
		CreatedAt: nowValue, UpdatedAt: nowValue,
	}
	if err := s.SaveWorkflow(ctx, workflow); err != nil {
		return err
	}
	return nil
}

func projectConfigExists(project model.Project) (bool, error) {
	path, err := projectmanifest.ConfigPath(project.Folders)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect project config: %w", err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("project config path is a directory: %s", path)
	}
	return true, nil
}

func (s *Store) syncAllProjectConfigs(ctx context.Context) error {
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects for config sync: %w", err)
	}
	for _, project := range projects {
		if err := s.syncProjectConfig(ctx, project); err != nil {
			return fmt.Errorf("sync project %s config: %w", project.ID, err)
		}
	}
	return nil
}

// EnsureProjectConfig creates the project-owned catalog when a project is
// created or moved. Existing config is never overwritten by this method.
func (s *Store) EnsureProjectConfig(ctx context.Context, projectID string) error {
	project, err := s.GetProject(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	exists, err := projectConfigExists(project)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.writeProjectConfigLocked(ctx, project)
}

// LoadProjectConfig makes the .centurion catalog authoritative when a project
// is opened. A missing config is created from the current local catalog once,
// which migrates installations created before project-owned config existed.
func (s *Store) LoadProjectConfig(ctx context.Context, projectID string) error {
	project, err := s.GetProject(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	return s.syncProjectConfig(ctx, project)
}

func (s *Store) syncProjectConfig(ctx context.Context, project model.Project) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	path, err := projectmanifest.ConfigPath(project.Folders)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return s.writeProjectConfigLocked(ctx, project)
	} else if statErr != nil {
		return fmt.Errorf("inspect project config: %w", statErr)
	}
	config, err := projectmanifest.ReadConfig(path)
	if err != nil {
		return err
	}
	if config.ProjectID != project.ID {
		return fmt.Errorf("project config belongs to %q, expected %q", config.ProjectID, project.ID)
	}
	return s.importProjectConfigLocked(ctx, project, config)
}

func (s *Store) persistProjectConfig(ctx context.Context, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return nil
	}
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("load project for config persistence: %w", err)
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.writeProjectConfigLocked(ctx, project)
}

func (s *Store) writeProjectConfigLocked(ctx context.Context, project model.Project) error {
	agents, err := s.ListAgentsForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("list project agents: %w", err)
	}
	workflows, err := s.ListWorkflowsForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("list project workflows: %w", err)
	}
	schedules, err := s.ListSchedulesForProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("list project schedules: %w", err)
	}
	_, err = projectmanifest.WriteConfig(model.ProjectConfig{
		SchemaVersion: 1,
		ProjectID:     project.ID,
		Folders:       append([]string(nil), project.Folders...),
		UpdatedAt:     now(),
		Agents:        agents,
		Workflows:     workflows,
		Schedules:     schedules,
	})
	return err
}

func (s *Store) importProjectConfigLocked(ctx context.Context, project model.Project, config model.ProjectConfig) error {
	if len(config.Folders) > 0 {
		roots, err := security.NormalizeRoots(config.Folders)
		if err != nil {
			return fmt.Errorf("validate project config folders: %w", err)
		}
		if len(roots) != len(project.Folders) {
			return errors.New("project config folder list does not match the project manifest")
		}
		for index := range roots {
			if !strings.EqualFold(roots[index], project.Folders[index]) {
				return errors.New("project config folder list does not match the project manifest")
			}
		}
	}
	for index := range config.Agents {
		agent := &config.Agents[index]
		if strings.TrimSpace(agent.ID) == "" || strings.TrimSpace(agent.Name) == "" || strings.TrimSpace(agent.Role) == "" {
			return fmt.Errorf("project config agent %q is incomplete", agent.ID)
		}
		agent.ProjectID = project.ID
		for rootIndex, root := range agent.WorkspaceRoots {
			normalized, err := security.NormalizePath(root)
			if err != nil {
				return fmt.Errorf("validate workspace root for agent %s: %w", agent.ID, err)
			}
			if !security.IsWithinRoots(normalized, project.Folders) {
				return fmt.Errorf("agent %s workspace root is outside the project", agent.ID)
			}
			agent.WorkspaceRoots[rootIndex] = normalized
		}
	}
	workflowIDs := make(map[string]struct{}, len(config.Workflows))
	for index := range config.Workflows {
		workflow := &config.Workflows[index]
		if strings.TrimSpace(workflow.ID) == "" || strings.TrimSpace(workflow.Name) == "" {
			return errors.New("project config contains an incomplete workflow")
		}
		workflow.ProjectID = project.ID
		workflowIDs[workflow.ID] = struct{}{}
	}
	for index := range config.Schedules {
		if _, ok := workflowIDs[config.Schedules[index].WorkflowID]; !ok {
			return fmt.Errorf("schedule %s references a workflow outside the project config", config.Schedules[index].ID)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project config import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM schedules WHERE workflow_id IN (SELECT id FROM workflows WHERE project_id = ?)", project.ID); err != nil {
		return fmt.Errorf("clear project schedules: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM workflows WHERE project_id = ?", project.ID); err != nil {
		return fmt.Errorf("clear project workflows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM agents WHERE project_id = ?", project.ID); err != nil {
		return fmt.Errorf("clear project agents: %w", err)
	}
	for _, agent := range config.Agents {
		if err := insertAgent(ctx, tx, agent); err != nil {
			return err
		}
	}
	for _, workflow := range config.Workflows {
		if err := insertWorkflow(ctx, tx, workflow); err != nil {
			return err
		}
	}
	for _, schedule := range config.Schedules {
		if err := insertSchedule(ctx, tx, schedule); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project config import: %w", err)
	}
	return nil
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAgent(ctx context.Context, execer sqlExecer, agent model.AgentProfile) error {
	roots, err := jsonString(agent.WorkspaceRoots)
	if err != nil {
		return err
	}
	tools, err := jsonString(agent.ToolAllowlist)
	if err != nil {
		return err
	}
	if agent.CreatedAt == "" {
		agent.CreatedAt = now()
	}
	if agent.UpdatedAt == "" {
		agent.UpdatedAt = agent.CreatedAt
	}
	_, err = execer.ExecContext(ctx, `
		INSERT INTO agents (id, project_id, name, role, instructions, model_id, reasoning_effort, workspace_roots_json, tool_allowlist_json, approval_profile, room_id, avatar_id, visual_state, max_duration_seconds, max_turns, max_attempts, memory_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, name=excluded.name, role=excluded.role, instructions=excluded.instructions, model_id=excluded.model_id, reasoning_effort=excluded.reasoning_effort, workspace_roots_json=excluded.workspace_roots_json, tool_allowlist_json=excluded.tool_allowlist_json, approval_profile=excluded.approval_profile, room_id=excluded.room_id, avatar_id=excluded.avatar_id, visual_state=excluded.visual_state, max_duration_seconds=excluded.max_duration_seconds, max_turns=excluded.max_turns, max_attempts=excluded.max_attempts, memory_summary=excluded.memory_summary, updated_at=excluded.updated_at`,
		agent.ID, agent.ProjectID, agent.Name, agent.Role, agent.Instructions, agent.ModelID, agent.ReasoningEffort, roots, tools, agent.ApprovalProfile, agent.RoomID, agent.AvatarID, agent.VisualState, agent.MaxDurationSeconds, agent.MaxTurns, agent.MaxAttempts, agent.MemorySummary, agent.CreatedAt, agent.UpdatedAt)
	return err
}

func insertWorkflow(ctx context.Context, execer sqlExecer, workflow model.WorkflowDefinition) error {
	if workflow.Version <= 0 {
		workflow.Version = 1
	}
	if workflow.CreatedAt == "" {
		workflow.CreatedAt = now()
	}
	if workflow.UpdatedAt == "" {
		workflow.UpdatedAt = workflow.CreatedAt
	}
	definition, err := json.Marshal(workflow)
	if err != nil {
		return fmt.Errorf("encode project workflow: %w", err)
	}
	_, err = execer.ExecContext(ctx, `
		INSERT INTO workflows (id, project_id, name, version, definition_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, name=excluded.name, version=excluded.version, definition_json=excluded.definition_json, updated_at=excluded.updated_at`,
		workflow.ID, workflow.ProjectID, workflow.Name, workflow.Version, string(definition), workflow.CreatedAt, workflow.UpdatedAt)
	return err
}

func insertSchedule(ctx context.Context, execer sqlExecer, schedule model.Schedule) error {
	if schedule.CreatedAt == "" {
		schedule.CreatedAt = now()
	}
	if schedule.UpdatedAt == "" {
		schedule.UpdatedAt = schedule.CreatedAt
	}
	_, err := execer.ExecContext(ctx, `
		INSERT INTO schedules (id, name, workflow_id, cron, timezone, enabled, next_run_at, last_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, workflow_id=excluded.workflow_id, cron=excluded.cron, timezone=excluded.timezone, enabled=excluded.enabled, next_run_at=excluded.next_run_at, last_run_at=excluded.last_run_at, updated_at=excluded.updated_at`,
		schedule.ID, schedule.Name, schedule.WorkflowID, schedule.Cron, schedule.Timezone, boolInt(schedule.Enabled), schedule.NextRunAt, schedule.LastRunAt, schedule.CreatedAt, schedule.UpdatedAt)
	return err
}

func (s *Store) migrateLegacyLanguage(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin language migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	legacyAgents := []struct {
		id           string
		legacyRole   string
		role         string
		legacyPrompt string
		prompt       string
	}{
		{
			id:           "agent-architect",
			legacyRole:   "Arquiteta de sistemas",
			role:         "Systems architect",
			legacyPrompt: "Defina uma abordagem pequena, segura e verificável. Entregue decisões e riscos em formato objetivo.",
			prompt:       "Define a small, safe, and verifiable approach. Return decisions and risks in an objective format.",
		},
		{
			id:           "agent-builder",
			legacyRole:   "Implementador",
			role:         "Software implementer",
			legacyPrompt: "Implemente somente dentro do workspace permitido. Valide as mudanças com testes e relate qualquer bloqueio.",
			prompt:       "Implement only inside the allowed workspace. Validate changes with tests and report any blocker.",
		},
	}
	for _, agent := range legacyAgents {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET role = ?, instructions = ?, updated_at = ? WHERE id = ? AND role = ? AND instructions = ?`, agent.role, agent.prompt, now(), agent.id, agent.legacyRole, agent.legacyPrompt); err != nil {
			return fmt.Errorf("translate agent %s: %w", agent.id, err)
		}
	}

	const legacyWorkflowDescription = "Exemplo de supervisor com execução paralela, condição, loop limitado e aprovação."
	var name, description, definition string
	err = tx.QueryRowContext(ctx, `SELECT name, json_extract(definition_json, '$.description'), definition_json FROM workflows WHERE id = ?`, "workflow-studio-brief").Scan(&name, &description, &definition)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read legacy workflow: %w", err)
	}
	if err == nil && name == "Brief de produto" && description == legacyWorkflowDescription {
		var workflow model.WorkflowDefinition
		if err := json.Unmarshal([]byte(definition), &workflow); err != nil {
			return fmt.Errorf("decode legacy workflow: %w", err)
		}
		workflow.Name = "Product brief"
		workflow.Description = "Supervisor example with parallel execution, a condition, a bounded loop, and approval."
		labels := map[string]string{
			"brief":    "Define direction",
			"parallel": "Open workstreams",
			"build":    "Build",
			"review":   "Review",
			"join":     "Consolidate",
			"quality":  "Quality approved",
			"loop":     "Iterate corrections",
			"approval": "Approve publication",
			"artifact": "Record result",
		}
		for index := range workflow.Nodes {
			if label, ok := labels[workflow.Nodes[index].ID]; ok {
				workflow.Nodes[index].Label = label
			}
		}
		workflow.UpdatedAt = now()
		encoded, err := json.Marshal(workflow)
		if err != nil {
			return fmt.Errorf("encode translated workflow: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflows SET name = ?, definition_json = ?, updated_at = ? WHERE id = ?`, workflow.Name, string(encoded), workflow.UpdatedAt, workflow.ID); err != nil {
			return fmt.Errorf("translate legacy workflow: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit language migration: %w", err)
	}
	return nil
}

func (s *Store) SaveAgent(ctx context.Context, agent model.AgentProfile) error {
	roots, err := jsonString(agent.WorkspaceRoots)
	if err != nil {
		return err
	}
	tools, err := jsonString(agent.ToolAllowlist)
	if err != nil {
		return err
	}
	if agent.CreatedAt == "" {
		agent.CreatedAt = now()
	}
	if agent.UpdatedAt == "" {
		agent.UpdatedAt = now()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agents (id, project_id, name, role, instructions, model_id, reasoning_effort, workspace_roots_json, tool_allowlist_json, approval_profile, room_id, avatar_id, visual_state, max_duration_seconds, max_turns, max_attempts, memory_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, name=excluded.name, role=excluded.role, instructions=excluded.instructions, model_id=excluded.model_id, reasoning_effort=excluded.reasoning_effort, workspace_roots_json=excluded.workspace_roots_json, tool_allowlist_json=excluded.tool_allowlist_json, approval_profile=excluded.approval_profile, room_id=excluded.room_id, avatar_id=excluded.avatar_id, visual_state=excluded.visual_state, max_duration_seconds=excluded.max_duration_seconds, max_turns=excluded.max_turns, max_attempts=excluded.max_attempts, memory_summary=excluded.memory_summary, updated_at=excluded.updated_at`,
		agent.ID, agent.ProjectID, agent.Name, agent.Role, agent.Instructions, agent.ModelID, agent.ReasoningEffort, roots, tools, agent.ApprovalProfile, agent.RoomID, agent.AvatarID, agent.VisualState, agent.MaxDurationSeconds, agent.MaxTurns, agent.MaxAttempts, agent.MemorySummary, agent.CreatedAt, agent.UpdatedAt)
	if err != nil {
		return err
	}
	return s.persistProjectConfig(ctx, agent.ProjectID)
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	var projectID string
	if err := s.db.QueryRowContext(ctx, "SELECT project_id FROM agents WHERE id = ?", id).Scan(&projectID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return s.persistProjectConfig(ctx, projectID)
}

func (s *Store) ListAgents(ctx context.Context) ([]model.AgentProfile, error) {
	return s.listAgents(ctx, "")
}

func (s *Store) ListAgentsForProject(ctx context.Context, projectID string) ([]model.AgentProfile, error) {
	return s.listAgents(ctx, strings.TrimSpace(projectID))
}

func (s *Store) listAgents(ctx context.Context, projectID string) ([]model.AgentProfile, error) {
	query := `SELECT id, project_id, name, role, instructions, model_id, reasoning_effort, workspace_roots_json, tool_allowlist_json, approval_profile, room_id, avatar_id, visual_state, max_duration_seconds, max_turns, max_attempts, memory_summary, created_at, updated_at FROM agents`
	args := make([]any, 0, 1)
	if projectID != "" {
		query += ` WHERE project_id = ? OR project_id = ''`
		args = append(args, projectID)
	}
	query += ` ORDER BY name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.AgentProfile, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, agent)
	}
	return result, rows.Err()
}

func (s *Store) GetAgent(ctx context.Context, id string) (model.AgentProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, project_id, name, role, instructions, model_id, reasoning_effort, workspace_roots_json, tool_allowlist_json, approval_profile, room_id, avatar_id, visual_state, max_duration_seconds, max_turns, max_attempts, memory_summary, created_at, updated_at FROM agents WHERE id = ?`, id)
	return scanAgent(row)
}

func (s *Store) SaveWorkflow(ctx context.Context, workflow model.WorkflowDefinition) error {
	if workflow.ID == "" {
		return errors.New("workflow id is required")
	}
	if workflow.Version <= 0 {
		workflow.Version = 1
	}
	if workflow.CreatedAt == "" {
		workflow.CreatedAt = now()
	}
	if workflow.UpdatedAt == "" {
		workflow.UpdatedAt = now()
	}
	definition, err := json.Marshal(workflow)
	if err != nil {
		return fmt.Errorf("encode workflow: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflows (id, project_id, name, version, definition_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, name=excluded.name, version=excluded.version, definition_json=excluded.definition_json, updated_at=excluded.updated_at`,
		workflow.ID, workflow.ProjectID, workflow.Name, workflow.Version, string(definition), workflow.CreatedAt, workflow.UpdatedAt)
	if err != nil {
		return err
	}
	return s.persistProjectConfig(ctx, workflow.ProjectID)
}

// ApplyBuilderProposal persists the newly generated profiles and workflow as
// one local transaction. The builder never writes directly; this method is
// called only after AppService has normalized and validated the proposal.
func (s *Store) ApplyBuilderProposal(ctx context.Context, agents []model.AgentProfile, workflow *model.WorkflowDefinition) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, agent := range agents {
		if strings.TrimSpace(agent.ID) == "" || strings.TrimSpace(agent.Name) == "" || strings.TrimSpace(agent.Role) == "" {
			return errors.New("builder agent is missing id, name or role")
		}
		roots, err := jsonString(agent.WorkspaceRoots)
		if err != nil {
			return err
		}
		tools, err := jsonString(agent.ToolAllowlist)
		if err != nil {
			return err
		}
		createdAt := agent.CreatedAt
		if createdAt == "" {
			createdAt = now()
		}
		updatedAt := agent.UpdatedAt
		if updatedAt == "" {
			updatedAt = createdAt
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agents (id, project_id, name, role, instructions, model_id, reasoning_effort, workspace_roots_json, tool_allowlist_json, approval_profile, room_id, avatar_id, visual_state, max_duration_seconds, max_turns, max_attempts, memory_summary, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, name=excluded.name, role=excluded.role, instructions=excluded.instructions, model_id=excluded.model_id, reasoning_effort=excluded.reasoning_effort, workspace_roots_json=excluded.workspace_roots_json, tool_allowlist_json=excluded.tool_allowlist_json, approval_profile=excluded.approval_profile, room_id=excluded.room_id, avatar_id=excluded.avatar_id, visual_state=excluded.visual_state, max_duration_seconds=excluded.max_duration_seconds, max_turns=excluded.max_turns, max_attempts=excluded.max_attempts, memory_summary=excluded.memory_summary, updated_at=excluded.updated_at`,
			agent.ID, agent.ProjectID, agent.Name, agent.Role, agent.Instructions, agent.ModelID, agent.ReasoningEffort, roots, tools, agent.ApprovalProfile, agent.RoomID, agent.AvatarID, agent.VisualState, agent.MaxDurationSeconds, agent.MaxTurns, agent.MaxAttempts, agent.MemorySummary, createdAt, updatedAt); err != nil {
			return err
		}
	}

	if workflow != nil {
		definition, err := json.Marshal(*workflow)
		if err != nil {
			return fmt.Errorf("encode builder workflow: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflows (id, project_id, name, version, definition_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, name=excluded.name, version=excluded.version, definition_json=excluded.definition_json, updated_at=excluded.updated_at`,
			workflow.ID, workflow.ProjectID, workflow.Name, workflow.Version, string(definition), workflow.CreatedAt, workflow.UpdatedAt); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit builder proposal: %w", err)
	}
	projectID := ""
	if workflow != nil {
		projectID = workflow.ProjectID
	}
	if projectID == "" && len(agents) > 0 {
		projectID = agents[0].ProjectID
	}
	return s.persistProjectConfig(ctx, projectID)
}

func (s *Store) DeleteWorkflow(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("workflow id is required")
	}
	var projectID string
	if err := s.db.QueryRowContext(ctx, "SELECT project_id FROM workflows WHERE id = ?", id).Scan(&projectID); err != nil {
		return err
	}
	var scheduleCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schedules WHERE workflow_id = ?", id).Scan(&scheduleCount); err != nil {
		return err
	}
	if scheduleCount > 0 {
		return fmt.Errorf("workflow is used by %d schedule(s); delete those schedules first", scheduleCount)
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM workflows WHERE id = ?", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return s.persistProjectConfig(ctx, projectID)
}

func (s *Store) GetWorkflow(ctx context.Context, id string) (model.WorkflowDefinition, error) {
	var definition string
	if err := s.db.QueryRowContext(ctx, "SELECT definition_json FROM workflows WHERE id = ?", id).Scan(&definition); err != nil {
		return model.WorkflowDefinition{}, err
	}
	var workflow model.WorkflowDefinition
	if err := json.Unmarshal([]byte(definition), &workflow); err != nil {
		return model.WorkflowDefinition{}, fmt.Errorf("decode workflow %s: %w", id, err)
	}
	return workflow, nil
}

func (s *Store) ListWorkflows(ctx context.Context) ([]model.WorkflowDefinition, error) {
	return s.listWorkflows(ctx, "")
}

func (s *Store) ListWorkflowsForProject(ctx context.Context, projectID string) ([]model.WorkflowDefinition, error) {
	return s.listWorkflows(ctx, strings.TrimSpace(projectID))
}

func (s *Store) listWorkflows(ctx context.Context, projectID string) ([]model.WorkflowDefinition, error) {
	query := "SELECT definition_json FROM workflows"
	args := make([]any, 0, 1)
	if projectID != "" {
		query += " WHERE project_id = ? OR project_id = ''"
		args = append(args, projectID)
	}
	query += " ORDER BY updated_at DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.WorkflowDefinition, 0)
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			return nil, err
		}
		var workflow model.WorkflowDefinition
		if err := json.Unmarshal([]byte(definition), &workflow); err != nil {
			return nil, err
		}
		result = append(result, workflow)
	}
	return result, rows.Err()
}

func (s *Store) SaveProject(ctx context.Context, project model.Project) error {
	folders, err := json.Marshal(project.Folders)
	if err != nil {
		return fmt.Errorf("encode project folders: %w", err)
	}
	if project.CreatedAt == "" {
		project.CreatedAt = now()
	}
	if project.UpdatedAt == "" {
		project.UpdatedAt = now()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, description, folders_json, created_at, updated_at, last_opened_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, description=excluded.description, folders_json=excluded.folders_json, updated_at=excluded.updated_at, last_opened_at=excluded.last_opened_at`,
		project.ID, project.Name, project.Description, string(folders), project.CreatedAt, project.UpdatedAt, project.LastOpenedAt)
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (model.Project, error) {
	var project model.Project
	var folders string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, folders_json, created_at, updated_at, last_opened_at FROM projects WHERE id = ?`, id).Scan(&project.ID, &project.Name, &project.Description, &folders, &project.CreatedAt, &project.UpdatedAt, &project.LastOpenedAt)
	if err != nil {
		return project, err
	}
	if err := json.Unmarshal([]byte(folders), &project.Folders); err != nil {
		return project, fmt.Errorf("decode project folders: %w", err)
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, folders_json, created_at, updated_at, last_opened_at FROM projects ORDER BY CASE WHEN last_opened_at = '' THEN 1 ELSE 0 END, last_opened_at DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]model.Project, 0)
	for rows.Next() {
		var project model.Project
		var folders string
		if err := rows.Scan(&project.ID, &project.Name, &project.Description, &folders, &project.CreatedAt, &project.UpdatedAt, &project.LastOpenedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(folders), &project.Folders); err != nil {
			return nil, fmt.Errorf("decode project folders: %w", err)
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	activeID, err := s.getSettingString(ctx, activeProjectSettingKey)
	if err != nil {
		return err
	}
	if activeID == id {
		return s.setSettingString(ctx, activeProjectSettingKey, "")
	}
	return nil
}

func (s *Store) GetActiveProject(ctx context.Context) (model.Project, error) {
	activeID, err := s.getSettingString(ctx, activeProjectSettingKey)
	if err != nil {
		return model.Project{}, err
	}
	if activeID != "" {
		if project, projectErr := s.GetProject(ctx, activeID); projectErr == nil {
			return project, nil
		}
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return model.Project{}, err
	}
	if len(projects) == 0 {
		return model.Project{}, sql.ErrNoRows
	}
	if err := s.SetActiveProject(ctx, projects[0].ID); err != nil {
		return model.Project{}, err
	}
	return s.GetProject(ctx, projects[0].ID)
}

func (s *Store) SetActiveProject(ctx context.Context, id string) error {
	project, err := s.GetProject(ctx, id)
	if err != nil {
		return err
	}
	if err := s.LoadProjectConfig(ctx, project.ID); err != nil {
		return fmt.Errorf("load project config: %w", err)
	}
	if err := s.setSettingString(ctx, activeProjectSettingKey, id); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE projects SET last_opened_at = ?, updated_at = updated_at WHERE id = ?", now(), id)
	return err
}

func (s *Store) AppendHistory(ctx context.Context, entry model.HistoryEntry) error {
	if entry.CreatedAt == "" {
		entry.CreatedAt = now()
	}
	metadata, err := jsonString(entry.Metadata)
	if err != nil {
		return err
	}
	entry.Title = security.RedactSensitiveText(entry.Title)
	entry.Content = security.RedactSensitiveText(entry.Content)
	if len([]rune(entry.Content)) > 16000 {
		entry.Content = string([]rune(entry.Content)[:16000]) + "\n[truncated]"
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO history (id, project_id, kind, title, content, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, entry.ID, entry.ProjectID, entry.Kind, entry.Title, entry.Content, metadata, entry.CreatedAt)
	return err
}

func (s *Store) ListHistory(ctx context.Context, filter model.HistoryFilter) ([]model.HistoryEntry, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := "SELECT id, project_id, kind, title, content, metadata_json, created_at FROM history WHERE 1=1"
	args := make([]any, 0, 2)
	if filter.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, filter.ProjectID)
	}
	if filter.Kind == "planning" {
		query += " AND kind IN (?, ?)"
		args = append(args, "planning_prompt", "planning_response")
	} else if filter.Kind != "" {
		query += " AND kind = ?"
		args = append(args, filter.Kind)
	}
	query += " ORDER BY created_at DESC LIMIT " + strconv.Itoa(limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]model.HistoryEntry, 0)
	for rows.Next() {
		var entry model.HistoryEntry
		var metadata string
		if err := rows.Scan(&entry.ID, &entry.ProjectID, &entry.Kind, &entry.Title, &entry.Content, &metadata, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadata), &entry.Metadata); err != nil {
			return nil, fmt.Errorf("decode history metadata: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) AppendAudit(ctx context.Context, entry model.AuditEntry) error {
	if strings.TrimSpace(entry.ID) == "" {
		entry.ID = uuid.NewString()
	}
	if strings.TrimSpace(entry.Kind) == "" {
		return errors.New("audit kind is required")
	}
	if entry.CreatedAt == "" {
		entry.CreatedAt = now()
	}
	entry.Actor = truncateRedacted(entry.Actor, 120)
	entry.Target = truncateRedacted(entry.Target, 500)
	entry.Decision = truncateRedacted(entry.Decision, 120)
	entry.Detail = truncateRedacted(entry.Detail, 8000)
	metadata, err := redactedJSON(entry.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_log (id, project_id, run_id, kind, actor, target, decision, detail, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.ProjectID, entry.RunID, truncateRedacted(entry.Kind, 120), entry.Actor, entry.Target, entry.Decision, entry.Detail, metadata, entry.CreatedAt)
	return err
}

func (s *Store) ListAudit(ctx context.Context, filter model.AuditFilter) ([]model.AuditEntry, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := "SELECT id, project_id, run_id, kind, actor, target, decision, detail, metadata_json, created_at FROM audit_log WHERE 1=1"
	args := make([]any, 0, 3)
	if strings.TrimSpace(filter.ProjectID) != "" {
		query += " AND project_id = ?"
		args = append(args, strings.TrimSpace(filter.ProjectID))
	}
	if strings.TrimSpace(filter.RunID) != "" {
		query += " AND run_id = ?"
		args = append(args, strings.TrimSpace(filter.RunID))
	}
	if strings.TrimSpace(filter.Kind) != "" {
		query += " AND kind = ?"
		args = append(args, strings.TrimSpace(filter.Kind))
	}
	query += " ORDER BY created_at DESC LIMIT " + strconv.Itoa(limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]model.AuditEntry, 0)
	for rows.Next() {
		var entry model.AuditEntry
		var metadata string
		if err := rows.Scan(&entry.ID, &entry.ProjectID, &entry.RunID, &entry.Kind, &entry.Actor, &entry.Target, &entry.Decision, &entry.Detail, &metadata, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadata), &entry.Metadata); err != nil {
			return nil, fmt.Errorf("decode audit metadata: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) CreateRun(ctx context.Context, run model.Run) error {
	input, err := jsonString(run.Input)
	if err != nil {
		return err
	}
	output, err := jsonString(run.Output)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (id, project_id, workflow_id, status, input_json, output_json, current_node_id, started_at, updated_at, completed_at, error, cancel_requested, paused, prompt_tokens_used, prompt_token_budget, output_bytes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.WorkflowID, run.Status, input, output, run.CurrentNodeID, run.StartedAt, run.UpdatedAt, nullableString(run.CompletedAt), run.Error, boolInt(run.CancelRequested), boolInt(run.Paused), run.PromptTokensUsed, run.PromptTokenBudget, run.OutputBytes)
	return err
}

func (s *Store) UpdateRun(ctx context.Context, run model.Run) error {
	input, err := jsonString(run.Input)
	if err != nil {
		return err
	}
	output, err := jsonString(run.Output)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE runs SET project_id=?, status=?, input_json=?, output_json=?, current_node_id=?, started_at=?, updated_at=?, completed_at=?, error=?, cancel_requested=?, paused=?, prompt_tokens_used=?, prompt_token_budget=?, output_bytes=? WHERE id=?`,
		run.ProjectID, run.Status, input, output, run.CurrentNodeID, run.StartedAt, run.UpdatedAt, nullableString(run.CompletedAt), run.Error, boolInt(run.CancelRequested), boolInt(run.Paused), run.PromptTokensUsed, run.PromptTokenBudget, run.OutputBytes, run.ID)
	return err
}

func (s *Store) GetRun(ctx context.Context, id string) (model.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, project_id, workflow_id, status, input_json, output_json, current_node_id, started_at, updated_at, completed_at, error, cancel_requested, paused, prompt_tokens_used, prompt_token_budget, output_bytes FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *Store) ListRuns(ctx context.Context, filter model.RunFilter) ([]model.Run, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := "SELECT id, project_id, workflow_id, status, input_json, output_json, current_node_id, started_at, updated_at, completed_at, error, cancel_requested, paused, prompt_tokens_used, prompt_token_budget, output_bytes FROM runs WHERE 1=1"
	args := make([]any, 0, 4)
	if filter.ProjectID != "" {
		query += " AND project_id = ?"
		args = append(args, filter.ProjectID)
	}
	if filter.WorkflowID != "" {
		query += " AND workflow_id = ?"
		args = append(args, filter.WorkflowID)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	query += " ORDER BY updated_at DESC LIMIT " + strconv.Itoa(limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) AppendRunEvent(ctx context.Context, event model.RunEvent) (model.RunEvent, error) {
	if event.SchemaVersion == 0 {
		event.SchemaVersion = 1
	}
	if event.Timestamp == "" {
		event.Timestamp = now()
	}
	data, err := jsonString(event.Data)
	if err != nil {
		return event, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?", event.RunID).Scan(&event.Sequence); err != nil {
		return event, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_events (run_id, sequence, type, source, level, node_id, agent_id, message, data_json, timestamp, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RunID, event.Sequence, event.Type, event.Source, event.Level, event.NodeID, event.AgentID, event.Message, data, event.Timestamp, event.SchemaVersion)
	if err != nil {
		return event, err
	}
	if err := tx.Commit(); err != nil {
		return event, err
	}
	return event, nil
}

func (s *Store) ListRunEvents(ctx context.Context, runID string, afterSequence int64) ([]model.RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, type, source, level, node_id, agent_id, message, data_json, timestamp, schema_version FROM run_events WHERE run_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT 1000`, runID, afterSequence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.RunEvent, 0)
	for rows.Next() {
		var event model.RunEvent
		var data string
		if err := rows.Scan(&event.Sequence, &event.Type, &event.Source, &event.Level, &event.NodeID, &event.AgentID, &event.Message, &data, &event.Timestamp, &event.SchemaVersion); err != nil {
			return nil, err
		}
		event.RunID = runID
		if err := json.Unmarshal([]byte(data), &event.Data); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) UpsertRunStep(ctx context.Context, step RunStep) error {
	output, err := jsonString(step.Output)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO run_steps (run_id, node_id, status, attempt, thread_id, turn_id, output_json, error, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, node_id) DO UPDATE SET status=excluded.status, attempt=excluded.attempt, thread_id=excluded.thread_id, turn_id=excluded.turn_id, output_json=excluded.output_json, error=excluded.error, started_at=excluded.started_at, completed_at=excluded.completed_at`,
		step.RunID, step.NodeID, step.Status, step.Attempt, step.ThreadID, step.TurnID, output, step.Error, step.StartedAt, step.CompletedAt)
	return err
}

func (s *Store) GetRunStep(ctx context.Context, runID, nodeID string) (RunStep, error) {
	var step RunStep
	var output string
	err := s.db.QueryRowContext(ctx, `SELECT run_id, node_id, status, attempt, thread_id, turn_id, output_json, error, started_at, completed_at FROM run_steps WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&step.RunID, &step.NodeID, &step.Status, &step.Attempt, &step.ThreadID, &step.TurnID, &output, &step.Error, &step.StartedAt, &step.CompletedAt)
	if err != nil {
		return step, err
	}
	if err := json.Unmarshal([]byte(output), &step.Output); err != nil {
		return step, err
	}
	return step, nil
}

func (s *Store) SaveSchedule(ctx context.Context, schedule model.Schedule) error {
	if schedule.CreatedAt == "" {
		schedule.CreatedAt = now()
	}
	if schedule.UpdatedAt == "" {
		schedule.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO schedules (id, name, workflow_id, cron, timezone, enabled, next_run_at, last_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, workflow_id=excluded.workflow_id, cron=excluded.cron, timezone=excluded.timezone, enabled=excluded.enabled, next_run_at=excluded.next_run_at, last_run_at=excluded.last_run_at, updated_at=excluded.updated_at`,
		schedule.ID, schedule.Name, schedule.WorkflowID, schedule.Cron, schedule.Timezone, boolInt(schedule.Enabled), schedule.NextRunAt, schedule.LastRunAt, schedule.CreatedAt, schedule.UpdatedAt)
	if err != nil {
		return err
	}
	workflow, workflowErr := s.GetWorkflow(ctx, schedule.WorkflowID)
	if workflowErr == nil {
		return s.persistProjectConfig(ctx, workflow.ProjectID)
	}
	return nil
}

func (s *Store) ListSchedules(ctx context.Context) ([]model.Schedule, error) {
	return s.listSchedules(ctx, "")
}

func (s *Store) ListSchedulesForProject(ctx context.Context, projectID string) ([]model.Schedule, error) {
	return s.listSchedules(ctx, strings.TrimSpace(projectID))
}

func (s *Store) listSchedules(ctx context.Context, projectID string) ([]model.Schedule, error) {
	query := `SELECT s.id, s.name, s.workflow_id, s.cron, s.timezone, s.enabled, s.next_run_at, s.last_run_at, s.created_at, s.updated_at FROM schedules s LEFT JOIN workflows w ON w.id = s.workflow_id`
	args := make([]any, 0, 1)
	if projectID != "" {
		query += ` WHERE w.project_id = ? OR w.project_id = ''`
		args = append(args, projectID)
	}
	query += ` ORDER BY s.name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Schedule, 0)
	for rows.Next() {
		var schedule model.Schedule
		var enabled int
		if err := rows.Scan(&schedule.ID, &schedule.Name, &schedule.WorkflowID, &schedule.Cron, &schedule.Timezone, &enabled, &schedule.NextRunAt, &schedule.LastRunAt, &schedule.CreatedAt, &schedule.UpdatedAt); err != nil {
			return nil, err
		}
		schedule.Enabled = enabled != 0
		result = append(result, schedule)
	}
	return result, rows.Err()
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	var workflowID string
	lookupErr := s.db.QueryRowContext(ctx, "SELECT workflow_id FROM schedules WHERE id = ?", id).Scan(&workflowID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM schedules WHERE id = ?", id); err != nil {
		return err
	}
	if lookupErr == nil {
		if workflow, err := s.GetWorkflow(ctx, workflowID); err == nil {
			return s.persistProjectConfig(ctx, workflow.ProjectID)
		}
	}
	return nil
}

func (s *Store) ListSystemPrompts(ctx context.Context) ([]model.SystemPrompt, error) {
	overrides, err := s.readSystemPromptOverrides(ctx)
	if err != nil {
		return nil, err
	}
	prompts := model.DefaultSystemPrompts()
	for index := range prompts {
		prompt := &prompts[index]
		prompt.Template = prompt.DefaultTemplate
		if override, ok := overrides[prompt.ID]; ok {
			if model.ValidateSystemPrompt(prompt.ID, override.Template) == nil {
				prompt.Template = override.Template
			}
			prompt.IsCustomized = override.Template != prompt.DefaultTemplate
			prompt.UpdatedAt = override.UpdatedAt
		}
	}
	return prompts, nil
}

func (s *Store) SystemPromptTemplates(ctx context.Context) (map[string]string, error) {
	prompts, err := s.ListSystemPrompts(ctx)
	if err != nil {
		return nil, err
	}
	templates := make(map[string]string, len(prompts))
	for _, prompt := range prompts {
		templates[prompt.ID] = prompt.Template
	}
	return templates, nil
}

func (s *Store) SaveSystemPrompt(ctx context.Context, promptID, template string) (model.SystemPrompt, error) {
	if err := model.ValidateSystemPrompt(promptID, template); err != nil {
		return model.SystemPrompt{}, err
	}
	defaults := model.DefaultSystemPromptTemplates()
	overrides, err := s.readSystemPromptOverrides(ctx)
	if err != nil {
		return model.SystemPrompt{}, err
	}
	if template == defaults[promptID] {
		delete(overrides, promptID)
	} else {
		overrides[promptID] = storedSystemPromptOverride{Template: template, UpdatedAt: now()}
	}
	if err := s.writeSystemPromptOverrides(ctx, overrides); err != nil {
		return model.SystemPrompt{}, err
	}
	prompts, err := s.ListSystemPrompts(ctx)
	if err != nil {
		return model.SystemPrompt{}, err
	}
	for _, prompt := range prompts {
		if prompt.ID == promptID {
			return prompt, nil
		}
	}
	return model.SystemPrompt{}, fmt.Errorf("unknown system prompt %q", promptID)
}

func (s *Store) ResetSystemPrompt(ctx context.Context, promptID string) (model.SystemPrompt, error) {
	if !model.SystemPromptExists(promptID) {
		return model.SystemPrompt{}, fmt.Errorf("unknown system prompt %q", promptID)
	}
	overrides, err := s.readSystemPromptOverrides(ctx)
	if err != nil {
		return model.SystemPrompt{}, err
	}
	delete(overrides, promptID)
	if err := s.writeSystemPromptOverrides(ctx, overrides); err != nil {
		return model.SystemPrompt{}, err
	}
	prompts, err := s.ListSystemPrompts(ctx)
	if err != nil {
		return model.SystemPrompt{}, err
	}
	for _, prompt := range prompts {
		if prompt.ID == promptID {
			return prompt, nil
		}
	}
	return model.SystemPrompt{}, fmt.Errorf("unknown system prompt %q", promptID)
}

func (s *Store) readSystemPromptOverrides(ctx context.Context) (map[string]storedSystemPromptOverride, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, "SELECT value_json FROM settings WHERE key = ?", systemPromptOverridesKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return make(map[string]storedSystemPromptOverride), nil
	}
	if err != nil {
		return nil, err
	}
	overrides := make(map[string]storedSystemPromptOverride)
	if strings.TrimSpace(encoded) == "" {
		return overrides, nil
	}
	if err := json.Unmarshal([]byte(encoded), &overrides); err == nil {
		return overrides, nil
	}
	legacy := make(map[string]string)
	if err := json.Unmarshal([]byte(encoded), &legacy); err != nil {
		return nil, fmt.Errorf("decode system prompt settings: %w", err)
	}
	for promptID, template := range legacy {
		overrides[promptID] = storedSystemPromptOverride{Template: template}
	}
	return overrides, nil
}

func (s *Store) writeSystemPromptOverrides(ctx context.Context, overrides map[string]storedSystemPromptOverride) error {
	encoded, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("encode system prompt settings: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, value_json) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`, systemPromptOverridesKey, string(encoded))
	return err
}

func (s *Store) getSettingString(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value_json FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var decoded string
	if json.Unmarshal([]byte(value), &decoded) == nil {
		return decoded, nil
	}
	return value, nil
}

func (s *Store) setSettingString(ctx context.Context, key, value string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, value_json) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`, key, string(encoded))
	return err
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}

func scanAgent(scanner interface{ Scan(...any) error }) (model.AgentProfile, error) {
	var agent model.AgentProfile
	var roots, tools string
	err := scanner.Scan(&agent.ID, &agent.ProjectID, &agent.Name, &agent.Role, &agent.Instructions, &agent.ModelID, &agent.ReasoningEffort, &roots, &tools, &agent.ApprovalProfile, &agent.RoomID, &agent.AvatarID, &agent.VisualState, &agent.MaxDurationSeconds, &agent.MaxTurns, &agent.MaxAttempts, &agent.MemorySummary, &agent.CreatedAt, &agent.UpdatedAt)
	if err != nil {
		return agent, err
	}
	if err := json.Unmarshal([]byte(roots), &agent.WorkspaceRoots); err != nil {
		return agent, err
	}
	if err := json.Unmarshal([]byte(tools), &agent.ToolAllowlist); err != nil {
		return agent, err
	}
	return agent, nil
}

func scanRun(scanner interface{ Scan(...any) error }) (model.Run, error) {
	var run model.Run
	var input, output string
	var completed sql.NullString
	var cancelRequested, paused int
	err := scanner.Scan(&run.ID, &run.ProjectID, &run.WorkflowID, &run.Status, &input, &output, &run.CurrentNodeID, &run.StartedAt, &run.UpdatedAt, &completed, &run.Error, &cancelRequested, &paused, &run.PromptTokensUsed, &run.PromptTokenBudget, &run.OutputBytes)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(input), &run.Input); err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(output), &run.Output); err != nil {
		return run, err
	}
	run.CompletedAt = completed.String
	run.CancelRequested = cancelRequested != 0
	run.Paused = paused != 0
	return run, nil
}

func jsonString(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func redactedJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return "", err
	}
	redacted, err := json.Marshal(redactJSONValue(normalized))
	if err != nil {
		return "", err
	}
	return string(redacted), nil
}

func redactJSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		return security.RedactSensitiveText(typed)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = redactJSONValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactJSONValue(item)
		}
		return result
	default:
		return value
	}
}

func truncateRedacted(value string, maxRunes int) string {
	value = security.RedactSensitiveText(strings.TrimSpace(value))
	if maxRunes <= 0 || len([]rune(value)) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "\n[truncated]"
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
