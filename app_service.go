package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/RuanFernandes/centurion/internal/codex"
	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/orchestrator"
	projectmanifest "github.com/RuanFernandes/centurion/internal/project"
	"github.com/RuanFernandes/centurion/internal/scheduler"
	"github.com/RuanFernandes/centurion/internal/security"
	"github.com/RuanFernandes/centurion/internal/store"
	"github.com/google/uuid"
)

type AppService struct {
	store    *store.Store
	codex    *codex.AppServer
	executor *orchestrator.Executor

	mu             sync.RWMutex
	emitter        func(string, any)
	auth           model.AuthState
	approvals      map[string]*pendingApproval
	connectMu      sync.Mutex
	connected      bool
	monitorCancel  context.CancelFunc
	builderMu      sync.Mutex
	builderBusy    bool
	builderThreads map[string]time.Time
	plannerThreads map[string]time.Time
}

type pendingApproval struct {
	request      model.ApprovalRequest
	response     chan bool
	serverID     []byte
	serverParams map[string]any
	serverMethod string
}

func NewAppService(dataStore *store.Store) *AppService {
	service := &AppService{
		store:          dataStore,
		auth:           model.AuthState{Status: model.AuthStatusChecking, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		approvals:      make(map[string]*pendingApproval),
		builderThreads: make(map[string]time.Time),
		plannerThreads: make(map[string]time.Time),
	}
	service.codex = codex.NewAppServer("codex", service.handleCodexNotification, service.handleServerRequest)
	service.executor = orchestrator.NewExecutor(dataStore, service.codex, service.requestApproval, service.emitRunEvent, service.emitAgentState)
	return service
}

func (s *AppService) setEmitter(emitter func(string, any)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
	s.emitAuth()
}

func (s *AppService) Connect(ctx context.Context) {
	s.connectMu.Lock()
	if s.connected {
		s.connectMu.Unlock()
		return
	}
	connectionContext, cancel := context.WithCancel(ctx)
	if err := s.codex.Start(connectionContext); err != nil {
		cancel()
		s.connectMu.Unlock()
		s.mu.Lock()
		s.auth = model.AuthState{Status: model.AuthStatusOffline, Error: err.Error(), UpdatedAt: now()}
		s.mu.Unlock()
		s.emitAuth()
		return
	}
	s.connected = true
	s.monitorCancel = cancel
	s.connectMu.Unlock()
	s.mu.Lock()
	s.auth = s.codex.AuthState()
	s.mu.Unlock()
	s.emitAuth()
	s.emitRuntimeCatalog()
	_ = s.executor.RecoverInterruptedRuns(context.Background())
	go s.monitorConnection(connectionContext)
}

func (s *AppService) Close() {
	s.connectMu.Lock()
	s.connected = false
	cancel := s.monitorCancel
	s.monitorCancel = nil
	s.connectMu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = s.codex.Stop()
}

func (s *AppService) monitorConnection(ctx context.Context) {
	connectionTicker := time.NewTicker(4 * time.Second)
	usageTicker := time.NewTicker(60 * time.Second)
	defer connectionTicker.Stop()
	defer usageTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-usageTicker.C:
			if s.codex.IsConnected() {
				refreshContext, cancel := context.WithTimeout(ctx, 15*time.Second)
				_ = s.codex.RefreshRateLimits(refreshContext)
				cancel()
			}
		case <-connectionTicker.C:
		}
		if s.codex.IsConnected() {
			continue
		}
		s.mu.Lock()
		s.auth = model.AuthState{Status: model.AuthStatusOffline, Error: "Codex App Server exited; reconnecting.", UpdatedAt: now()}
		s.mu.Unlock()
		s.emitAuth()
		retryContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.codex.Start(retryContext)
		cancel()
		if err != nil {
			continue
		}
		s.mu.Lock()
		s.auth = s.codex.AuthState()
		s.mu.Unlock()
		s.emitAuth()
		s.emitRuntimeCatalog()
	}
}

func (s *AppService) GetAuthState() model.AuthState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.auth
}

func (s *AppService) GetRuntimeStatus() model.RuntimeStatus {
	status := s.codex.RuntimeStatus()
	status.ActiveRuns = s.executor.ActiveRunCount()
	return status
}

func (s *AppService) ListModels() ([]model.ModelInfo, error) {
	if err := s.ensureConnected(); err != nil {
		return s.codex.Models(), err
	}
	ctx, cancel := operationContext()
	defer cancel()
	models, err := s.codex.ListModels(ctx)
	if err == nil {
		s.emit("models.updated", model.ModelsUpdatedEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "codex", Models: models})
	}
	return models, err
}

