package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RuanFernandes/centurion/internal/codex"
	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/security"
	"github.com/RuanFernandes/centurion/internal/store"
	"github.com/google/uuid"
)

type ApprovalRequester func(context.Context, model.ApprovalRequest) (bool, error)

const defaultPromptTokenBudget = 12000

var ErrPromptBudgetExceeded = errors.New("workflow prompt token budget exceeded")
var ErrAgentBlocked = errors.New("agent reported a blocking result")

type AgentBlockedError struct {
	AgentName string
	NodeID    string
	Reason    string
	Output    map[string]any
}

func (e *AgentBlockedError) Error() string {
	if e == nil {
		return ErrAgentBlocked.Error()
	}
	name := strings.TrimSpace(e.AgentName)
	if name == "" {
		name = "Agent"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "the agent returned a blocking result"
	}
	return fmt.Sprintf("%s blocked workflow node %q: %s", name, e.NodeID, reason)
}

func (e *AgentBlockedError) Unwrap() error { return ErrAgentBlocked }

type Executor struct {
	store           *store.Store
	codex           *codex.AppServer
	requestApproval ApprovalRequester
	emitEvent       func(model.RunEvent)
	emitAgentState  func(model.AgentStateEvent)

	mu             sync.Mutex
	runtimes       map[string]*runtime
	usageMu        sync.Mutex
	closeOnce      sync.Once
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	closed         bool
}

type runtime struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	paused          bool
	resumeCh        chan struct{}
	threadID        string
	turnID          string
	maxTurns        int
	turnCount       int
	agentTurns      map[string]int
	promptTokens    int
	maxPromptTokens int
	outputBytes     int
	allowedRoots    []string
	projectRoots    []string
	projectID       string
}

type branchResult struct {
	joinID string
	scope  map[string]any
	err    error
}

func NewExecutor(dataStore *store.Store, appServer *codex.AppServer, requestApproval ApprovalRequester, emitEvent func(model.RunEvent), emitAgentState func(model.AgentStateEvent)) *Executor {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	return &Executor{
		store:           dataStore,
		codex:           appServer,
		requestApproval: requestApproval,
		emitEvent:       emitEvent,
		emitAgentState:  emitAgentState,
		runtimes:        make(map[string]*runtime),
		shutdownCtx:     shutdownCtx,
		shutdownCancel:  shutdownCancel,
	}
}

// Close stops every active workflow runtime before the Codex process is
// closed. The runtime context is also used by delay, approval, and other
// non-Codex steps, so a shutdown cannot leave background work behind.
func (e *Executor) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		runtimes := make([]*runtime, 0, len(e.runtimes))
		for _, rt := range e.runtimes {
			runtimes = append(runtimes, rt)
		}
		e.mu.Unlock()
		e.shutdownCancel()
		for _, rt := range runtimes {
			rt.cancel()
		}
	})
}

func (e *Executor) Start(ctx context.Context, workflow model.WorkflowDefinition, input map[string]any, projects ...model.Project) (model.Run, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return model.Run{}, errors.New("orchestrator is closed")
	}
	validation := ValidateWorkflow(workflow)
	if !validation.Valid {
		return model.Run{}, fmt.Errorf("workflow is invalid: %s", validation.Errors[0].Message)
	}
	if e.workflowNeedsCodex(workflow) {
		if err := e.codex.CanRun(); err != nil {
			return model.Run{}, err
		}
	}
	nowValue := time.Now().UTC().Format(time.RFC3339Nano)
	run := model.Run{
		ID:         uuid.NewString(),
		WorkflowID: workflow.ID,
		// Interactive runs are admitted immediately. There is no in-process
		// worker queue today, so exposing queued here only creates a stale
		// snapshot between Start and the execution goroutine.
		Status:    model.RunStatusRunning,
		Input:     cloneMap(input),
		Output:    cloneMap(input),
		StartedAt: nowValue,
		UpdatedAt: nowValue,
	}
	run.PromptTokenBudget = promptBudget(workflow)
	if len(projects) > 0 {
		run.ProjectID = projects[0].ID
	}
	if err := e.store.CreateRun(ctx, run); err != nil {
		return model.Run{}, err
	}
	e.appendEvent(ctx, model.RunEvent{RunID: run.ID, Type: "run.created", Source: "orchestrator", Level: "info", Message: "Run created", Data: map[string]any{"workflowID": workflow.ID, "projectID": run.ProjectID}})
	var projectRoots []string
	if len(projects) > 0 {
		projectRoots = projects[0].Folders
	}
	e.startRuntime(run, workflow, workflow.EntryNodeID, projectRoots)
	return run, nil
}

func (e *Executor) Pause(ctx context.Context, runID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if isTerminalRunStatus(run.Status) {
		return fmt.Errorf("run %s cannot be paused from status %s", runID, run.Status)
	}
	e.mu.Lock()
	rt := e.runtimes[runID]
	e.mu.Unlock()
	if rt == nil {
		run.Paused = true
		run.Status = model.RunStatusPaused
		run.UpdatedAt = now()
		return e.store.UpdateRun(ctx, run)
	}
	rt.mu.Lock()
	rt.paused = true
	rt.mu.Unlock()
	run.Paused = true
	run.Status = model.RunStatusPaused
	run.UpdatedAt = now()
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.appendEvent(ctx, model.RunEvent{RunID: runID, Type: "run.paused", Source: "orchestrator", Level: "info", Message: "Run paused; the active step will finish safely"})
	return nil
}

func (e *Executor) Resume(ctx context.Context, runID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if isTerminalRunStatus(run.Status) {
		return fmt.Errorf("run %s cannot be resumed from status %s", runID, run.Status)
	}
	if run.CancelRequested {
		return fmt.Errorf("run %s has been canceled and cannot be resumed", runID)
	}
	e.mu.Lock()
	rt := e.runtimes[runID]
	e.mu.Unlock()
	if rt != nil {
		rt.mu.Lock()
		wasPaused := rt.paused
		if wasPaused {
			close(rt.resumeCh)
			rt.resumeCh = make(chan struct{})
		}
		rt.paused = false
		rt.mu.Unlock()
		run.Paused = false
		run.Status = model.RunStatusRunning
		run.UpdatedAt = now()
		if err := e.store.UpdateRun(ctx, run); err != nil {
			return err
		}
		e.appendEvent(ctx, model.RunEvent{RunID: runID, Type: "run.resumed", Source: "orchestrator", Level: "info", Message: "Run resumed"})
		return nil
	}
	workflow, err := e.store.GetWorkflow(ctx, run.WorkflowID)
	if err != nil {
		return err
	}
	if validation := ValidateWorkflow(workflow); !validation.Valid {
		return fmt.Errorf("workflow is invalid: %s", validation.Errors[0].Message)
	}
	if run.Status != model.RunStatusPaused && run.Status != model.RunStatusInterrupted {
		return fmt.Errorf("run %s cannot be resumed from status %s", runID, run.Status)
	}
	run.Paused = false
	run.Status = model.RunStatusRunning
	run.UpdatedAt = now()
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return err
	}
	startNode := run.CurrentNodeID
	if startNode == "" {
		startNode = workflow.EntryNodeID
	}
	e.startRuntime(run, workflow, startNode, e.loadProjectRoots(ctx, run.ProjectID))
	return nil
}