func (s *AppService) ListSystemPrompts() ([]model.SystemPrompt, error) {
	ctx, cancel := operationContext()
	defer cancel()
	return s.store.ListSystemPrompts(ctx)
}

func (s *AppService) UpdateSystemPrompt(promptID, template string) (model.SystemPrompt, error) {
	if strings.TrimSpace(promptID) == "" {
		return model.SystemPrompt{}, errors.New("promptID is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	return s.store.SaveSystemPrompt(ctx, promptID, template)
}

func (s *AppService) ResetSystemPrompt(promptID string) (model.SystemPrompt, error) {
	if strings.TrimSpace(promptID) == "" {
		return model.SystemPrompt{}, errors.New("promptID is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	return s.store.ResetSystemPrompt(ctx, promptID)
}

func (s *AppService) ListMCPServers() ([]model.MCPServer, error) {
	if err := s.ensureConnected(); err != nil {
		return s.codex.MCPServers(), err
	}
	ctx, cancel := operationContext()
	defer cancel()
	servers, err := s.codex.ListMCPServers(ctx)
	if err == nil {
		s.emit("mcp.status", model.MCPStatusEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "codex", Servers: servers})
	}
	return servers, err
}

func (s *AppService) LoginMCPServer(serverID string) (string, error) {
	if strings.TrimSpace(serverID) == "" {
		return "", errors.New("serverID is required")
	}
	if err := s.ensureConnected(); err != nil {
		return "", err
	}
	ctx, cancel := operationContext()
	defer cancel()
	return s.codex.LoginMCPServer(ctx, serverID)
}

func (s *AppService) ReloadMCPServers() error {
	if err := s.ensureConnected(); err != nil {
		return err
	}
	ctx, cancel := operationContext()
	defer cancel()
	if err := s.codex.ReloadMCPServers(ctx); err != nil {
		return err
	}
	s.emit("mcp.status", model.MCPStatusEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "codex", Servers: s.codex.MCPServers()})
	return nil
}

func (s *AppService) ListAgents() ([]model.AgentProfile, error) {
	ctx, cancel := operationContext()
	defer cancel()
	if project, err := s.store.GetActiveProject(ctx); err == nil {
		return s.store.ListAgentsForProject(ctx, project.ID)
	}
	return s.store.ListAgents(ctx)
}

func (s *AppService) CreateAgent(profile model.AgentProfile) (model.AgentProfile, error) {
	if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Role) == "" {
		return model.AgentProfile{}, errors.New("agent name and role are required")
	}
	if profile.ID == "" {
		profile.ID = uuid.NewString()
	}
	if profile.VisualState == "" {
		profile.VisualState = model.AgentStateIdle
	}
	if profile.ApprovalProfile == "" {
		profile.ApprovalProfile = "on_request"
	}
	if profile.MaxDurationSeconds <= 0 {
		profile.MaxDurationSeconds = 1800
	}
	if profile.MaxTurns <= 0 {
		profile.MaxTurns = 12
	}
	if profile.MaxAttempts <= 0 {
		profile.MaxAttempts = 2
	}
	ctx, cancel := operationContext()
	defer cancel()
	if profile.ProjectID == "" {
		if active, err := s.store.GetActiveProject(ctx); err == nil {
			profile.ProjectID = active.ID
		}
	}
	if len(profile.WorkspaceRoots) > 0 {
		roots, err := security.NormalizeRoots(profile.WorkspaceRoots)
		if err != nil {
			return model.AgentProfile{}, err
		}
		profile.WorkspaceRoots = roots
	}
	if err := s.store.SaveAgent(ctx, profile); err != nil {
		return model.AgentProfile{}, err
	}
	return profile, nil
}

func (s *AppService) UpdateAgent(profile model.AgentProfile) (model.AgentProfile, error) {
	if strings.TrimSpace(profile.ID) == "" {
		return model.AgentProfile{}, errors.New("agent id is required")
	}
	if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Role) == "" {
		return model.AgentProfile{}, errors.New("agent name and role are required")
	}
	if len(profile.WorkspaceRoots) > 0 {
		roots, err := security.NormalizeRoots(profile.WorkspaceRoots)
		if err != nil {
			return model.AgentProfile{}, err
		}
		profile.WorkspaceRoots = roots
	}
	if profile.ApprovalProfile == "" {
		profile.ApprovalProfile = "on_request"
	}
	if profile.MaxDurationSeconds <= 0 {
		profile.MaxDurationSeconds = 1800
	}
	if profile.MaxTurns <= 0 {
		profile.MaxTurns = 12
	}
	if profile.MaxAttempts <= 0 {
		profile.MaxAttempts = 2
	}
	ctx, cancel := operationContext()
	defer cancel()
	if profile.ProjectID == "" {
		if existing, err := s.store.GetAgent(ctx, profile.ID); err == nil {
			profile.ProjectID = existing.ProjectID
		}
		if profile.ProjectID == "" {
			if active, err := s.store.GetActiveProject(ctx); err == nil {
				profile.ProjectID = active.ID
			}
		}
	}
	profile.UpdatedAt = now()
	if err := s.store.SaveAgent(ctx, profile); err != nil {
		return model.AgentProfile{}, err
	}
	return profile, nil
}

func (s *AppService) DeleteAgent(agentID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.store.DeleteAgent(ctx, agentID)
}

func (s *AppService) ValidateWorkflow(definition model.WorkflowDefinition) model.WorkflowValidation {
	return orchestrator.ValidateWorkflow(definition)
}

func (s *AppService) SaveWorkflow(definition model.WorkflowDefinition) (model.WorkflowDefinition, error) {
	if definition.ID == "" {
		definition.ID = uuid.NewString()
	}
	validation := orchestrator.ValidateWorkflow(definition)
	if !validation.Valid {
		return model.WorkflowDefinition{}, fmt.Errorf("workflow is invalid: %s", validation.Errors[0].Message)
	}
	if definition.Version <= 0 {
		definition.Version = 1
	}
	if definition.CreatedAt == "" {
		definition.CreatedAt = now()
	}
	definition.UpdatedAt = now()
	ctx, cancel := operationContext()
	defer cancel()
	if definition.ProjectID == "" {
		if active, err := s.store.GetActiveProject(ctx); err == nil {
			definition.ProjectID = active.ID
		}
	}
	if err := s.store.SaveWorkflow(ctx, definition); err != nil {
		return model.WorkflowDefinition{}, err
	}
	return definition, nil
}

func (s *AppService) DeleteWorkflow(workflowID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.store.DeleteWorkflow(ctx, workflowID)
}

func (s *AppService) LoadWorkflow(workflowID string) (model.WorkflowDefinition, error) {
	ctx, cancel := operationContext()
	defer cancel()
	return s.store.GetWorkflow(ctx, workflowID)
}

func (s *AppService) ListWorkflows() ([]model.WorkflowDefinition, error) {
	ctx, cancel := operationContext()
	defer cancel()
	if project, err := s.store.GetActiveProject(ctx); err == nil {
		return s.store.ListWorkflowsForProject(ctx, project.ID)
	}
	return s.store.ListWorkflows(ctx)
}

func (s *AppService) ListProjects() ([]model.Project, error) {
	ctx, cancel := operationContext()
	defer cancel()
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for index := range projects {
		projects[index] = withManifestPath(projects[index])
	}
	return projects, nil
}

func (s *AppService) GetActiveProject() (model.Project, error) {
	ctx, cancel := operationContext()
	defer cancel()
	project, err := s.store.GetActiveProject(ctx)
	if err != nil {
		return model.Project{}, err
	}
	return withManifestPath(project), nil
}

func (s *AppService) CreateProject(project model.Project) (model.Project, error) {
	project.Name = strings.TrimSpace(project.Name)
	if project.Name == "" {
		return model.Project{}, errors.New("project name is required")
	}
	if project.ID == "" {
		project.ID = uuid.NewString()
	}
	if err := normalizeProjectFolders(&project); err != nil {
		return model.Project{}, err
	}
	project.CreatedAt = now()
	project.UpdatedAt = project.CreatedAt
	manifestPath, err := projectmanifest.WriteManifest(project)
	if err != nil {
		return model.Project{}, err
	}
	project.ManifestPath = manifestPath
	ctx, cancel := operationContext()
	defer cancel()
	if err := s.store.SaveProject(ctx, project); err != nil {
		return model.Project{}, err
	}
	if err := s.store.EnsureProjectConfig(ctx, project.ID); err != nil {
		return model.Project{}, fmt.Errorf("create project config: %w", err)
	}
	_ = s.store.AppendHistory(ctx, model.HistoryEntry{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		Kind:      "project",
		Title:     "Project created",
		Content:   project.Name,
		Metadata:  map[string]any{"folders": project.Folders},
		CreatedAt: now(),
	})
	_ = s.store.AppendAudit(ctx, model.AuditEntry{ProjectID: project.ID, Kind: "project.created", Actor: "user", Target: project.ID, Detail: "Project created", Metadata: map[string]any{"folders": project.Folders}, CreatedAt: now()})
	return project, nil
}