func (e *Executor) Cancel(ctx context.Context, runID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if isTerminalRunStatus(run.Status) {
		if run.Status == model.RunStatusCanceled {
			return nil
		}
		return fmt.Errorf("run %s cannot be canceled from status %s", runID, run.Status)
	}
	e.mu.Lock()
	rt := e.runtimes[runID]
	e.mu.Unlock()
	run.CancelRequested = true
	run.Status = model.RunStatusCanceled
	run.UpdatedAt = now()
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return err
	}
	if rt != nil {
		threadID, turnID := rt.activeTurn()
		if threadID != "" && turnID != "" {
			interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = e.codex.Interrupt(interruptCtx, threadID, turnID)
			cancel()
		}
		rt.cancel()
	}
	e.appendEvent(ctx, model.RunEvent{RunID: runID, Type: "run.canceled", Source: "orchestrator", Level: "warning", Message: "Cancellation requested"})
	return nil
}

func (e *Executor) RetryStep(ctx context.Context, runID, stepID string) error {
	e.mu.Lock()
	active := e.runtimes[runID] != nil
	e.mu.Unlock()
	if active {
		return fmt.Errorf("run %s is still active; cancel or wait for it before retrying a step", runID)
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	workflow, err := e.store.GetWorkflow(ctx, run.WorkflowID)
	if err != nil {
		return err
	}
	if validation := ValidateWorkflow(workflow); !validation.Valid {
		return fmt.Errorf("workflow is invalid: %s", validation.Errors[0].Message)
	}
	if _, exists := nodeByID(workflow.Nodes)[stepID]; !exists {
		return fmt.Errorf("step %s does not exist in workflow", stepID)
	}
	run.CurrentNodeID = stepID
	run.Status = model.RunStatusRunning
	run.Error = ""
	run.CancelRequested = false
	run.Paused = false
	run.CompletedAt = ""
	run.UpdatedAt = now()
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.startRuntime(run, workflow, stepID, e.loadProjectRoots(ctx, run.ProjectID))
	return nil
}

func (e *Executor) Steer(ctx context.Context, runID, message string) error {
	e.mu.Lock()
	rt := e.runtimes[runID]
	e.mu.Unlock()
	if rt == nil {
		return errors.New("run is not actively executing")
	}
	threadID, turnID := rt.activeTurn()
	if threadID == "" || turnID == "" {
		return errors.New("run has no active Codex turn")
	}
	if err := e.codex.Steer(ctx, threadID, turnID, message); err != nil {
		return err
	}
	e.appendEvent(ctx, model.RunEvent{RunID: runID, Type: "run.steer", Source: "orchestrator", Level: "info", Message: "Guidance sent to agent", Data: map[string]any{"message": message}})
	return nil
}

func (e *Executor) GetRun(ctx context.Context, runID string) (model.Run, error) {
	return e.store.GetRun(ctx, runID)
}

func (e *Executor) ActiveRunCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.runtimes)
}

func (e *Executor) ListRuns(ctx context.Context, filter model.RunFilter) ([]model.Run, error) {
	return e.store.ListRuns(ctx, filter)
}

func (e *Executor) Events(ctx context.Context, runID string, afterSequence int64) ([]model.RunEvent, error) {
	return e.store.ListRunEvents(ctx, runID, afterSequence)
}

func (e *Executor) RecoverInterruptedRuns(ctx context.Context) error {
	for _, status := range []string{model.RunStatusQueued, model.RunStatusRunning, model.RunStatusWaitingApproval} {
		runs, err := e.store.ListRuns(ctx, model.RunFilter{Status: status, Limit: 200})
		if err != nil {
			return err
		}
		for _, run := range runs {
			run.Status = model.RunStatusInterrupted
			run.Error = "The application restarted before this run finished; review the checkpoint before trying again."
			if status == model.RunStatusQueued {
				run.Error = "This run was left queued by an older Centurion session; review the workflow and resume it."
			}
			checkpoint := map[string]any{"currentNodeID": run.CurrentNodeID}
			if run.CurrentNodeID != "" {
				if step, stepErr := e.store.GetRunStep(ctx, run.ID, run.CurrentNodeID); stepErr == nil {
					checkpoint["stepStatus"] = step.Status
					checkpoint["attempt"] = step.Attempt
					checkpoint["threadID"] = step.ThreadID
					checkpoint["turnID"] = step.TurnID
				}
			}
			run.UpdatedAt = now()
			if err := e.store.UpdateRun(ctx, run); err != nil {
				return err
			}
			e.appendEvent(ctx, model.RunEvent{RunID: run.ID, Type: "run.recovered", Source: "orchestrator", Level: "warning", Message: run.Error, Data: checkpoint})
		}
	}
	return nil
}

func (e *Executor) startRuntime(run model.Run, workflow model.WorkflowDefinition, startNode string, projectRoots ...[]string) {
	runContext, cancel := context.WithCancel(e.shutdownCtx)
	configuredProjectRoots := make([]string, 0)
	if len(projectRoots) > 0 {
		configuredProjectRoots = append(configuredProjectRoots, projectRoots[0]...)
		if normalized, err := security.NormalizeRoots(configuredProjectRoots); err == nil {
			configuredProjectRoots = normalized
		} else {
			configuredProjectRoots = nil
		}
	}
	allowedRoots := append([]string(nil), configuredProjectRoots...)
	if len(allowedRoots) == 0 {
		allowedRoots = make([]string, 0)
	}
	for _, node := range workflow.Nodes {
		if len(configuredProjectRoots) > 0 {
			break
		}
		if node.Type != "agent" || node.AgentID == "" {
			continue
		}
		agent, err := e.store.GetAgent(context.Background(), node.AgentID)
		if err == nil {
			allowedRoots = append(allowedRoots, agent.WorkspaceRoots...)
		}
	}
	if len(allowedRoots) > 0 {
		if normalized, err := security.NormalizeRoots(allowedRoots); err == nil {
			allowedRoots = normalized
		}
	}
	rt := &runtime{
		ctx:             runContext,
		cancel:          cancel,
		resumeCh:        make(chan struct{}),
		maxTurns:        workflow.GlobalLimits.MaxTurns,
		agentTurns:      make(map[string]int),
		maxPromptTokens: promptBudget(workflow),
		allowedRoots:    allowedRoots,
		projectRoots:    append([]string(nil), configuredProjectRoots...),
		projectID:       run.ProjectID,
	}
	e.mu.Lock()
	if previous := e.runtimes[run.ID]; previous != nil {
		previous.cancel()
	}
	e.runtimes[run.ID] = rt
	e.mu.Unlock()
	go e.execute(rt, run, workflow, startNode)
}