func (s *AppService) UpdateProject(project model.Project) (model.Project, error) {
	project.ID = strings.TrimSpace(project.ID)
	project.Name = strings.TrimSpace(project.Name)
	if project.ID == "" {
		return model.Project{}, errors.New("project id is required")
	}
	if project.Name == "" {
		return model.Project{}, errors.New("project name is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	existing, err := s.store.GetProject(ctx, project.ID)
	if err != nil {
		return model.Project{}, err
	}
	if err := normalizeProjectFolders(&project); err != nil {
		return model.Project{}, err
	}
	project.CreatedAt = existing.CreatedAt
	project.UpdatedAt = now()
	project.LastOpenedAt = existing.LastOpenedAt
	manifestPath, err := projectmanifest.WriteManifest(project)
	if err != nil {
		return model.Project{}, err
	}
	project.ManifestPath = manifestPath
	if err := s.store.SaveProject(ctx, project); err != nil {
		return model.Project{}, err
	}
	if err := s.store.EnsureProjectConfig(ctx, project.ID); err != nil {
		return model.Project{}, fmt.Errorf("update project config: %w", err)
	}
	_ = s.store.AppendAudit(ctx, model.AuditEntry{ProjectID: project.ID, Kind: "project.updated", Actor: "user", Target: project.ID, Detail: "Project settings updated", Metadata: map[string]any{"folders": project.Folders}, CreatedAt: now()})
	return project, nil
}

func (s *AppService) SetActiveProject(projectID string) (model.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return model.Project{}, errors.New("project id is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	if err := s.store.SetActiveProject(ctx, projectID); err != nil {
		return model.Project{}, err
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return model.Project{}, err
	}
	project = withManifestPath(project)
	_ = s.store.AppendHistory(ctx, model.HistoryEntry{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		Kind:      "project",
		Title:     "Project opened",
		Content:   project.Name,
		CreatedAt: now(),
	})
	_ = s.store.AppendAudit(ctx, model.AuditEntry{ProjectID: project.ID, Kind: "project.opened", Actor: "user", Target: project.ID, Detail: "Project opened", CreatedAt: now()})
	s.emit("project.updated", project)
	return project, nil
}

func (s *AppService) DeleteProject(projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("project id is required")
	}
	ctx, cancel := operationContext()
	defer cancel()
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return err
	}
	if len(projects) <= 1 {
		return errors.New("the last project cannot be deleted")
	}
	active, activeErr := s.store.GetActiveProject(ctx)
	if err := s.store.DeleteProject(ctx, projectID); err != nil {
		return err
	}
	_ = s.store.AppendAudit(ctx, model.AuditEntry{ProjectID: projectID, Kind: "project.deleted", Actor: "user", Target: projectID, Detail: "Project deleted", CreatedAt: now()})
	if activeErr == nil && active.ID == projectID {
		for _, project := range projects {
			if project.ID != projectID {
				if err := s.store.SetActiveProject(ctx, project.ID); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (s *AppService) ListHistory(filter model.HistoryFilter) ([]model.HistoryEntry, error) {
	ctx, cancel := operationContext()
	defer cancel()
	if strings.TrimSpace(filter.ProjectID) == "" {
		if project, err := s.store.GetActiveProject(ctx); err == nil {
			filter.ProjectID = project.ID
		}
	}
	return s.store.ListHistory(ctx, filter)
}

func (s *AppService) ListAudit(filter model.AuditFilter) ([]model.AuditEntry, error) {
	ctx, cancel := operationContext()
	defer cancel()
	if strings.TrimSpace(filter.ProjectID) == "" {
		if project, err := s.store.GetActiveProject(ctx); err == nil {
			filter.ProjectID = project.ID
		}
	}
	return s.store.ListAudit(ctx, filter)
}

func (s *AppService) ExportProjectSnapshot(projectID string) (string, error) {
	ctx, cancel := operationContext()
	defer cancel()
	project, err := projectForID(ctx, s.store, projectID)
	if err != nil {
		return "", err
	}
	agents, err := s.store.ListAgentsForProject(ctx, project.ID)
	if err != nil {
		return "", err
	}
	workflows, err := s.store.ListWorkflowsForProject(ctx, project.ID)
	if err != nil {
		return "", err
	}
	schedules, err := s.store.ListSchedulesForProject(ctx, project.ID)
	if err != nil {
		return "", err
	}
	history, err := s.store.ListHistory(ctx, model.HistoryFilter{ProjectID: project.ID, Limit: 500})
	if err != nil {
		return "", err
	}
	path, err := projectmanifest.WriteSnapshot(model.ProjectSnapshot{
		SchemaVersion: 1,
		ExportedAt:    now(),
		Project:       project,
		Agents:        agents,
		Workflows:     workflows,
		Schedules:     schedules,
		History:       history,
	})
	if err != nil {
		return "", err
	}
	_ = s.store.AppendAudit(ctx, model.AuditEntry{ProjectID: project.ID, Kind: "project.export", Actor: "user", Target: path, Detail: "Project snapshot exported", CreatedAt: now()})
	return path, nil
}

func (s *AppService) StartRun(workflowID string, input map[string]any) (model.Run, error) {
	ctx, cancel := operationContext()
	defer cancel()
	workflow, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return model.Run{}, err
	}
	project, err := s.store.GetActiveProject(ctx)
	if err != nil {
		return model.Run{}, fmt.Errorf("load active project: %w", err)
	}
	if workflow.ProjectID != "" && workflow.ProjectID != project.ID {
		if trigger, _ := input["trigger"].(string); trigger == "schedule" {
			project, err = s.store.GetProject(ctx, workflow.ProjectID)
			if err != nil {
				return model.Run{}, fmt.Errorf("load scheduled workflow project: %w", err)
			}
		} else {
			return model.Run{}, errors.New("workflow belongs to a different project")
		}
	}
	run, err := s.executor.Start(ctx, workflow, input, project)
	if err == nil {
		encodedInput, _ := json.Marshal(input)
		_ = s.store.AppendHistory(ctx, model.HistoryEntry{
			ID:        uuid.NewString(),
			ProjectID: run.ProjectID,
			Kind:      "conversation",
			Title:     workflow.Name + " run started",
			Content:   string(encodedInput),
			Metadata:  map[string]any{"runID": run.ID, "workflowID": workflow.ID},
			CreatedAt: now(),
		})
		_ = s.store.AppendAudit(ctx, model.AuditEntry{ProjectID: run.ProjectID, RunID: run.ID, Kind: "run.started", Actor: "user", Target: workflow.ID, Detail: "Workflow run started", CreatedAt: now()})
		s.emit("run.created", run)
	}
	return run, err
}

func (s *AppService) RunTerminalCommand(projectID, command, workingDir string) (model.TerminalResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return model.TerminalResult{}, errors.New("terminal command is required")
	}
	if len(command) > 4000 {
		return model.TerminalResult{}, errors.New("terminal command exceeds the 4000 character limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	project, err := projectForID(ctx, s.store, projectID)
	if err != nil {
		return model.TerminalResult{}, err
	}
	if len(project.Folders) == 0 {
		return model.TerminalResult{}, errors.New("active project has no folders")
	}
	if strings.TrimSpace(workingDir) == "" {
		workingDir = project.Folders[0]
	}
	workingDir, err = security.NormalizePath(workingDir)
	if err != nil {
		return model.TerminalResult{}, err
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		return model.TerminalResult{}, fmt.Errorf("working directory is unavailable: %w", err)
	}
	if !info.IsDir() || !security.IsWithinRoots(workingDir, project.Folders) {
		return model.TerminalResult{}, security.ErrPathOutsideWorkspace
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/D", "/S", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-lc", command)
	}
	cmd.Dir = workingDir
	output := &cappedBuffer{limit: 256 * 1024}
	cmd.Stdout = output
	cmd.Stderr = output
	started := time.Now()
	runErr := cmd.Run()
	duration := time.Since(started).Milliseconds()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	if ctx.Err() != nil {
		exitCode = -1
		if output.Len() == 0 {
			_, _ = output.WriteString("Command timed out after 120 seconds.")
		}
	}
	result := model.TerminalResult{Command: command, WorkingDir: workingDir, Output: output.String(), ExitCode: exitCode, DurationMS: duration, Truncated: output.truncated}
	_ = s.store.AppendHistory(context.Background(), model.HistoryEntry{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		Kind:      "terminal",
		Title:     command,
		Content:   result.Output,
		Metadata:  map[string]any{"workingDir": workingDir, "exitCode": exitCode, "durationMs": duration},
		CreatedAt: now(),
	})
	_ = s.store.AppendAudit(context.Background(), model.AuditEntry{ProjectID: project.ID, Kind: "terminal.command", Actor: "user", Target: workingDir, Decision: commandDecision(exitCode), Detail: command, Metadata: map[string]any{"exitCode": exitCode, "durationMs": duration, "truncated": result.Truncated}, CreatedAt: now()})
	return result, nil
}

func (s *AppService) PauseRun(runID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.Pause(ctx, runID)
}

func (s *AppService) ResumeRun(runID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.Resume(ctx, runID)
}

func (s *AppService) CancelRun(runID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.Cancel(ctx, runID)
}

func (s *AppService) RetryStep(runID, stepID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.RetryStep(ctx, runID, stepID)
}

func (s *AppService) SteerRun(runID, message string) error {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.Steer(ctx, runID, message)
}

func (s *AppService) GetRun(runID string) (model.Run, error) {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.GetRun(ctx, runID)
}

func (s *AppService) ListRuns(filter model.RunFilter) ([]model.Run, error) {
	ctx, cancel := operationContext()
	defer cancel()
	if strings.TrimSpace(filter.ProjectID) == "" {
		if project, err := s.store.GetActiveProject(ctx); err == nil {
			filter.ProjectID = project.ID
		}
	}
	return s.executor.ListRuns(ctx, filter)
}

func (s *AppService) GetRunEvents(runID string, afterSequence int64) ([]model.RunEvent, error) {
	ctx, cancel := operationContext()
	defer cancel()
	return s.executor.Events(ctx, runID, afterSequence)
}

func (s *AppService) ResolveApproval(decision model.ApprovalDecision) error {
	if strings.TrimSpace(decision.ID) == "" {
		return errors.New("approval id is required")
	}
	decision.Decision = strings.ToLower(strings.TrimSpace(decision.Decision))
	if decision.Decision != "approve" && decision.Decision != "accept" && decision.Decision != "decline" && decision.Decision != "cancel" {
		return errors.New("approval decision must be approve, accept, decline or cancel")
	}
	s.mu.Lock()
	pending, ok := s.approvals[decision.ID]
	if ok && pending.response != nil {
		delete(s.approvals, decision.ID)
		s.mu.Unlock()
		pending.response <- decision.Decision == "approve" || decision.Decision == "accept"
		s.appendApprovalAudit(pending.request, decision.Decision, "user")
		s.emit("approval.resolved", decision)
		return nil
	}
	if ok {
		delete(s.approvals, decision.ID)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("approval request not found or expired")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response := serverApprovalResponse(pending, decision.Decision)
	if response == nil {
		s.mu.Lock()
		s.approvals[decision.ID] = pending
		s.mu.Unlock()
		return errors.New("this server request requires structured user input and cannot be approved from the quick decision")
	}
	if err := s.codexRespond(ctx, pending.serverID, response); err != nil {
		return err
	}
	s.appendApprovalAudit(pending.request, decision.Decision, "user")
	s.emit("approval.resolved", decision)
	return nil
}

func (s *AppService) CreateSchedule(schedule model.Schedule) (model.Schedule, error) {
	if schedule.ID == "" {
		schedule.ID = uuid.NewString()
	}
	if schedule.Name == "" || schedule.WorkflowID == "" || schedule.Cron == "" {
		return model.Schedule{}, errors.New("schedule name, workflowID and cron are required")
	}
	if schedule.Timezone == "" {
		schedule.Timezone = "Local"
	}
	ctx, cancel := operationContext()
	defer cancel()
	if err := s.store.SaveSchedule(ctx, schedule); err != nil {
		return model.Schedule{}, err
	}
	if executable, err := os.Executable(); err == nil {
		if err := scheduler.Register(schedule, executable); err != nil {
			return schedule, fmt.Errorf("schedule persisted but could not be registered: %w", err)
		}
	}
	s.emit("scheduler.updated", model.SchedulerEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "scheduler", Action: "saved", ScheduleID: schedule.ID})
	return schedule, nil
}

func (s *AppService) UpdateSchedule(schedule model.Schedule) (model.Schedule, error) {
	if schedule.ID == "" {
		return model.Schedule{}, errors.New("schedule id is required")
	}
	schedule.UpdatedAt = now()
	ctx, cancel := operationContext()
	defer cancel()
	if err := s.store.SaveSchedule(ctx, schedule); err != nil {
		return model.Schedule{}, err
	}
	if executable, err := os.Executable(); err == nil {
		if err := scheduler.Register(schedule, executable); err != nil {
			return schedule, fmt.Errorf("schedule persisted but could not be registered: %w", err)
		}
	}
	s.emit("scheduler.updated", model.SchedulerEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "scheduler", Action: "updated", ScheduleID: schedule.ID})
	return schedule, nil
}

func (s *AppService) DeleteSchedule(scheduleID string) error {
	ctx, cancel := operationContext()
	defer cancel()
	if err := s.store.DeleteSchedule(ctx, scheduleID); err != nil {
		return err
	}
	if err := scheduler.Unregister(scheduleID); err != nil {
		return err
	}
	s.emit("scheduler.updated", model.SchedulerEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "scheduler", Action: "deleted", ScheduleID: scheduleID})
	return nil
}

func (s *AppService) ListSchedules() ([]model.Schedule, error) {
	ctx, cancel := operationContext()
	defer cancel()
	if project, err := s.store.GetActiveProject(ctx); err == nil {
		return s.store.ListSchedulesForProject(ctx, project.ID)
	}
	return s.store.ListSchedules(ctx)
}

func (s *AppService) GetDiagnostics() []string {
	return s.codex.Diagnostics()
}

func (s *AppService) ensureConnected() error {
	s.connectMu.Lock()
	connected := s.connected
	s.connectMu.Unlock()
	if connected && s.codex.IsConnected() {
		return nil
	}
	return errors.New("Codex App Server is unavailable; install the Codex CLI and try again")
}

func (s *AppService) requestApproval(ctx context.Context, request model.ApprovalRequest) (bool, error) {
	response := make(chan bool, 1)
	s.mu.Lock()
	s.approvals[request.ID] = &pendingApproval{request: request, response: response}
	s.mu.Unlock()
	s.appendApprovalAudit(request, "requested", "system")
	s.emit("approval.requested", request)
	select {
	case approved := <-response:
		decision := "decline"
		if approved {
			decision = "approve"
		}
		s.appendApprovalAudit(request, decision, "user")
		return approved, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.approvals, request.ID)
		s.mu.Unlock()
		s.emit("approval.resolved", model.ApprovalDecision{ID: request.ID, Decision: "timeout"})
		s.appendApprovalAudit(request, "timeout", "system")
		return false, ctx.Err()
	}
}