func promptBudget(workflow model.WorkflowDefinition) int {
	if workflow.GlobalLimits.MaxPromptTokens <= 0 {
		return defaultPromptTokenBudget
	}
	return workflow.GlobalLimits.MaxPromptTokens
}

func (e *Executor) loadProjectRoots(ctx context.Context, projectID string) []string {
	if strings.TrimSpace(projectID) == "" {
		return nil
	}
	project, err := e.store.GetProject(ctx, projectID)
	if err != nil {
		return nil
	}
	return append([]string(nil), project.Folders...)
}

func (e *Executor) execute(rt *runtime, run model.Run, workflow model.WorkflowDefinition, startNode string) {
	defer func() {
		e.mu.Lock()
		// A manual retry can replace a runtime while the old goroutine is
		// unwinding. Never let the old goroutine delete the replacement.
		if current := e.runtimes[run.ID]; current == rt {
			delete(e.runtimes, run.ID)
		}
		e.mu.Unlock()
	}()
	if err := e.updateRunStatus(run.ID, model.RunStatusRunning, ""); err != nil {
		return
	}
	deadline := workflow.GlobalLimits.MaxDurationSeconds
	if deadline <= 0 {
		deadline = 1800
	}
	ctx, cancel := context.WithTimeout(rt.ctx, time.Duration(deadline)*time.Second)
	defer cancel()
	scope := cloneMap(run.Output)
	if len(scope) == 0 {
		scope = cloneMap(run.Input)
	}
	nodes := nodeByID(workflow.Nodes)
	counts := make(map[string]int)
	parallelSem := make(chan struct{}, maxInt(workflow.GlobalLimits.MaxParallel, 1))
	current := startNode
	var executionErr error
	for current != "" {
		if err := e.waitIfPaused(ctx, rt); err != nil {
			executionErr = err
			break
		}
		if err := ctx.Err(); err != nil {
			executionErr = err
			break
		}
		node, ok := nodes[current]
		if !ok {
			executionErr = fmt.Errorf("node %s does not exist", current)
			break
		}
		run.CurrentNodeID = current
		run.Status = model.RunStatusRunning
		run.UpdatedAt = now()
		e.syncRuntimeUsage(&run, rt)
		_ = e.store.UpdateRun(context.Background(), run)
		if node.Type == "loop" {
			counts[node.ID]++
			if counts[node.ID] > node.MaxIterations {
				if exhausted, ok := node.Config["onExhausted"].(string); ok && exhausted != "" {
					current = exhausted
					continue
				}
				executionErr = fmt.Errorf("loop %s exceeded maxIterations=%d", node.ID, node.MaxIterations)
				break
			}
		}
		if node.Type == "parallel" {
			joinID, err := e.executeParallel(ctx, rt, run.ID, node, workflow, scope, run.Input, parallelSem)
			if err != nil {
				executionErr = err
				break
			}
			run.Output = cloneMap(scope)
			run.CurrentNodeID = joinID
			run.Paused = rt.isPaused()
			if run.Paused {
				run.Status = model.RunStatusPaused
			} else {
				run.Status = model.RunStatusRunning
			}
			run.UpdatedAt = now()
			e.syncRuntimeUsage(&run, rt)
			_ = e.store.UpdateRun(context.Background(), run)
			current = joinID
			continue
		}
		output, err := e.executeWithRetry(ctx, rt, run.ID, node, scope, workflow, run.Input)
		if err != nil {
			// A semantic blocker is an intentional stop from an agent, not a
			// transient node failure. Never let a workflow-level "continue" or
			// "fallback" policy turn a refused implementation into a completed run.
			if errors.Is(err, ErrAgentBlocked) {
				executionErr = err
				break
			}
			next, handled, policyErr := e.handleNodeError(ctx, rt, run.ID, node, err, workflow, scope)
			if policyErr != nil {
				executionErr = policyErr
				break
			}
			if !handled {
				executionErr = err
				break
			}
			run.Output = cloneMap(scope)
			run.CurrentNodeID = next
			run.Paused = rt.isPaused()
			if run.Paused {
				run.Status = model.RunStatusPaused
			} else {
				run.Status = model.RunStatusRunning
			}
			run.UpdatedAt = now()
			e.syncRuntimeUsage(&run, rt)
			_ = e.store.UpdateRun(context.Background(), run)
			current = next
			continue
		}
		mergeScope(scope, node.ID, output)
		run.Output = cloneMap(scope)
		next, err := chooseNext(workflow.Edges, node.ID, scope)
		if err != nil {
			executionErr = err
			break
		}
		if next == "" && node.Type == "condition" {
			executionErr = fmt.Errorf("condition node %s did not match any outgoing edge", node.ID)
			break
		}
		run.CurrentNodeID = next
		run.Paused = rt.isPaused()
		if run.Paused {
			run.Status = model.RunStatusPaused
		} else {
			run.Status = model.RunStatusRunning
		}
		e.syncRuntimeUsage(&run, rt)
		_ = e.store.UpdateRun(context.Background(), run)
		if node.Type == "join" {
			// Join nodes are synchronization points; their first matching edge is enough.
		}
		current = next
	}

	finalStatus := model.RunStatusCompleted
	errorMessage := ""
	if executionErr != nil {
		errorMessage = executionErr.Error()
		if errors.Is(executionErr, context.Canceled) || runWasCanceled(e.store, run.ID) {
			finalStatus = model.RunStatusCanceled
		} else if errors.Is(executionErr, context.DeadlineExceeded) {
			finalStatus = model.RunStatusInterrupted
		} else if errors.Is(executionErr, ErrAgentBlocked) {
			finalStatus = model.RunStatusBlocked
		} else {
			finalStatus = model.RunStatusFailed
		}
	}
	finalRun, getErr := e.store.GetRun(context.Background(), run.ID)
	if getErr == nil {
		finalRun.Status = finalStatus
		finalRun.Output = cloneMap(scope)
		finalRun.Error = errorMessage
		finalRun.CurrentNodeID = current
		finalRun.UpdatedAt = now()
		finalRun.CompletedAt = now()
		finalRun.Paused = false
		e.syncRuntimeUsage(&finalRun, rt)
		_ = e.store.UpdateRun(context.Background(), finalRun)
	}
	e.appendEvent(context.Background(), model.RunEvent{RunID: run.ID, Type: "run." + finalStatus, Source: "orchestrator", Level: levelForStatus(finalStatus), Message: messageForStatus(finalStatus, errorMessage), Data: map[string]any{"error": errorMessage}})
}

func (e *Executor) executeParallel(ctx context.Context, rt *runtime, runID string, parallelNode model.WorkflowNode, workflow model.WorkflowDefinition, scope, input map[string]any, semaphore chan struct{}) (string, error) {
	outgoing := outgoingEdges(workflow.Edges, parallelNode.ID)
	if len(outgoing) < 2 {
		return "", errors.New("parallel node requires at least two outgoing branches")
	}
	results := make([]branchResult, len(outgoing))
	parallelCtx, cancelParallel := context.WithCancel(ctx)
	defer cancelParallel()
	var cancelOnce sync.Once
	var waitGroup sync.WaitGroup
	for index, edge := range outgoing {
		index, edge := index, edge
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-parallelCtx.Done():
				results[index].err = parallelCtx.Err()
				return
			}
			branchScope := cloneMap(scope)
			results[index] = branchResult{scope: branchScope}
			joinID, err := e.executeBranch(parallelCtx, rt, runID, edge.To, workflow, branchScope, input, semaphore)
			results[index].joinID = joinID
			results[index].err = err
			if err != nil {
				// Stop sibling branches as soon as one branch fails. This avoids
				// spending more turns or performing more side effects after the
				// workflow is already known to fail.
				cancelOnce.Do(cancelParallel)
			}
		}()
	}
	waitGroup.Wait()
	joinID := ""
	if err := preferredParallelError(ctx.Err(), results); err != nil {
		return "", err
	}
	for _, result := range results {
		if joinID == "" {
			joinID = result.joinID
		} else if result.joinID != joinID {
			return "", errors.New("parallel branches must converge on the same join node")
		}
		for key, value := range result.scope {
			scope[key] = value
		}
	}
	if joinID == "" {
		return "", errors.New("parallel branches did not reach a join node")
	}
	e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "parallel.completed", Source: "orchestrator", Level: "info", NodeID: parallelNode.ID, Message: "Parallel workstreams completed"})
	return joinID, nil
}

// preferredParallelError preserves the actual failed branch when canceling it
// causes sibling branches to report context.Canceled. Without this ordering, a
// real file-operation failure can be mislabeled as a canceled workflow.
func preferredParallelError(parentErr error, results []branchResult) error {
	if parentErr != nil {
		return parentErr
	}
	for _, result := range results {
		if result.err != nil && errors.Is(result.err, ErrAgentBlocked) {
			return result.err
		}
	}
	for _, result := range results {
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			return result.err
		}
	}
	for _, result := range results {
		if result.err != nil {
			return result.err
		}
	}
	return nil
}

func (e *Executor) executeBranch(ctx context.Context, rt *runtime, runID, startNode string, workflow model.WorkflowDefinition, scope, input map[string]any, semaphore chan struct{}) (string, error) {
	nodes := nodeByID(workflow.Nodes)
	counts := make(map[string]int)
	current := startNode
	for current != "" {
		if err := e.waitIfPaused(ctx, rt); err != nil {
			return "", err
		}
		node, ok := nodes[current]
		if !ok {
			return "", fmt.Errorf("branch node %s does not exist", current)
		}
		if node.Type == "join" {
			return node.ID, nil
		}
		if node.Type == "parallel" {
			return "", errors.New("nested parallel nodes are not supported in the first executor")
		}
		if node.Type == "loop" {
			counts[node.ID]++
			if counts[node.ID] > node.MaxIterations {
				if exhausted, ok := node.Config["onExhausted"].(string); ok && exhausted != "" {
					current = exhausted
					continue
				}
				return "", fmt.Errorf("loop %s exceeded maxIterations=%d", node.ID, node.MaxIterations)
			}
		}
		output, err := e.executeWithRetry(ctx, rt, runID, node, scope, workflow, input)
		if err != nil {
			if errors.Is(err, ErrAgentBlocked) {
				return "", err
			}
			next, handled, policyErr := e.handleNodeError(ctx, rt, runID, node, err, workflow, scope)
			if policyErr != nil {
				return "", policyErr
			}
			if !handled {
				return "", err
			}
			current = next
			continue
		}
		mergeScope(scope, node.ID, output)
		next, err := chooseNext(workflow.Edges, node.ID, scope)
		if err != nil {
			return "", err
		}
		if next == "" && node.Type == "condition" {
			return "", fmt.Errorf("condition node %s did not match any outgoing edge", node.ID)
		}
		current = next
	}
	return "", errors.New("parallel branch ended without a join node")
}

func (e *Executor) executeWithRetry(ctx context.Context, rt *runtime, runID string, node model.WorkflowNode, scope map[string]any, workflow model.WorkflowDefinition, input map[string]any) (map[string]any, error) {
	maxAttempts := node.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if !node.Retry.Idempotent && maxAttempts > 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := e.waitIfPaused(ctx, rt); err != nil {
			return nil, err
		}
		startedAt := now()
		e.persistStep(runID, node.ID, "running", attempt, nil, "", startedAt, "")
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "step.started", Source: "orchestrator", Level: "info", NodeID: node.ID, AgentID: node.AgentID, Message: node.Label, Data: map[string]any{"attempt": attempt}})
		nodeContext := ctx
		cancel := func() {}
		if node.TimeoutSeconds > 0 {
			nodeContext, cancel = context.WithTimeout(ctx, time.Duration(node.TimeoutSeconds)*time.Second)
		}
		output, err := e.executeNode(nodeContext, rt, runID, node, scope, workflow, input)
		cancel()
		if err == nil {
			e.persistStep(runID, node.ID, "completed", attempt, output, "", startedAt, now())
			e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "step.completed", Source: "orchestrator", Level: "info", NodeID: node.ID, AgentID: node.AgentID, Message: node.Label + " completed", Data: map[string]any{"attempt": attempt}})
			return output, nil
		}
		lastErr = err
		stepStatus := "failed"
		eventType := "step.failed"
		eventLevel := "error"
		var stepOutput map[string]any
		var blockedErr *AgentBlockedError
		if errors.As(err, &blockedErr) {
			stepStatus = "blocked"
			eventType = "step.blocked"
			eventLevel = "warning"
			stepOutput = blockedErr.Output
		}
		e.persistStep(runID, node.ID, stepStatus, attempt, stepOutput, err.Error(), startedAt, now())
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: eventType, Source: "orchestrator", Level: eventLevel, NodeID: node.ID, AgentID: node.AgentID, Message: err.Error(), Data: map[string]any{"attempt": attempt}})
		if attempt == maxAttempts || errors.Is(err, ErrAgentBlocked) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			break
		}
		backoff := time.Duration(node.Retry.BackoffSeconds*attempt) * time.Second
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
		if err := e.waitForRetry(ctx, rt, backoff); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (e *Executor) waitForRetry(ctx context.Context, rt *runtime, backoff time.Duration) error {
	deadline := time.NewTimer(backoff)
	defer deadline.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-deadline.C:
		return e.waitIfPaused(ctx, rt)
	}
}