func (s *AppService) appendApprovalAudit(request model.ApprovalRequest, decision, actor string) {
	projectID := ""
	if request.RunID != "" {
		if run, err := s.store.GetRun(context.Background(), request.RunID); err == nil {
			projectID = run.ProjectID
		}
	}
	_ = s.store.AppendAudit(context.Background(), model.AuditEntry{
		ID:        request.ID + "-" + decision,
		ProjectID: projectID,
		RunID:     request.RunID,
		Kind:      "approval." + decision,
		Actor:     actor,
		Target:    request.Kind,
		Decision:  decision,
		Detail:    request.Detail,
		Metadata:  map[string]any{"title": request.Title, "threadID": request.ThreadID, "turnID": request.TurnID},
		CreatedAt: now(),
	})
}

func commandDecision(exitCode int) string {
	if exitCode == 0 {
		return "completed"
	}
	return "failed"
}

func (s *AppService) handleServerRequest(request codex.ServerRequest) {
	params := make(map[string]any)
	_ = json.Unmarshal(request.Params, &params)
	id := strings.Trim(strings.TrimSpace(string(request.ID)), "\"")
	if id == "" {
		return
	}
	kind := "codex"
	title := "Codex action requires approval"
	detail := request.Method
	switch request.Method {
	case "item/commandExecution/requestApproval":
		kind = "command"
		title = "Allow command execution?"
		if command, ok := params["command"].(string); ok && command != "" {
			detail = command
		}
	case "item/fileChange/requestApproval":
		kind = "file_change"
		title = "Allow file change?"
	case "item/permissions/requestApproval":
		kind = "permissions"
		title = "Grant additional permissions?"
	case "mcpServer/elicitation/request", "tool/requestUserInput":
		kind = "user_input"
		title = "Codex needs a response"
	}
	requestModel := model.ApprovalRequest{SchemaVersion: 1, Timestamp: now(), ID: id, Kind: kind, Title: title, Detail: detail, ThreadID: stringValue(params["threadId"]), TurnID: stringValue(params["turnId"]), ItemID: stringValue(params["itemId"]), Choices: []string{"accept", "decline"}}
	s.mu.Lock()
	s.approvals[id] = &pendingApproval{request: requestModel, serverID: append([]byte(nil), request.ID...), serverParams: params, serverMethod: request.Method}
	s.mu.Unlock()
	s.appendApprovalAudit(requestModel, "requested", "codex")
	s.emit("approval.requested", requestModel)
	time.AfterFunc(5*time.Minute, func() {
		s.mu.Lock()
		pending, exists := s.approvals[id]
		if exists && pending.response == nil {
			delete(s.approvals, id)
		}
		s.mu.Unlock()
		if exists && pending.response == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if response := serverApprovalResponse(pending, "decline"); response != nil {
				_ = s.codexRespond(ctx, request.ID, response)
			} else {
				_ = s.codexRespondError(ctx, request.ID, &codex.RPCError{Code: -32000, Message: "approval request expired without structured user input"})
			}
			cancel()
			s.emit("approval.resolved", model.ApprovalDecision{ID: id, Decision: "timeout"})
			s.appendApprovalAudit(requestModel, "timeout", "system")
		}
	})
}