// handleNodeError applies the workflow-level error policy after a node has
// exhausted its retry budget. It returns the next node only when the policy
// explicitly handled the failure; the default is fail-fast.
func (e *Executor) handleNodeError(ctx context.Context, rt *runtime, runID string, node model.WorkflowNode, nodeErr error, workflow model.WorkflowDefinition, scope map[string]any) (string, bool, error) {
	policy := strings.ToLower(strings.TrimSpace(workflow.ErrorPolicy))
	if policy == "" || policy == "stop" {
		return "", false, nil
	}
	failureOutput := map[string]any{
		"status": "failed",
		"error":  nodeErr.Error(),
	}
	switch policy {
	case "continue":
		mergeScope(scope, node.ID, failureOutput)
		next, err := chooseNext(workflow.Edges, node.ID, scope)
		if err != nil {
			return "", false, err
		}
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "step.continued_after_error", Source: "orchestrator", Level: "warning", NodeID: node.ID, Message: "Step failed; workflow continued by policy", Data: failureOutput})
		return next, true, nil
	case "fallback":
		target, _ := node.Config["fallbackNodeID"].(string)
		target = strings.TrimSpace(target)
		if target == "" {
			return "", false, errors.New("fallback error policy requires a fallbackNodeID")
		}
		mergeScope(scope, node.ID, failureOutput)
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "step.fallback", Source: "orchestrator", Level: "warning", NodeID: node.ID, Message: "Step failed; workflow switched to its fallback", Data: map[string]any{"fallbackNodeID": target, "error": nodeErr.Error()}})
		return target, true, nil
	case "request_approval":
		if e.requestApproval == nil {
			return "", false, errors.New("approval broker is not configured for the workflow error policy")
		}
		request := model.ApprovalRequest{
			SchemaVersion: 1,
			Timestamp:     now(),
			ID:            uuid.NewString(),
			Kind:          "workflow_error",
			Title:         "Continue after step failure?",
			Detail:        fmt.Sprintf("%s failed: %s", node.Label, nodeErr.Error()),
			RunID:         runID,
			Choices:       []string{"continue", "stop"},
		}
		e.emitAgent(runID, "", model.AgentStateWaitingApproval, node.ID, request.Detail)
		_ = e.updateRunStatus(runID, model.RunStatusWaitingApproval, nodeErr.Error())
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "approval.requested", Source: "orchestrator", Level: "warning", NodeID: node.ID, Message: request.Title, Data: map[string]any{"error": nodeErr.Error()}})
		approved, err := e.requestApproval(ctx, request)
		if err != nil {
			return "", false, err
		}
		_ = e.updateRunStatus(runID, model.RunStatusRunning, "")
		if !approved {
			return "", false, nodeErr
		}
		mergeScope(scope, node.ID, failureOutput)
		next, err := chooseNext(workflow.Edges, node.ID, scope)
		if err != nil {
			return "", false, err
		}
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "step.continued_after_approval", Source: "orchestrator", Level: "warning", NodeID: node.ID, Message: "Step failure was approved for continuation"})
		return next, true, nil
	default:
		return "", false, nil
	}
}