func (s *AppService) codexRespond(ctx context.Context, id []byte, result any) error {
	return s.codexRespondRaw(ctx, id, result)
}

func (s *AppService) codexRespondError(ctx context.Context, id []byte, rpcErr *codex.RPCError) error {
	return s.codex.RespondServerRequestError(ctx, id, rpcErr)
}

func (s *AppService) codexRespondRaw(ctx context.Context, id []byte, result any) error {
	return s.codex.RespondServerRequest(ctx, id, result)
}

func (s *AppService) handleCodexNotification(notification model.CodexNotification) {
	if notification.Method == "account/state" {
		if raw, ok := notification.Params["auth"]; ok {
			if encoded, err := json.Marshal(raw); err == nil {
				var auth model.AuthState
				if json.Unmarshal(encoded, &auth) == nil {
					s.mu.Lock()
					s.auth = auth
					s.mu.Unlock()
					s.emit("auth.updated", auth)
				}
			}
		}
		return
	}
	if strings.HasPrefix(notification.Method, "mcpServer/") {
		s.emit("mcp.status", model.MCPStatusEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "codex", Servers: s.codex.MCPServers()})
	}
	s.emit("codex.notification", notification)
}

func (s *AppService) emitRunEvent(event model.RunEvent) {
	s.emit("run.event", event)
	s.emit("run.updated", model.RunUpdateEvent{SchemaVersion: 1, Timestamp: now(), Sequence: event.Sequence, Source: "orchestrator", RunID: event.RunID, Event: event.Type})
}