func (e *Executor) executeNode(ctx context.Context, rt *runtime, runID string, node model.WorkflowNode, scope map[string]any, workflow model.WorkflowDefinition, input map[string]any) (map[string]any, error) {
	switch node.Type {
	case "agent":
		agent, err := e.store.GetAgent(ctx, node.AgentID)
		if err != nil {
			return nil, fmt.Errorf("load agent %s: %w", node.AgentID, err)
		}
		if len(rt.projectRoots) > 0 {
			agent.WorkspaceRoots = effectiveAgentRoots(agent.WorkspaceRoots, rt.projectRoots)
		}
		if err := rt.reserveTurn(agent.ID, agent.MaxTurns); err != nil {
			return nil, err
		}
		agentState := model.AgentStateWorking
		agentMessage := "Step ended"
		e.emitAgent(runID, agent.ID, model.AgentStateWorking, node.ID, "Executing instructions")
		defer func() { e.emitAgent(runID, agent.ID, agentState, node.ID, agentMessage) }()
		existingThreadID := ""
		if previousStep, stepErr := e.store.GetRunStep(context.Background(), runID, node.ID); stepErr == nil {
			existingThreadID = strings.TrimSpace(previousStep.ThreadID)
		}
		promptMode := "bootstrap"
		contextPolicy := "input-and-upstream-control-projected"
		var prompt string
		if existingThreadID != "" {
			promptMode = "continuation"
			contextPolicy = "thread-continuation"
			prompt = buildAgentContinuationPrompt(node)
		} else {
			promptTemplates, promptErr := e.store.SystemPromptTemplates(ctx)
			if promptErr != nil {
				agentState = model.AgentStateError
				agentMessage = promptErr.Error()
				return nil, fmt.Errorf("load system prompts: %w", promptErr)
			}
			prompt = buildAgentPrompt(agent, workflow, node, input, scope, promptTemplates)
		}
		estimatedPromptTokens := estimateTextTokens(prompt)
		usedPromptTokens, budgetErr := rt.reservePromptTokens(estimatedPromptTokens)
		if budgetErr != nil {
			agentState = model.AgentStateBlocked
			agentMessage = budgetErr.Error()
			e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "usage.budget_exceeded", Source: "orchestrator", Level: "warning", NodeID: node.ID, AgentID: agent.ID, Message: budgetErr.Error(), Data: map[string]any{
				"estimatedPromptTokens": estimatedPromptTokens,
				"usedPromptTokens":      usedPromptTokens,
				"promptTokenBudget":     rt.maxPromptTokens,
			}})
			e.updateRunUsage(runID, usedPromptTokens, rt.maxPromptTokens, rt.outputUsage())
			return nil, budgetErr
		}
		e.updateRunUsage(runID, usedPromptTokens, rt.maxPromptTokens, rt.outputUsage())
		_ = e.store.AppendHistory(context.Background(), model.HistoryEntry{
			ID:        uuid.NewString(),
			ProjectID: rt.projectID,
			Kind:      "prompt",
			Title:     agent.Name + " prompt",
			Content:   prompt,
			Metadata:  map[string]any{"runID": runID, "nodeID": node.ID, "agentID": agent.ID, "promptMode": promptMode, "threadReused": existingThreadID != ""},
			CreatedAt: now(),
		})
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "agent.started", Source: "orchestrator", Level: "info", NodeID: node.ID, AgentID: agent.ID, Message: agent.Name + " started working", Data: map[string]any{
			"promptBytes":           len(prompt),
			"estimatedPromptTokens": estimatedPromptTokens,
			"usedPromptTokens":      usedPromptTokens,
			"promptTokenBudget":     rt.maxPromptTokens,
			"promptMode":            promptMode,
			"threadReused":          existingThreadID != "",
			"contextPolicy":         contextPolicy,
			"contextBudgetBytes":    maxAgentPromptContextBytes,
		}})
		lastActivityKey := ""
		emitAgentActivity := func(method string) {
			phase := agentActivityForMethod(method)
			key := phase.Activity + "\x00" + phase.Detail
			if key == lastActivityKey {
				return
			}
			lastActivityKey = key
			e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "agent.activity", Source: "orchestrator", Level: "info", NodeID: node.ID, AgentID: agent.ID, Message: phase.Activity, Data: map[string]any{
				"detail": phase.Detail,
				"method": method,
			}})
		}
		emitAgentActivity("turn/started")
		agentContext := ctx
		cancel := func() {}
		if agent.MaxDurationSeconds > 0 {
			agentContext, cancel = context.WithTimeout(ctx, time.Duration(agent.MaxDurationSeconds)*time.Second)
		}
		onTurnStarted := func(threadID, turnID string) {
			rt.setTurn(threadID, turnID)
			// Persist the checkpoint as soon as Codex assigns the turn. If the
			// app closes mid-turn, recovery can show which thread was active
			// instead of treating the step as an opaque interruption.
			if step, stepErr := e.store.GetRunStep(context.Background(), runID, node.ID); stepErr == nil {
				step.ThreadID = threadID
				step.TurnID = turnID
				_ = e.store.UpsertRunStep(context.Background(), step)
			}
		}
		result, err := e.codex.RunAgentTurn(agentContext, agent, prompt, existingThreadID, onTurnStarted, func(notification codex.Notification) {
			// Keep the run log useful without storing notification payloads. Some
			// Codex notifications can contain private reasoning, prompt fragments,
			// tool arguments, or file contents.
			emitAgentActivity(notification.Method)
		})
		cancel()
		emitAgentActivity("turn/completed")
		outputBytes := rt.recordOutput(len(result.Output))
		e.updateRunUsage(runID, usedPromptTokens, rt.maxPromptTokens, outputBytes)
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "agent.output", Source: "orchestrator", Level: "info", NodeID: node.ID, AgentID: agent.ID, Message: agent.Name + " returned a result", Data: map[string]any{
			"status":      result.Status,
			"outputBytes": outputBytes,
			"threadID":    result.ThreadID,
			"turnID":      result.TurnID,
		}})
		if step, stepErr := e.store.GetRunStep(context.Background(), runID, node.ID); stepErr == nil {
			step.ThreadID = result.ThreadID
			step.TurnID = result.TurnID
			_ = e.store.UpsertRunStep(context.Background(), step)
		}
		rt.setTurn("", "")
		if err != nil {
			agentState = model.AgentStateError
			agentMessage = err.Error()
			return nil, err
		}
		if result.Output != "" {
			_ = e.store.AppendHistory(context.Background(), model.HistoryEntry{
				ID:        uuid.NewString(),
				ProjectID: rt.projectID,
				Kind:      "conversation",
				Title:     agent.Name + " response",
				Content:   result.Output,
				Metadata:  map[string]any{"runID": runID, "nodeID": node.ID, "agentID": agent.ID, "threadID": result.ThreadID, "turnID": result.TurnID},
				CreatedAt: now(),
			})
		}
		agentState = model.AgentStateSuccess
		agentMessage = "Step completed"
		if result.Output == "" {
			return map[string]any{"status": result.Status}, nil
		}
		structured := parseAgentOutput(result.Output, result.Status)
		if failed, reason := agentOutputFailed(structured); failed {
			agentState = model.AgentStateError
			agentMessage = reason
			e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "agent.failed", Source: "orchestrator", Level: "error", NodeID: node.ID, AgentID: agent.ID, Message: reason, Data: structured})
			return nil, fmt.Errorf("%s failed workflow node %q: %s", agent.Name, node.ID, reason)
		}
		if blocked, reason := agentOutputBlocked(structured); blocked {
			agentState = model.AgentStateBlocked
			agentMessage = reason
			e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "agent.blocked", Source: "orchestrator", Level: "warning", NodeID: node.ID, AgentID: agent.ID, Message: reason, Data: structured})
			return nil, &AgentBlockedError{AgentName: agent.Name, NodeID: node.ID, Reason: reason, Output: structured}
		}
		return structured, nil
	case "condition":
		value, err := EvaluateCondition(node.Condition, scope)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": value}, nil
	case "approval":
		if e.requestApproval == nil {
			return nil, errors.New("approval broker is not configured")
		}
		request := model.ApprovalRequest{SchemaVersion: 1, Timestamp: now(), ID: uuid.NewString(), Kind: "workflow", Title: node.Label, Detail: "This step requires confirmation before continuing.", RunID: runID, Choices: []string{"approve", "decline"}}
		e.emitAgent(runID, "", model.AgentStateWaitingApproval, node.ID, request.Detail)
		_ = e.updateRunStatus(runID, model.RunStatusWaitingApproval, "")
		e.appendEvent(context.Background(), model.RunEvent{RunID: runID, Type: "approval.requested", Source: "orchestrator", Level: "warning", NodeID: node.ID, Message: request.Title})
		approved, err := e.requestApproval(ctx, request)
		if err != nil {
			return nil, err
		}
		_ = e.updateRunStatus(runID, model.RunStatusRunning, "")
		if !approved {
			return nil, errors.New("approval declined")
		}
		return map[string]any{"approved": true}, nil
	case "tool":
		if node.ToolName == "delay" {
			milliseconds := 250
			if value, ok := node.Config["milliseconds"].(float64); ok {
				milliseconds = int(value)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(maxInt(milliseconds, 0)) * time.Millisecond):
			}
			return map[string]any{"tool": node.ToolName, "status": "completed"}, nil
		}
		if e.requestApproval == nil {
			return nil, errors.New("approval broker is not configured for a tool node")
		}
		request := model.ApprovalRequest{SchemaVersion: 1, Timestamp: now(), ID: uuid.NewString(), Kind: "tool", Title: "Run tool: " + node.ToolName, Detail: "Tool nodes require an explicit decision until an allowlist policy is configured.", RunID: runID, Choices: []string{"approve", "decline"}}
		approved, err := e.requestApproval(ctx, request)
		if err != nil {
			return nil, err
		}
		if !approved {
			return nil, errors.New("tool approval declined")
		}
		return map[string]any{"tool": node.ToolName, "status": "approved-for-codex"}, nil
	case "artifact":
		if strings.TrimSpace(node.ArtifactPath) == "" {
			return map[string]any{"recorded": true}, nil
		}
		roots := []string{}
		for _, value := range scope {
			if candidate, ok := value.(map[string]any); ok {
				if rawRoots, ok := candidate["workspaceRoots"].([]any); ok {
					for _, root := range rawRoots {
						if text, ok := root.(string); ok {
							roots = append(roots, text)
						}
					}
				}
			}
		}
		if len(roots) == 0 {
			roots = append(roots, rt.allowedRoots...)
		}
		path, err := security.ValidateArtifactPath(node.ArtifactPath, roots)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path}, nil
	case "join", "loop", "parallel":
		return map[string]any{"status": "completed"}, nil
	default:
		return nil, fmt.Errorf("unsupported node type %s", node.Type)
	}
}