func (s *AppService) emitAgentState(event model.AgentStateEvent) {
	s.emit("office.agent.state", event)
}

func (s *AppService) emitAuth() {
	s.emit("auth.updated", s.GetAuthState())
}

func (s *AppService) emitRuntimeCatalog() {
	s.emit("models.updated", model.ModelsUpdatedEvent{
		SchemaVersion: 1,
		Timestamp:     now(),
		Sequence:      time.Now().UnixNano(),
		Source:        "codex",
		Models:        s.codex.Models(),
	})
	s.emit("mcp.status", model.MCPStatusEvent{
		SchemaVersion: 1,
		Timestamp:     now(),
		Sequence:      time.Now().UnixNano(),
		Source:        "codex",
		Servers:       s.codex.MCPServers(),
	})
}

func (s *AppService) emit(name string, value any) {
	s.mu.RLock()
	emitter := s.emitter
	s.mu.RUnlock()
	if emitter != nil {
		emitter(name, value)
	}
}

func operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func normalizeProjectFolders(project *model.Project) error {
	if project == nil || len(project.Folders) == 0 {
		return errors.New("at least one project folder is required")
	}
	folders := make([]string, 0, len(project.Folders))
	for _, folder := range project.Folders {
		if strings.TrimSpace(folder) != "" {
			folders = append(folders, folder)
		}
	}
	normalized, err := security.NormalizeRoots(folders)
	if err != nil {
		return err
	}
	project.Folders = normalized
	return nil
}