func (e *Executor) waitIfPaused(ctx context.Context, rt *runtime) error {
	for {
		rt.mu.Lock()
		paused := rt.paused
		resumeCh := rt.resumeCh
		rt.mu.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resumeCh:
		}
	}
}

func (rt *runtime) reservePromptTokens(estimated int) (int, error) {
	if estimated <= 0 {
		return 0, nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.maxPromptTokens > 0 && rt.promptTokens+estimated > rt.maxPromptTokens {
		return rt.promptTokens, fmt.Errorf("%w: used %d of %d estimated tokens", ErrPromptBudgetExceeded, rt.promptTokens, rt.maxPromptTokens)
	}
	rt.promptTokens += estimated
	return rt.promptTokens, nil
}

func (rt *runtime) recordOutput(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.outputBytes += bytes
	return rt.outputBytes
}

func (rt *runtime) outputUsage() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.outputBytes
}

func (rt *runtime) usage() (int, int, int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.promptTokens, rt.maxPromptTokens, rt.outputBytes
}

func (e *Executor) syncRuntimeUsage(run *model.Run, rt *runtime) {
	if run == nil || rt == nil {
		return
	}
	promptTokens, promptBudget, outputBytes := rt.usage()
	if promptTokens > run.PromptTokensUsed {
		run.PromptTokensUsed = promptTokens
	}
	if promptBudget > 0 {
		run.PromptTokenBudget = promptBudget
	}
	if outputBytes > run.OutputBytes {
		run.OutputBytes = outputBytes
	}
}

func (e *Executor) updateRunUsage(runID string, promptTokens, promptBudget, outputBytes int) {
	e.usageMu.Lock()
	defer e.usageMu.Unlock()
	run, err := e.store.GetRun(context.Background(), runID)
	if err != nil {
		return
	}
	if promptTokens > run.PromptTokensUsed {
		run.PromptTokensUsed = promptTokens
	}
	if promptBudget > 0 {
		run.PromptTokenBudget = promptBudget
	}
	if outputBytes > run.OutputBytes {
		run.OutputBytes = outputBytes
	}
	run.UpdatedAt = now()
	_ = e.store.UpdateRun(context.Background(), run)
}

func (e *Executor) updateRunStatus(runID, status, errorMessage string) error {
	run, err := e.store.GetRun(context.Background(), runID)
	if err != nil {
		return err
	}
	changed := run.Status != status || run.Error != errorMessage
	run.Status = status
	run.Error = errorMessage
	run.UpdatedAt = now()
	if err := e.store.UpdateRun(context.Background(), run); err != nil {
		return err
	}
	if changed {
		level := "info"
		if status == model.RunStatusWaitingApproval {
			level = "warning"
		} else if status == model.RunStatusFailed || status == model.RunStatusInterrupted {
			level = "error"
		}
		e.appendEvent(context.Background(), model.RunEvent{
			RunID:   runID,
			Type:    "run.status",
			Source:  "orchestrator",
			Level:   level,
			Message: "Run status: " + status,
			Data: map[string]any{
				"status": status,
				"error":  errorMessage,
			},
		})
	}
	return nil
}

func (e *Executor) appendEvent(ctx context.Context, event model.RunEvent) {
	if event.Level == "" {
		event.Level = "info"
	}
	stored, err := e.store.AppendRunEvent(ctx, event)
	if err == nil && e.emitEvent != nil {
		e.emitEvent(stored)
	}
}

func (e *Executor) emitAgent(runID, agentID, state, nodeID, message string) {
	if e.emitAgentState == nil || agentID == "" {
		return
	}
	e.emitAgentState(model.AgentStateEvent{SchemaVersion: 1, Timestamp: now(), Sequence: time.Now().UnixNano(), Source: "orchestrator", RunID: runID, AgentID: agentID, State: state, NodeID: nodeID, Message: message})
}

func (e *Executor) workflowNeedsCodex(workflow model.WorkflowDefinition) bool {
	for _, node := range workflow.Nodes {
		if node.Type == "agent" {
			return true
		}
	}
	return false
}

func (rt *runtime) setTurn(threadID, turnID string) {
	rt.mu.Lock()
	rt.threadID = threadID
	rt.turnID = turnID
	rt.mu.Unlock()
}

func (rt *runtime) reserveTurn(agentID string, agentLimit int) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.maxTurns > 0 && rt.turnCount >= rt.maxTurns {
		return fmt.Errorf("workflow maxTurns=%d exceeded", rt.maxTurns)
	}
	if agentID != "" && agentLimit > 0 && rt.agentTurns[agentID] >= agentLimit {
		return fmt.Errorf("agent %s maxTurns=%d exceeded", agentID, agentLimit)
	}
	rt.turnCount++
	if agentID != "" {
		if rt.agentTurns == nil {
			rt.agentTurns = make(map[string]int)
		}
		rt.agentTurns[agentID]++
	}
	return nil
}

func (rt *runtime) isPaused() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.paused
}

func (rt *runtime) activeTurn() (string, string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.threadID, rt.turnID
}

func nodeByID(nodes []model.WorkflowNode) map[string]model.WorkflowNode {
	result := make(map[string]model.WorkflowNode, len(nodes))
	for _, node := range nodes {
		result[node.ID] = node
	}
	return result
}

func outgoingEdges(edges []model.WorkflowEdge, from string) []model.WorkflowEdge {
	result := make([]model.WorkflowEdge, 0)
	for _, edge := range edges {
		if edge.From == from {
			result = append(result, edge)
		}
	}
	return result
}

func chooseNext(edges []model.WorkflowEdge, from string, scope map[string]any) (string, error) {
	for _, edge := range outgoingEdges(edges, from) {
		ok, err := EvaluateCondition(edge.Condition, scope)
		if err != nil {
			return "", err
		}
		if ok {
			return edge.To, nil
		}
	}
	return "", nil
}

func mergeScope(scope map[string]any, nodeID string, output map[string]any) {
	if output == nil {
		return
	}
	scope[nodeID] = output
	for key, value := range output {
		if key != "text" && key != "status" {
			scope[nodeID+"."+key] = value
		}
	}
}

func (e *Executor) persistStep(runID, nodeID, status string, attempt int, output map[string]any, errorMessage, startedAt, completedAt string) {
	step, err := e.store.GetRunStep(context.Background(), runID, nodeID)
	if err != nil {
		step = store.RunStep{RunID: runID, NodeID: nodeID}
	}
	step.Status = status
	step.Attempt = attempt
	step.Output = output
	step.Error = errorMessage
	step.StartedAt = startedAt
	step.CompletedAt = completedAt
	_ = e.store.UpsertRunStep(context.Background(), step)
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return make(map[string]any)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var result map[string]any
	if json.Unmarshal(encoded, &result) != nil || result == nil {
		return map[string]any{}
	}
	return result
}

func runWasCanceled(dataStore *store.Store, runID string) bool {
	run, err := dataStore.GetRun(context.Background(), runID)
	return err == nil && run.CancelRequested
}

func parseAgentOutput(raw, turnStatus string) map[string]any {
	raw = strings.TrimSpace(raw)
	var structured map[string]any
	if json.Unmarshal([]byte(raw), &structured) == nil && structured != nil {
		return structured
	}
	if candidate := extractJSONObject(raw); candidate != "" && json.Unmarshal([]byte(candidate), &structured) == nil && structured != nil {
		if _, exists := structured["text"]; !exists {
			structured["_rawText"] = truncateText(raw, 6000)
		}
		return structured
	}
	return map[string]any{"text": raw, "status": turnStatus}
}

func extractJSONObject(value string) string {
	start := strings.IndexByte(value, '{')
	for start >= 0 && start < len(value) {
		depth := 0
		inString := false
		escaped := false
		for index := start; index < len(value); index++ {
			character := value[index]
			if inString {
				if escaped {
					escaped = false
					continue
				}
				if character == '\\' {
					escaped = true
				} else if character == '"' {
					inString = false
				}
				continue
			}
			switch character {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return value[start : index+1]
				}
			}
		}
		next := strings.IndexByte(value[start+1:], '{')
		if next < 0 {
			break
		}
		start += next + 1
	}
	return ""
}

func agentOutputBlocked(output map[string]any) (bool, string) {
	if output == nil {
		return false, ""
	}
	for _, key := range []string{"status", "result", "decision"} {
		value, ok := output[key].(string)
		if !ok {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch normalized {
		case "blocked", "bloqueado", "bloqueada", "not_ready", "not-ready", "not_ready_for_promotion", "not-ready-for-promotion", "implementation_not_started", "implementation-not-started", "implementation_handoff_not_created", "implementation-handoff-not-created", "do_not_route_builder", "do-not-route-builder", "do_not_approve", "do-not-approve", "scope_blocked", "scope-blocked":
			return true, agentBlockReason(output, value)
		}
	}
	return false, ""
}

// agentOutputFailed separates a completed transport turn from a failed task.
// Codex can correctly finish a turn while the agent reports that its scoped
// operation failed. Treat those result values as workflow failures rather than
// displaying a misleading completed state.
func agentOutputFailed(output map[string]any) (bool, string) {
	if output == nil {
		return false, ""
	}
	for _, key := range []string{"status", "result", "decision"} {
		value, ok := output[key].(string)
		if !ok {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch normalized {
		case "failed", "failure", "error", "erro", "falha", "falhou":
			return true, agentBlockReason(output, value)
		}
	}
	return false, ""
}

func agentBlockReason(output map[string]any, fallback string) string {
	if blockers, ok := output["blockers"].([]any); ok && len(blockers) > 0 {
		parts := make([]string, 0, minInt(len(blockers), 3))
		for _, blocker := range blockers[:minInt(len(blockers), 3)] {
			if text, ok := blocker.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	for _, key := range []string{"blocker", "reason", "message", "nextStep"} {
		if text, ok := output[key].(string); ok && strings.TrimSpace(text) != "" {
			return truncateText(strings.TrimSpace(text), 600)
		}
	}
	return "agent reported " + strings.TrimSpace(fallback)
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case model.RunStatusCompleted, model.RunStatusBlocked, model.RunStatusFailed, model.RunStatusCanceled, model.RunStatusInterrupted:
		return true
	default:
		return false
	}
}

func levelForStatus(status string) string {
	if status == model.RunStatusCompleted {
		return "info"
	}
	if status == model.RunStatusBlocked {
		return "warning"
	}
	if status == model.RunStatusCanceled || status == model.RunStatusInterrupted {
		return "warning"
	}
	return "error"
}

func messageForStatus(status, errorMessage string) string {
	if errorMessage != "" {
		return errorMessage
	}
	switch status {
	case model.RunStatusCompleted:
		return "Run completed"
	case model.RunStatusBlocked:
		return "Run blocked: an agent reported that it could not safely continue"
	case model.RunStatusCanceled:
		return "Run canceled"
	case model.RunStatusInterrupted:
		return "Run interrupted"
	default:
		return "Run failed"
	}
}

func maxInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func effectiveAgentRoots(agentRoots, projectRoots []string) []string {
	normalizedProjectRoots, projectErr := security.NormalizeRoots(projectRoots)
	if projectErr != nil {
		return nil
	}
	if len(agentRoots) == 0 {
		return normalizedProjectRoots
	}
	normalizedAgentRoots, agentErr := security.NormalizeRoots(agentRoots)
	if agentErr != nil {
		return normalizedProjectRoots
	}
	kept := make([]string, 0, len(normalizedAgentRoots))
	for _, root := range normalizedAgentRoots {
		if security.IsWithinRoots(root, normalizedProjectRoots) {
			kept = append(kept, root)
		}
	}
	if len(kept) == 0 {
		return normalizedProjectRoots
	}
	return kept
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