func withManifestPath(value model.Project) model.Project {
	if path, err := projectmanifest.ManifestPath(value.Folders); err == nil {
		value.ManifestPath = path
	}
	return value
}

func projectForID(ctx context.Context, dataStore *store.Store, projectID string) (model.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return dataStore.GetActiveProject(ctx)
	}
	return dataStore.GetProject(ctx, strings.TrimSpace(projectID))
}

type cappedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(data), nil
	}
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.truncated = true
		return len(data), nil
	}
	_, err := b.Buffer.Write(data)
	return len(data), err
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func serverApprovalResponse(pending *pendingApproval, decision string) map[string]any {
	if pending == nil {
		return nil
	}
	switch pending.serverMethod {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": decision}
	case "item/permissions/requestApproval":
		if decision != "accept" && decision != "approve" {
			return map[string]any{"permissions": []any{}, "scope": "turn"}
		}
		requested, ok := pending.serverParams["permissions"]
		if !ok {
			return map[string]any{"permissions": []any{}, "scope": "turn"}
		}
		return map[string]any{"permissions": requested, "scope": "turn"}
	case "mcpServer/elicitation/request":
		if decision == "accept" || decision == "approve" {
			return nil
		}
		return map[string]any{"action": "decline", "content": nil}
	case "tool/requestUserInput", "item/tool/requestUserInput":
		return nil
	default:
		return map[string]any{"decision": decision}
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
